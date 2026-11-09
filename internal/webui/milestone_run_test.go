package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
)

func writeMilestoneRunRecord(t *testing.T, top string, rec MilestoneRunRecord) {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(top, ".bottown"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(top, ".bottown", "milestone-"+rec.Slug+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// newMilestoneRunServer is a UI server whose bottown is a script that
// writes its arguments to args and its PID to pid, then waits, with a
// child in its process group, until it is signalled.
func newMilestoneRunServer(t *testing.T) (*Server, string) {
	t.Helper()
	_, port := newRunsFixtureServer(t, defaultRunsFixture())
	top := t.TempDir()
	fake := filepath.Join(top, "fake_bottown.sh")
	script := "#!/bin/sh\necho \"$@\" > " + filepath.Join(top, "args") + "\necho $$ > " + filepath.Join(top, "pid") + "\nsleep 30 &\nwait\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Config{
		Instance:    config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:     top,
		FullRepo:    "bottown/bottown",
		BottownPath: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, top
}

// TestActions_MilestoneRunRefusals: a spec the agents parser refuses, and
// other bad requests, come back with the parser's message and start
// nothing.
func TestActions_MilestoneRunRefusals(t *testing.T) {
	s, top := newMilestoneRunServer(t)
	ctx := context.Background()
	for _, tc := range []struct {
		req  MilestoneRunRequest
		want string
	}{
		{MilestoneRunRequest{Slug: "demo", Agents: "claude=*", Parallel: 1}, `family "anthropic" cannot be verified by another family`},
		{MilestoneRunRequest{Slug: "demo", Agents: "claude=*,nobody=1", Parallel: 1}, `nobody`},
		{MilestoneRunRequest{Slug: "demo", Agents: "claude=*,claude=1", Parallel: 1}, "appears more than once"},
		{MilestoneRunRequest{Slug: "demo", Agents: "", Parallel: 1}, "agents spec is empty"},
		{MilestoneRunRequest{Slug: "demo", Agents: "claude=*,agy=1", Parallel: 0}, "parallel must be at least 1"},
		{MilestoneRunRequest{Slug: "../x", Agents: "claude=*,agy=1", Parallel: 1}, "invalid plan slug"},
	} {
		if _, err := s.ExecuteMilestoneRun(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.req, err, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(top, "args")); err == nil {
		t.Fatal("a refused milestone run started")
	}

	// Through the endpoint, the parser's message is the action's error.
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"slug":"demo","agents":"claude=*","parallel":2}`)
	s.handleActionMilestoneRun(rec, httptest.NewRequest(http.MethodPost, "/api/actions/milestone-run", body))
	var res ActionResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusBadRequest || res.Success || !strings.Contains(res.Error, "cannot be verified by another family") {
		t.Fatalf("endpoint: %d %+v", rec.Code, res)
	}
}

// TestActions_MilestoneRunStartAndStop: start runs `milestone run` in the
// background with a record the Milestones view reads, refuses a second
// one, and stop signals its process group and removes the record.
func TestActions_MilestoneRunStartAndStop(t *testing.T) {
	s, top := newMilestoneRunServer(t)
	ctx := context.Background()

	out, err := s.ExecuteMilestoneRun(ctx, MilestoneRunRequest{Slug: "demo", Agents: " claude=*,agy:gemini-3=1 ", Parallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Milestone run started for demo") || !strings.Contains(out, ".bottown/milestone-demo.log") {
		t.Errorf("output %q", out)
	}
	var args, pidText []byte
	// A new script's first exec can wait seconds on macOS's security scan
	// on a loaded host: these deadlines only bound a wait for it.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		args, _ = os.ReadFile(filepath.Join(top, "args"))
		pidText, _ = os.ReadFile(filepath.Join(top, "pid"))
		if len(args) > 0 && len(pidText) > 0 {
			break
		}
	}
	if got, want := strings.TrimSpace(string(args)), "-i prod milestone run demo --agents claude=*,agy:gemini-3=1 --parallel 2"; got != want {
		t.Fatalf("args %q, want %q", got, want)
	}

	rec := s.getMilestoneRun("demo")
	if rec == nil || rec.Agents != "claude=*,agy:gemini-3=1" || rec.Parallel != 2 || strings.TrimSpace(string(pidText)) != strconv.Itoa(rec.PID) {
		t.Fatalf("record %+v, script pid %s", rec, pidText)
	}
	if sc := s.milestoneScheduler("demo", time.Now()); sc == nil || sc.ID != "milestone-demo" || len(sc.Pool) != 2 {
		t.Fatalf("scheduler %+v", sc)
	}

	if _, err := s.ExecuteMilestoneRun(ctx, MilestoneRunRequest{Slug: "demo", Agents: "claude=*,agy=1", Parallel: 1}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start: %v", err)
	}

	out, err = s.ExecuteStop(ctx, "milestone-demo")
	if err != nil || out != "Milestone run for demo stopped" {
		t.Fatalf("stop: %q, %v", out, err)
	}
	// The whole group got the signal: the script and its sleep exit.
	for deadline := time.Now().Add(10 * time.Second); syscall.Kill(-rec.PID, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(-rec.PID, syscall.SIGKILL)
			t.Fatal("the milestone run's process group is still alive")
		}
	}
	if _, err := os.Stat(filepath.Join(top, ".bottown", "milestone-demo.json")); !os.IsNotExist(err) {
		t.Fatalf("record left behind: %v", err)
	}
	if _, err := s.ExecuteStop(ctx, "milestone-demo"); err == nil || !strings.Contains(err.Error(), "no active milestone run for demo") {
		t.Fatalf("stopping again: %v", err)
	}
}

// TestActions_MilestoneRunStaleRecord: a record whose process is gone is
// not running, and is removed.
func TestActions_MilestoneRunStaleRecord(t *testing.T) {
	s, top := newMilestoneRunServer(t)
	writeMilestoneRunRecord(t, top, MilestoneRunRecord{Slug: "demo", Agents: "claude=*", Parallel: 1, PID: 1 << 30})
	if rec := s.getMilestoneRun("demo"); rec != nil {
		t.Fatalf("stale record read as running: %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(top, ".bottown", "milestone-demo.json")); !os.IsNotExist(err) {
		t.Fatalf("stale record kept: %v", err)
	}
}
