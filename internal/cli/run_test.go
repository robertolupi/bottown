package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

func TestStoppedHandover(t *testing.T) {
	rec := runs.Record{ID: "r-stop", Kind: policy.Discuss, Actor: "claude", Family: "anthropic", SHA: "abc123", StopRequested: true}
	got := handoverText(rec, runs.StatusFailed, 0, "harness done")
	for _, want := range []string{"**failed**", "Stopped by the owner.", "harness done"} {
		if !strings.Contains(got, want) {
			t.Errorf("handover lacks %q:\n%s", want, got)
		}
	}
}

func TestHandoverAfterRunCancellation(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/forgejo/api/v1/repos/bottown/bottown/issues/236/comments" {
			t.Errorf("handover request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer run-key" {
			t.Errorf("handover authorization: %q", r.Header.Get("Authorization"))
		}
		var comment map[string]string
		if err := json.NewDecoder(r.Body).Decode(&comment); err != nil {
			t.Error(err)
		}
		body = comment["body"]
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // The launcher is cancelled before its harness exits.
	rc := &runContext{inst: config.Instance{ServerPort: srv.Listener.Addr().(*net.TCPAddr).Port}}
	run := secrets.Run{Kind: policy.Discuss, Org: "bottown", Repo: "bottown", Issue: 236}
	rec := runs.Record{ID: "r-cancel", Kind: policy.Discuss, Actor: "claude", Family: "anthropic", SHA: "abc123"}
	if err := handover(ctx, rc, run, rec, "run-key", runs.StatusFailed, -1, "interrupted"); err != nil {
		t.Fatalf("handover after cancellation: %v", err)
	}
	if !strings.Contains(body, "interrupted") {
		t.Errorf("handover body: %q", body)
	}
}

// A plan run hands over like an implementer: the branch and its head.
func TestPlanHandover(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	rec := runs.Record{ID: "r-plan", Kind: policy.Plan, Actor: "claude", Family: "anthropic", Branch: "issue/7", Worktree: dir}
	got := handoverText(rec, runs.StatusSucceeded, 0, "planned")
	for _, want := range []string{"· plan ·", "Branch `issue/7` at `", "planned"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan handover lacks %q:\n%s", want, got)
		}
	}
}

// A run that chose a model says which, in the handover, run show and run ls.
func TestModelShown(t *testing.T) {
	rec := runs.Record{ID: "r-mod", Kind: policy.Verify, Actor: "agy", Family: "google", SHA: "abc123", Spec: "gemini-3.8-flash", Status: runs.StatusSucceeded}
	if got := handoverText(rec, runs.StatusSucceeded, 0, "ok"); !strings.Contains(got, "agy (google) · model `gemini-3.8-flash` · batch") {
		t.Errorf("handover:\n%s", got)
	}
	var b strings.Builder
	printRunShow(colorOut(&b), &rec, nil, nil, "", "")
	if !strings.Contains(b.String(), "as agy (google, model gemini-3.8-flash)") {
		t.Errorf("run show:\n%s", b.String())
	}
	b.Reset()
	if err := printRunLs(colorOut(&b), []runs.Record{rec}, []secrets.Run{{ID: "r-srv", Actor: "crush", Spec: "openrouter:~deepseek/latest", Expires: time.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MODEL", "gemini-3.8-flash", "openrouter:~deepseek/latest"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("run ls lacks %q:\n%s", want, b.String())
		}
	}
	rec.Spec = ""
	if got := handoverText(rec, runs.StatusSucceeded, 0, "ok"); strings.Contains(got, "model") {
		t.Errorf("handover without a model:\n%s", got)
	}
}

// The owner's --message is in the handover, so the issue shows what the
// agent was told beyond it.
func TestMessageHandover(t *testing.T) {
	rec := runs.Record{ID: "r-msg", Kind: policy.Verify, Actor: "codex", Family: "openai", SHA: "abc123", Message: "also read\nthe epic #123"}
	got := handoverText(rec, runs.StatusSucceeded, 0, "approved")
	if want := "Owner's note:" + quoteLines(rec.Message); !strings.Contains(got, want) {
		t.Errorf("handover lacks %q:\n%s", want, got)
	}
	rec.Message = ""
	if got := handoverText(rec, runs.StatusSucceeded, 0, "approved"); strings.Contains(got, "Owner's note") {
		t.Errorf("handover without a message:\n%s", got)
	}
}

// The commands that start implement, verify and plan runs take --message.
func TestMessageFlags(t *testing.T) {
	root := newRoot()
	for _, path := range [][]string{{"run", "implement"}, {"run", "verify"}, {"run", "plan"}, {"milestone", "plan"}, {"flow"}} {
		c, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		if f := c.Flags().Lookup("message"); f == nil || f.Shorthand != "m" {
			t.Errorf("%v lacks -m/--message", path)
		}
	}
}

// `run plan` and its alias `milestone plan` launch plan runs as an actor.
func TestPlanCommands(t *testing.T) {
	root := newRoot()
	for _, path := range [][]string{{"run", "plan"}, {"milestone", "plan"}} {
		c, _, err := root.Find(path)
		if err != nil || c.Name() != "plan" {
			t.Fatalf("%v: %v, %v", path, c, err)
		}
		if f := c.Flags().Lookup("actor"); f == nil || f.Shorthand != "a" || c.Flags().Lookup("interactive") == nil {
			t.Errorf("%v lacks -a/--actor or -I/--interactive", path)
		}
	}
}

// startHarness starts a shell that spawns a long child, the way a harness
// spawns MCP servers, and returns the child's pid once it is running. The
// trap is set after the fork, so the child cannot inherit it.
func startHarness(t *testing.T, group bool) (*exec.Cmd, int) {
	t.Helper()
	c := exec.Command("sh", "-c", `sleep 300 & trap 'exit 0' TERM; echo $!; while :; do sleep 0.1; done`)
	if group {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(child, syscall.SIGKILL)
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	return c, child
}

func gone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestSignalHarnessGroup(t *testing.T) {
	c, child := startHarness(t, true)
	if err := signalHarness(c.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := c.Wait(); err != nil {
		t.Errorf("harness did not exit cleanly on TERM: %v", err)
	}
	if !gone(child) {
		t.Errorf("harness child %d survived the stop", child)
	}
}

func TestSignalHarnessNoGroup(t *testing.T) {
	c, child := startHarness(t, false)
	if err := signalHarness(c.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := c.Wait(); err != nil {
		t.Errorf("harness did not exit cleanly on TERM: %v", err)
	}
	if syscall.Kill(child, 0) != nil {
		t.Errorf("child %d outside a harness group was signalled", child)
	}
}

// A launcher killed while it downloads a run's attachments, before the run
// is running, leaves a preparing record: gc keeps the run while the launcher
// lives, then revokes it and removes its attachments once it is dead.
func TestGCRemovesAttachmentsOfLauncherKilledWhilePreparing(t *testing.T) {
	if dir := os.Getenv("BOTTOWN_GC_TEST_DIR"); dir != "" {
		// The launcher: the same steps as startRun up to the download.
		ws := &runs.Workspace{RunsDir: dir}
		attach := ws.AttachmentsDir("r-cafe")
		rec := runs.Record{ID: "r-cafe", Kind: policy.Implement, Status: runs.StatusPreparing, PID: os.Getpid(), Started: time.Now().UTC(), Key: "k", Attachments: attach}
		if err := ws.Save(rec); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(attach, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(attach, ".download-1"), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Stdout.WriteString("ready\n")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // killed here
		return
	}
	mirror := filepath.Join(t.TempDir(), "mirror.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", mirror).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ws := &runs.Workspace{RunsDir: t.TempDir(), Mirror: mirror}
	c := exec.Command(os.Args[0], "-test.run=^TestGCRemovesAttachmentsOfLauncherKilledWhilePreparing$")
	c.Env = append(os.Environ(), "BOTTOWN_GC_TEST_DIR="+ws.RunsDir)
	stdin, _ := c.StdinPipe()
	defer stdin.Close()
	stdout, _ := c.StdoutPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		_ = c.Process.Kill()
		_ = c.Wait()
		t.Fatalf("launcher: %q, %v", line, err)
	}
	attach := ws.AttachmentsDir("r-cafe")

	var revoked []string
	revoke := func(r runs.Record) error { revoked = append(revoked, r.ID); return nil }
	recs, err := ws.Records(io.Discard)
	if err != nil || len(recs) != 1 || recs[0].Status != runs.StatusPreparing {
		t.Fatalf("live launcher: records %+v, %v", recs, err)
	}
	if removed, keep := gcLocal(ws, recs, true, nil, os.Stderr, revoke); removed != 0 || !slices.Equal(keep, []string{"r-cafe"}) || revoked != nil {
		t.Errorf("live launcher: gc removed %d, kept %v, revoked %v", removed, keep, revoked)
	}
	if _, err := os.Stat(filepath.Join(attach, ".download-1")); err != nil {
		t.Fatalf("gc removed a live run's download: %v", err)
	}

	_ = c.Process.Kill()
	_ = c.Wait()
	recs, err = ws.Records(io.Discard)
	if err != nil || len(recs) != 1 || recs[0].Status != runs.StatusDead {
		t.Fatalf("killed launcher: records %+v, %v", recs, err)
	}
	if removed, keep := gcLocal(ws, recs, false, nil, os.Stderr, revoke); removed != 1 || keep != nil || !slices.Equal(revoked, []string{"r-cafe"}) {
		t.Errorf("killed launcher: gc removed %d, kept %v, revoked %v", removed, keep, revoked)
	}
	if _, err := os.Stat(attach); !os.IsNotExist(err) {
		t.Errorf("attachments survive gc: %v", err)
	}
	if _, err := os.Stat(ws.Path("r-cafe", ".json")); !os.IsNotExist(err) {
		t.Errorf("record survives gc: %v", err)
	}
}

// A file in the runs directory that is not a run record, such as a stray
// <id>.notes.json, unmarshals into a record with no id. gc must skip it and
// warn once naming the file, rather than treat it as a finished run whose
// empty id is the runs directory itself.
func TestGCSkipsRecordWithoutID(t *testing.T) {
	ws := &runs.Workspace{RunsDir: t.TempDir()}
	stray := filepath.Join(ws.RunsDir, "r-2ecab347.notes.json")
	if err := os.WriteFile(stray, []byte("{\n  \"sha\": \"abc\",\n  \"steps\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	recs, err := ws.Records(&warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("records include a non-run file: %+v", recs)
	}
	if got := strings.Count(warn.String(), "r-2ecab347.notes.json"); got != 1 {
		t.Fatalf("warnings naming the file = %d, want 1: %q", got, warn.String())
	}
	removed, keep := gcLocal(ws, recs, true, nil, io.Discard, func(runs.Record) error { return nil })
	if removed != 0 || keep != nil {
		t.Fatalf("gc removed %d, kept %v", removed, keep)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("gc removed a file it does not own: %v", err)
	}
	if _, err := os.Stat(ws.RunsDir); err != nil {
		t.Fatalf("gc removed the runs directory: %v", err)
	}
}

// A corrupt run record (#244) is not dropped: run ls lists the run as
// unknown, with the file and the parse error, and gc warns and keeps it,
// its worktree and its server record.
func TestUnreadableRecordListedAndKept(t *testing.T) {
	ws := &runs.Workspace{RunsDir: t.TempDir()}
	corrupt := ws.Path("r-bad1", ".json")
	if err := os.WriteFile(corrupt, []byte("{\"id\": \"r-bad1\", \"status\": "), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := ws.WorktreeDir("r-bad1")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	recs, err := ws.Records(io.Discard)
	if err != nil || len(recs) != 1 || recs[0].Status != runs.StatusUnknown {
		t.Fatalf("records %+v, %v", recs, err)
	}

	var b strings.Builder
	remote := []secrets.Run{{ID: "r-bad1", Kind: policy.Implement, Issue: 7, Actor: "claude", Expires: time.Now().Add(time.Hour)}}
	if err := printRunLs(colorOut(&b), recs, remote); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"r-bad1", "unknown", "live", "#7", "claude", "r-bad1.json: unreadable run record", recs[0].LoadError} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("run ls lacks %q:\n%s", want, b.String())
		}
	}

	var warn bytes.Buffer
	removed, keep := gcLocal(ws, recs, true, nil, &warn, func(r runs.Record) error { t.Errorf("gc revoked %s", r.ID); return nil })
	if removed != 0 || !slices.Equal(keep, []string{"r-bad1"}) {
		t.Errorf("gc removed %d, kept %v", removed, keep)
	}
	if !strings.Contains(warn.String(), "r-bad1.json") || !strings.Contains(warn.String(), "kept") {
		t.Errorf("gc warning: %q", warn.String())
	}
	for _, p := range []string{corrupt, wt} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("gc removed %s: %v", p, err)
		}
	}
}

func TestExpiredLive(t *testing.T) {
	now := time.Now().UTC()
	ended := now.Add(-time.Hour)
	live := []secrets.Run{
		{ID: "r-active", Expires: now.Add(time.Hour)},
		{ID: "r-expired", Expires: now.Add(-time.Minute)},
		{ID: "r-launched", Expires: now.Add(-time.Minute)},
		{ID: "r-preparing", Expires: now.Add(-time.Minute)},
		{ID: "r-dead-launcher", Expires: now.Add(-time.Minute)},
		{ID: "r-ended", Expires: now.Add(-time.Minute), Ended: &ended},
		{ID: "r-no-expiry"},
	}
	local := []runs.Record{
		{ID: "r-launched", Status: runs.StatusRunning, PID: os.Getpid()},
		{ID: "r-preparing", Status: runs.StatusPreparing, PID: os.Getpid()},
		{ID: "r-dead-launcher", Status: runs.StatusDead},
	}
	got := expiredLive(live, local, now)
	if want := []string{"r-expired", "r-dead-launcher"}; !slices.Equal(got, want) {
		t.Errorf("expiredLive = %v, want %v", got, want)
	}
}

// A dead run whose revoke fails (the server is unreachable, say) keeps its
// local record, its only trace of a server run that may still be live
// (#232), and a later gc retries and removes it.
func TestGCKeepsDeadRunWhenRevokeFails(t *testing.T) {
	ws := &runs.Workspace{RunsDir: t.TempDir()}
	if err := ws.Save(runs.Record{ID: "r-dead", Status: runs.StatusRunning, PID: -1}); err != nil {
		t.Fatal(err)
	}
	recs, err := ws.Records(io.Discard)
	if err != nil || len(recs) != 1 || recs[0].Status != runs.StatusDead {
		t.Fatalf("records %+v, %v", recs, err)
	}
	var warn bytes.Buffer
	removed, keep := gcLocal(ws, recs, true, nil, &warn, func(runs.Record) error { return errors.New("connection refused") })
	if removed != 0 || !slices.Equal(keep, []string{"r-dead"}) {
		t.Errorf("failed revoke: gc removed %d, kept %v", removed, keep)
	}
	if !strings.Contains(warn.String(), "revoke dead run r-dead: connection refused; kept") {
		t.Errorf("gc warning: %q", warn.String())
	}
	if _, err := os.Stat(ws.Path("r-dead", ".json")); err != nil {
		t.Fatalf("gc removed the record of a run it could not revoke: %v", err)
	}

	var revoked []string
	removed, keep = gcLocal(ws, recs, true, nil, io.Discard, func(r runs.Record) error { revoked = append(revoked, r.ID); return nil })
	if removed != 1 || keep != nil || !slices.Equal(revoked, []string{"r-dead"}) {
		t.Errorf("retry: gc removed %d, kept %v, revoked %v", removed, keep, revoked)
	}
}

// A launcher retries its end-run until the server answers.
func TestRetryEnd(t *testing.T) {
	defer func(b []time.Duration) { runs.EndBackoff = b }(runs.EndBackoff)
	runs.EndBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	calls := 0
	err := runs.RetryEnd(context.Background(), func(context.Context) error {
		if calls++; calls < 3 {
			return errors.New("connection refused")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("retryEndRun: %v after %d calls, want success after 3", err, calls)
	}
	calls = 0
	err = runs.RetryEnd(context.Background(), func(context.Context) error { calls++; return errors.New("connection refused") })
	if err == nil || calls != len(runs.EndBackoff)+1 {
		t.Errorf("retryEndRun: %v after %d calls, want failure after %d", err, calls, len(runs.EndBackoff)+1)
	}
}

// A launcher whose end-run fails (the server is restarting, say) leaves its
// record ended but the server run live (#243): the next run gc revokes it,
// whatever its expiry, a failed one too without --failed, and removes only
// what it may.
func TestGCRevokesRunItsLauncherCouldNotEnd(t *testing.T) {
	defer func(b []time.Duration) { runs.EndBackoff = b }(runs.EndBackoff)
	runs.EndBackoff = []time.Duration{time.Millisecond}

	var mu sync.Mutex
	up := false
	live := map[string]secrets.Run{}
	var revoked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			http.Error(w, "restarting", http.StatusServiceUnavailable)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs":
			var out []secrets.Run
			for _, run := range live {
				out = append(out, run)
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/runs/"):
			var end server.EndRunRequest
			if err := json.NewDecoder(r.Body).Decode(&end); err != nil {
				t.Error(err)
			}
			id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
			if end.Status == "revoked" {
				revoked = append(revoked, id)
			}
			delete(live, id)
			_ = json.NewEncoder(w).Encode(secrets.Run{ID: id})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/purge":
			_ = json.NewEncoder(w).Encode(map[string][]string{"purged": nil})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			_, _ = w.Write([]byte("[]"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	mirror := filepath.Join(t.TempDir(), "mirror.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", mirror).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ws := &runs.Workspace{RunsDir: t.TempDir(), Mirror: mirror, Dir: t.TempDir()}
	rc := &runContext{inst: config.Instance{ServerPort: srv.Listener.Addr().(*net.TCPAddr).Port}, ws: ws, repo: "demo", key: "owner-key"}

	// Both launchers fail to end their runs, which stay live, unexpired.
	expires := time.Now().Add(12 * time.Hour)
	for id, status := range map[string]string{"r-d0e": runs.StatusSucceeded, "r-fa1": runs.StatusFailed} {
		live[id] = secrets.Run{ID: id, Expires: expires}
		err := runs.RetryEnd(context.Background(), func(ctx context.Context) error {
			return callServer(ctx, rc.inst, rc.key, "DELETE", "/v1/runs/"+id, server.EndRunRequest{Status: status}, nil)
		})
		if err == nil {
			t.Fatalf("end run %s succeeded on a server that is down", id)
		}
		ended := time.Now().UTC()
		if err := ws.Save(runs.Record{ID: id, Status: status, Ended: &ended, PID: -1}); err != nil {
			t.Fatal(err)
		}
	}
	// One more, ended on the server too, that gc leaves alone.
	ended := time.Now().UTC()
	if err := ws.Save(runs.Record{ID: "r-c1ea", Status: runs.StatusSucceeded, Ended: &ended, PID: -1}); err != nil {
		t.Fatal(err)
	}

	up = true
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out, warn bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&warn)
	if err := gcRuns(cmd, rc, false, false, 0); err != nil {
		t.Fatalf("gc: %v (%s)", err, warn.String())
	}
	slices.Sort(revoked)
	if want := []string{"r-d0e", "r-fa1"}; !slices.Equal(revoked, want) {
		t.Errorf("gc revoked %v, want %v\n%s\n%s", revoked, want, out.String(), warn.String())
	}
	if len(live) != 0 {
		t.Errorf("still live on the server: %v", live)
	}
	for id, kept := range map[string]bool{"r-d0e": false, "r-c1ea": false, "r-fa1": true} {
		if _, err := os.Stat(ws.Path(id, ".json")); (err == nil) != kept {
			t.Errorf("%s: record kept = %v, want %v", id, err == nil, kept)
		}
	}

	// The next gc has nothing left to revoke.
	revoked = nil
	if err := gcRuns(cmd, rc, false, false, 0); err != nil || revoked != nil {
		t.Errorf("second gc: %v, revoked %v", err, revoked)
	}
}

func TestOrphanLive(t *testing.T) {
	now := time.Now().UTC()
	old, fresh := now.Add(-time.Hour), now.Add(-time.Second)
	ended := now.Add(-time.Minute)
	live := []secrets.Run{
		{ID: "r-orphan", Actor: "codex", StartedBy: "owner", Created: old},
		{ID: "r-fresh", Actor: "codex", StartedBy: "owner", Created: fresh},
		{ID: "r-recorded", Actor: "codex", StartedBy: "owner", Created: old},
		{ID: "r-self", Actor: "claude", StartedBy: "claude", Created: old},
		{ID: "r-unknown", Actor: "codex", Created: old},
		{ID: "r-ended", Actor: "codex", StartedBy: "owner", Created: old, Ended: &ended},
	}
	local := []runs.Record{{ID: "r-recorded", Status: runs.StatusDead}}
	if got, want := orphanLive(live, local, now), []string{"r-orphan"}; !slices.Equal(got, want) {
		t.Errorf("orphanLive = %v, want %v", got, want)
	}
}

// run stop --revoke ends a live run of this repository that no running
// launcher holds: one with no local record (#232) or a dead launcher.
func TestRevokeRefusal(t *testing.T) {
	ended := time.Now().UTC()
	live := secrets.Run{ID: "r-1", Org: "bottown", Repo: "demo"}
	for _, c := range []struct {
		name  string
		run   secrets.Run
		local *runs.Record
		want  string
	}{
		{"no local record", live, nil, ""},
		{"dead launcher", live, &runs.Record{ID: "r-1", Status: runs.StatusRunning, PID: -1}, ""},
		{"ended locally", live, &runs.Record{ID: "r-1", Status: runs.StatusFailed, Ended: &ended}, ""},
		{"live launcher", live, &runs.Record{ID: "r-1", Status: runs.StatusRunning, PID: os.Getpid()}, "`bottown run stop r-1`"},
		{"preparing launcher", live, &runs.Record{ID: "r-1", Status: runs.StatusPreparing, PID: os.Getpid()}, "still running"},
		{"other repo", secrets.Run{ID: "r-1", Org: "bottown", Repo: "other"}, nil, "not a run of bottown/demo"},
		{"ended", secrets.Run{ID: "r-1", Org: "bottown", Repo: "demo", Ended: &ended, Status: "failed"}, nil, "already ended (failed)"},
	} {
		err := revokeRefusal(c.run, c.local, "bottown", "demo")
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: revokeRefusal = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestOpenRunContextRefusesActorAndRunWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	mainDir := filepath.Join(dir, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Git(mainDir, nil, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Git(mainDir, nil, "config", "extensions.worktreeConfig", "true"); err != nil {
		t.Fatal(err)
	}

	t.Run("actor worktree refused", func(t *testing.T) {
		actorDir := filepath.Join(dir, "crush")
		if err := os.MkdirAll(actorDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := runs.Git(actorDir, nil, "init", "-q"); err != nil {
			t.Fatal(err)
		}
		if _, err := runs.Git(actorDir, nil, "config", "extensions.worktreeConfig", "true"); err != nil {
			t.Fatal(err)
		}
		if _, err := runs.Git(actorDir, nil, "config", "--worktree", gitActorKey, "crush"); err != nil {
			t.Fatal(err)
		}
		t.Chdir(actorDir)
		_, err := openRunContext(&globals{})
		if err == nil {
			t.Fatal("expected error in actor worktree, got nil")
		}
		if !strings.Contains(err.Error(), "is actor crush's worktree; run this in the main checkout") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("run worktree refused", func(t *testing.T) {
		runDir := filepath.Join(mainDir, ".bottown", "runs", "r-test")
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := runs.Git(runDir, nil, "init", "-q"); err != nil {
			t.Fatal(err)
		}
		t.Chdir(runDir)
		_, err := openRunContext(&globals{})
		if err == nil {
			t.Fatal("expected error in run worktree, got nil")
		}
		if !strings.Contains(err.Error(), "this is a run worktree; run this in the main checkout") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestObservedFailure(t *testing.T) {
	cases := []struct {
		name    string
		ctxErr  error
		stopped bool
		code    int
		cause   string
	}{
		{"time limit", context.DeadlineExceeded, false, -1, secrets.FailureTimeout},
		{"cancelled launcher", context.Canceled, false, -1, secrets.FailureInterrupted},
		{"owner stop", nil, true, -1, secrets.FailureStopped},
		{"harness exit", nil, false, 1, secrets.FailureHarnessExit},
		{"clean exit", nil, false, 0, ""},
	}
	for _, c := range cases {
		f := observedFailure(c.ctxErr, c.stopped, c.code, 90*time.Second)
		if c.cause == "" {
			if f != nil {
				t.Errorf("%s: observed %+v, want none", c.name, f)
			}
			continue
		}
		if f == nil || f.Cause != c.cause || f.Detail == "" {
			t.Errorf("%s: observed %+v, want cause %s", c.name, f, c.cause)
		}
	}
	if f := observedFailure(nil, false, 3, time.Second); f.ExitCode == nil || *f.ExitCode != 3 {
		t.Errorf("harness exit code not kept: %+v", f)
	}
	if f := observedFailure(context.DeadlineExceeded, false, -1, 90*time.Second); !strings.Contains(f.Detail, "1m30s") {
		t.Errorf("timeout detail %q lacks the elapsed time", f.Detail)
	}
}

// The launcher's error is a failure only until the digest is final: after
// it, the error returned for a failed run is its outcome, which must not
// read as a launcher failure. A clean interactive exit the owner marks
// failed observes nothing, and the digest records nothing (#132).
func TestLauncherFailure(t *testing.T) {
	boom := errors.New("git worktree add: exit status 128")
	if f := launcherFailure(launchStarting, boom); f == nil || f.Cause != secrets.FailureStartup || f.Detail != boom.Error() {
		t.Errorf("before the harness: %+v, want startup with the error", f)
	}
	if f := launcherFailure(launchHarnessRan, boom); f == nil || f.Cause != secrets.FailureInterrupted {
		t.Errorf("after the harness ran: %+v, want interrupted", f)
	}
	for _, p := range []launchPhase{launchStarting, launchHarnessRan, launchFinalized} {
		if f := launcherFailure(p, nil); f != nil {
			t.Errorf("phase %d without an error: %+v", p, f)
		}
	}
	// startRun's end for an interactive run that exited 0 and that the
	// owner marked failed: nothing observed, then its outcome error.
	observed := observedFailure(nil, false, 0, time.Second)
	if f := launcherFailure(launchFinalized, &runs.FailedRunError{ID: "r-1", Status: runs.StatusFailed}); observed != nil || f != nil {
		t.Errorf("clean exit marked failed: observed %+v, launcher %+v; want neither", observed, f)
	}
}

// gc leaves no lock files for the runs it removes, nor for runs removed
// before it removed them (#233), and keeps those of runs it keeps.
func TestGCRemovesLocks(t *testing.T) {
	ws := &runs.Workspace{RunsDir: t.TempDir()}
	for _, r := range []runs.Record{{ID: "r-d1", Status: runs.StatusSucceeded}, {ID: "r-f1", Status: runs.StatusFailed}} {
		if err := ws.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(ws.Path("r-01d", ".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := ws.Records(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if removed, _ := gcLocal(ws, recs, false, nil, io.Discard, func(runs.Record) error { return nil }); removed != 1 {
		t.Fatalf("removed %d runs, want 1", removed)
	}
	locks, _ := filepath.Glob(filepath.Join(ws.RunsDir, "*.lock"))
	if want := []string{ws.Path("r-f1", ".lock")}; !slices.Equal(locks, want) {
		t.Fatalf("lock files %v, want %v", locks, want)
	}
}

// gc removes the e2e logs of issues with no open PR, and with --older-than
// old ones of open PRs too, and the web UI's logs of flows not running. A
// live flow's log, a recent e2e log of an open PR and files that only look
// like flow logs stay.
func TestGCFlowLogs(t *testing.T) {
	dir := t.TempDir()
	ws := &runs.Workspace{Dir: dir, RunsDir: filepath.Join(dir, "runs")}
	if err := os.MkdirAll(ws.RunsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	write := func(path string, body string, age time.Duration) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	e2e := func(issue int, sha string) string {
		return filepath.Join(ws.RunsDir, "flow-"+strconv.Itoa(issue)+"-"+sha+".e2e.log")
	}
	flowLog := func(issue int) string { return filepath.Join(dir, "flow-"+strconv.Itoa(issue)+".log") }
	flowInfo := func(issue int) string { return filepath.Join(dir, "flow-"+strconv.Itoa(issue)+".json") }

	write(e2e(1, "aaaaaaa"), "merged PR", time.Minute)        // no open PR: removed
	write(e2e(2, "bbbbbbb"), "open PR, recent", time.Minute)  // kept
	write(e2e(2, "ccccccc"), "open PR, old", 200*time.Hour)   // removed with --older-than
	write(filepath.Join(ws.RunsDir, "flow-x.e2e.log"), "", 0) // not a flow's: kept
	write(flowLog(3), "no record", 0)                         // removed
	write(flowLog(4), "dead flow", 0)                         // removed, with its record
	write(flowInfo(4), `{"issue": 4, "pid": 999999999}`, 0)
	write(flowLog(5), "live flow", 0) // kept
	write(flowInfo(5), `{"issue": 5, "pid": `+strconv.Itoa(os.Getpid())+`}`, 0)
	write(filepath.Join(dir, "run-implement-6.log"), "", 0) // not a flow log: kept

	if n := gcFlowLogs(ws, map[int64]bool{2: true}, 0, now, io.Discard); n != 3 {
		t.Errorf("without --older-than removed %d logs, want 3", n)
	}
	for _, p := range []string{e2e(1, "aaaaaaa"), flowLog(3), flowLog(4), flowInfo(4)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survives gc: %v", filepath.Base(p), err)
		}
	}
	for _, p := range []string{e2e(2, "bbbbbbb"), e2e(2, "ccccccc"), flowLog(5), flowInfo(5),
		filepath.Join(ws.RunsDir, "flow-x.e2e.log"), filepath.Join(dir, "run-implement-6.log")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("gc removed %s: %v", filepath.Base(p), err)
		}
	}
	if n := gcFlowLogs(ws, map[int64]bool{2: true}, 168*time.Hour, now, io.Discard); n != 1 {
		t.Errorf("with --older-than removed %d logs, want 1", n)
	}
	if _, err := os.Stat(e2e(2, "ccccccc")); !os.IsNotExist(err) {
		t.Errorf("old e2e log of an open PR survives --older-than: %v", err)
	}
	if _, err := os.Stat(e2e(2, "bbbbbbb")); err != nil {
		t.Errorf("--older-than removed a recent e2e log: %v", err)
	}
}

func TestIssueBranchNumber(t *testing.T) {
	for ref, want := range map[string]int64{"issue/278": 278, "issue/0": 0, "issue/x": 0, "feature/3": 0, "3": 0} {
		if n, ok := issueBranchNumber(ref); ok != (want > 0) || ok && n != want {
			t.Errorf("issueBranchNumber(%q) = %d, %v; want %d", ref, n, ok, want)
		}
	}
}
