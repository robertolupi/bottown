package cli

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
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
	printRunShow(&b, lipgloss.NewRenderer(&b), &rec, nil, nil, "", "")
	if !strings.Contains(b.String(), "as agy (google, model gemini-3.8-flash)") {
		t.Errorf("run show:\n%s", b.String())
	}
	b.Reset()
	if err := printRunLs(&b, lipgloss.NewRenderer(&b), []runs.Record{rec}, []secrets.Run{{ID: "r-srv", Actor: "crush", Spec: "openrouter:~deepseek/latest", Expires: time.Now().Add(time.Hour)}}); err != nil {
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
		attach := ws.AttachmentsDir("r-prep")
		rec := runs.Record{ID: "r-prep", Kind: policy.Implement, Status: runs.StatusPreparing, PID: os.Getpid(), Started: time.Now().UTC(), Key: "k", Attachments: attach}
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
	attach := ws.AttachmentsDir("r-prep")

	var revoked []string
	revoke := func(id string) { revoked = append(revoked, id) }
	recs, err := ws.Records()
	if err != nil || len(recs) != 1 || recs[0].Status != runs.StatusPreparing {
		t.Fatalf("live launcher: records %+v, %v", recs, err)
	}
	if removed, keep := gcLocal(ws, recs, true, os.Stderr, revoke); removed != 0 || !slices.Equal(keep, []string{"r-prep"}) || revoked != nil {
		t.Errorf("live launcher: gc removed %d, kept %v, revoked %v", removed, keep, revoked)
	}
	if _, err := os.Stat(filepath.Join(attach, ".download-1")); err != nil {
		t.Fatalf("gc removed a live run's download: %v", err)
	}

	_ = c.Process.Kill()
	_ = c.Wait()
	recs, err = ws.Records()
	if err != nil || len(recs) != 1 || recs[0].Status != runs.StatusDead {
		t.Fatalf("killed launcher: records %+v, %v", recs, err)
	}
	if removed, keep := gcLocal(ws, recs, false, os.Stderr, revoke); removed != 1 || keep != nil || !slices.Equal(revoked, []string{"r-prep"}) {
		t.Errorf("killed launcher: gc removed %d, kept %v, revoked %v", removed, keep, revoked)
	}
	if _, err := os.Stat(attach); !os.IsNotExist(err) {
		t.Errorf("attachments survive gc: %v", err)
	}
	if _, err := os.Stat(ws.Path("r-prep", ".json")); !os.IsNotExist(err) {
		t.Errorf("record survives gc: %v", err)
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

func TestOpenRunContextRefusesActorAndRunWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	mainDir := filepath.Join(dir, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git(mainDir, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(mainDir, "config", "extensions.worktreeConfig", "true"); err != nil {
		t.Fatal(err)
	}

	t.Run("actor worktree refused", func(t *testing.T) {
		actorDir := filepath.Join(dir, "crush")
		if err := os.MkdirAll(actorDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := git(actorDir, "init", "-q"); err != nil {
			t.Fatal(err)
		}
		if _, err := git(actorDir, "config", "extensions.worktreeConfig", "true"); err != nil {
			t.Fatal(err)
		}
		if _, err := git(actorDir, "config", "--worktree", gitActorKey, "crush"); err != nil {
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
		if _, err := git(runDir, "init", "-q"); err != nil {
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
