package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/runs"
)

// The flow's time limit ends a step's whole process group: a child that
// keeps the output open does not hold the check past it.
func TestRunChecksOwnGroupTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	l := &checkLog{w: io.Discard}
	start := time.Now()
	steps := runChecks(ctx, t.TempDir(), []checkSpec{{"e2e", "sleep 30 & sleep 30"}}, l, time.Now, true)
	if took := time.Since(start); took > 5*time.Second || len(steps) != 1 || steps[0].Status != stepFailed {
		t.Fatalf("steps = %+v after %s", steps, took)
	}
}

// The flow's checker runs the e2e commands of the default branch's
// .bottown.toml in a clean worktree at the PR head, keeps the log in the
// runs directory, and removes the worktree.
func TestFlowChecker(t *testing.T) {
	remote, top := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(remote, "init", "-q", "-b", "main")
	// The commands come from main; the head's own .bottown.toml is ignored.
	write(".bottown.toml", "e2e = [\"test -f fixed\", \"echo e2e ok\"]\n")
	git(remote, "add", ".")
	git(remote, "commit", "-q", "-m", "init")
	git(remote, "switch", "-q", "-c", "issue/9")
	write(".bottown.toml", "e2e = [\"true\"]\n")
	write("work", "x\n")
	git(remote, "add", ".")
	git(remote, "commit", "-q", "-m", "work")
	broken := git(remote, "rev-parse", "HEAD")
	write("fixed", "\n")
	git(remote, "add", ".")
	git(remote, "commit", "-q", "-m", "fix")
	fixed := git(remote, "rev-parse", "HEAD")
	git(remote, "switch", "-q", "main")
	git(top, "init", "-q")

	ws, err := runs.Open(top, "bottown", remote, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := flowChecker{ws: ws}
	ctx := context.Background()

	req := flow.CheckRequest{Repo: "demo", Issue: 9, PR: 12, SHA: broken}
	out, err := c.Check(ctx, req)
	if err != nil || out.Passed || out.Skipped || out.Log != c.LogPath(req) || filepath.Dir(out.Log) != ws.RunsDir {
		t.Fatalf("broken head: %+v, %v", out, err)
	}
	if !strings.HasPrefix(out.Description, "failed: test -f fixed (exit 1)") || !strings.Contains(out.Summary, "test -f fixed failed (exit 1)") {
		t.Errorf("broken head: description %q, summary %q", out.Description, out.Summary)
	}
	log, _ := os.ReadFile(out.Log)
	if !strings.Contains(string(log), "==> [e2e] test -f fixed") || !strings.Contains(string(log), broken) || strings.Contains(string(log), "e2e ok") {
		t.Errorf("broken log:\n%s", log)
	}

	req.SHA = fixed
	out, err = c.Check(ctx, req)
	if err != nil || !out.Passed || !strings.HasPrefix(out.Description, "passed: test -f fixed; echo e2e ok in ") {
		t.Fatalf("fixed head: %+v, %v", out, err)
	}
	if log, _ := os.ReadFile(out.Log); !strings.Contains(string(log), "e2e ok") {
		t.Errorf("fixed log:\n%s", log)
	}
	if wts := git(ws.Mirror, "worktree", "list"); strings.Contains(wts, "flow-9-") {
		t.Errorf("check worktrees left behind:\n%s", wts)
	}

	req.SHA = strings.Repeat("0", 40)
	if _, err := c.Check(ctx, req); err == nil || !strings.Contains(err.Error(), "not in the mirror") {
		t.Fatalf("unknown head: %v", err)
	}

	// e2e = [] on the default branch: no post-implement checks.
	write(".bottown.toml", "e2e = []\n")
	git(remote, "commit", "-q", "-am", "no e2e")
	if out, err := c.Check(ctx, req); err != nil || !out.Skipped {
		t.Fatalf("no e2e commands: %+v, %v", out, err)
	}
}

// While the checks run, their log has what the commands wrote so far, and
// a check record marks them live, with the flow's time limit; both live
// tails read them. The record goes when the checks end.
func TestFlowCheckerLive(t *testing.T) {
	remote, top, sync := t.TempDir(), t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	started, release := filepath.Join(sync, "started"), filepath.Join(sync, "release")
	toml := fmt.Sprintf("e2e = [\"echo first output; touch %s; while [ ! -f %s ]; do sleep 0.05; done\"]\n", started, release)
	if err := os.WriteFile(filepath.Join(remote, ".bottown.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	git(remote, "init", "-q", "-b", "main")
	git(remote, "add", ".")
	git(remote, "commit", "-q", "-m", "init")
	head := git(remote, "rev-parse", "HEAD")
	git(top, "init", "-q")
	ws, err := runs.Open(top, "bottown", remote, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := flowChecker{ws: ws}
	req := flow.CheckRequest{Repo: "demo", Issue: 9, PR: 12, SHA: head}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	type result struct {
		out flow.CheckOutcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := c.Check(ctx, req)
		done <- result{out, err}
	}()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("checks ended early: %+v, %v", r.out, r.err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	if log, _ := os.ReadFile(c.LogPath(req)); !strings.Contains(string(log), "first output") {
		t.Errorf("log while running:\n%s", log)
	}
	live := runs.LiveChecks(ws.RunsDir)
	if len(live) != 1 {
		t.Fatalf("live checks = %+v", live)
	}
	rec := live[0]
	if rec.ID+".log" != filepath.Base(c.LogPath(req)) || rec.Issue != 9 || rec.PR != 12 || rec.SHA != head || rec.Repo != "demo" ||
		len(rec.Commands) != 1 || rec.Expires.IsZero() {
		t.Errorf("record = %+v", rec)
	}

	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.err != nil || !r.out.Passed {
		t.Fatalf("checks: %+v, %v", r.out, r.err)
	}
	if live := runs.LiveChecks(ws.RunsDir); len(live) != 0 {
		t.Errorf("checks still live after they ended: %+v", live)
	}
}
