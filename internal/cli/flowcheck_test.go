package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	var steps []string
	c.progress = func(r flow.CheckRequest, step string) { steps = append(steps, fmt.Sprintf("!%d %s", r.PR, step)) }
	out, err = c.Check(ctx, req)
	if err != nil || !out.Passed || !strings.HasPrefix(out.Description, "passed: test -f fixed; echo e2e ok in ") {
		t.Fatalf("fixed head: %+v, %v", out, err)
	}
	// Each group says what it runs and where its log is as it starts.
	if want := []string{"!12 e2e: test -f fixed; echo e2e ok (log: " + out.Log + ")"}; !slices.Equal(steps, want) {
		t.Errorf("progress = %q, want %q", steps, want)
	}
	c.progress = nil
	if log, _ := os.ReadFile(out.Log); !strings.Contains(string(log), "e2e ok") {
		t.Errorf("fixed log:\n%s", log)
	}
	if wts := git(ws.Mirror, "worktree", "list"); strings.Contains(wts, "flow-9-") {
		t.Errorf("check worktrees left behind:\n%s", wts)
	}

	// A check that cannot mark itself live does not start: without the
	// record, another check's cleanup would remove its worktree.
	failing := flowChecker{ws: ws, hold: func(string, runs.CheckRecord) (func(), error) {
		return nil, fmt.Errorf("no record")
	}}
	before, _ := os.ReadDir(ws.RunsDir)
	if out, err := failing.Check(ctx, req); err == nil || !strings.Contains(err.Error(), "no record") || out.Passed {
		t.Fatalf("check without a record: %+v, %v", out, err)
	}
	if after, _ := os.ReadDir(ws.RunsDir); len(after) != len(before) {
		t.Errorf("check without a record left files: %v, before %v", after, before)
	}
	if wts := git(ws.Mirror, "worktree", "list"); strings.Contains(wts, "flow-9-") {
		t.Errorf("check without a record made a worktree:\n%s", wts)
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

type gateRecord struct{ sha, check, state, description string }

type gateRecords []gateRecord

func (r *gateRecords) RecordGate(_ context.Context, _, sha, check, state, description string) error {
	*r = append(*r, gateRecord{sha, check, state, description})
	return nil
}

// A request for both groups runs check and then e2e in one worktree, each
// to its own log, and each is recorded as its own status; a check failure
// stops before e2e; a group with no commands is skipped and says so; the
// e2e group alone, the flow's request, does not run the check commands.
func TestFlowCheckerGroups(t *testing.T) {
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
	// Each group leaves a marker in the worktree: e2e sees check's, so both
	// ran in the same one.
	write(".bottown.toml", "check = [\"echo check ok\", \"test -f fixed\", \"touch checked\"]\ne2e = [\"test -f checked\", \"echo e2e ok\"]\n")
	git(remote, "add", ".")
	git(remote, "commit", "-q", "-m", "init")
	broken := git(remote, "rev-parse", "HEAD")
	write("fixed", "\n")
	git(remote, "add", ".")
	git(remote, "commit", "-q", "-m", "fix")
	fixed := git(remote, "rev-parse", "HEAD")
	git(top, "init", "-q")
	ws, err := runs.Open(top, "bottown", remote, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := flowChecker{ws: ws}
	ctx := context.Background()
	both := []string{flow.GroupCheck, flow.GroupE2E}

	req := flow.CheckRequest{Repo: "demo", Issue: 9, PR: 12, SHA: fixed, Groups: both}
	out, err := c.Check(ctx, req)
	if err != nil || !out.Passed || out.Skipped || strings.Join(out.Ran(), ",") != "check,e2e" || out.Failed() != "" {
		t.Fatalf("both groups: %+v, %v", out, err)
	}
	chk, e2e := out.Groups[0], out.Groups[1]
	if !strings.HasSuffix(chk.Log, ".check.log") || !strings.HasSuffix(e2e.Log, ".e2e.log") ||
		strings.TrimSuffix(chk.Log, ".check.log") != strings.TrimSuffix(e2e.Log, ".e2e.log") || out.Log != e2e.Log || c.LogPath(req) != e2e.Log {
		t.Errorf("logs: check %s, e2e %s, outcome %s, LogPath %s", chk.Log, e2e.Log, out.Log, c.LogPath(req))
	}
	if log, _ := os.ReadFile(chk.Log); !strings.Contains(string(log), "==> [check] echo check ok") || strings.Contains(string(log), "e2e ok") {
		t.Errorf("check log:\n%s", log)
	}
	if log, _ := os.ReadFile(e2e.Log); !strings.Contains(string(log), "==> [e2e] test -f checked") || !strings.Contains(string(log), "e2e ok") {
		t.Errorf("e2e log:\n%s", log)
	}
	if !strings.HasPrefix(out.Description, "check: passed: echo check ok; test -f fixed; touch checked in ") ||
		!strings.Contains(out.Description, "; e2e: passed: test -f checked; echo e2e ok in ") {
		t.Errorf("description %q", out.Description)
	}
	var rec gateRecords
	if err := flow.RecordChecks(ctx, &rec, "demo", fixed, out); err != nil {
		t.Fatal(err)
	}
	if len(rec) != 2 || rec[0].check != "check" || rec[0].state != "success" || rec[0].description != chk.Description ||
		rec[1].check != "e2e" || rec[1].state != "success" || rec[1].sha != fixed {
		t.Errorf("recorded %+v", rec)
	}

	// The check group fails at the broken head: e2e does not run.
	req.SHA = broken
	out, err = c.Check(ctx, req)
	if err != nil || out.Passed || strings.Join(out.Ran(), ",") != "check" || out.Failed() != "check" || len(out.Groups) != 1 {
		t.Fatalf("broken head: %+v, %v", out, err)
	}
	if out.Log != out.Groups[0].Log || !strings.Contains(out.Summary, "test -f fixed failed (exit 1)") || !strings.HasPrefix(out.Description, "check: failed: test -f fixed (exit 1)") {
		t.Errorf("broken head: log %s, description %q, summary %q", out.Log, out.Description, out.Summary)
	}
	if logs, _ := filepath.Glob(filepath.Join(ws.RunsDir, "flow-9-"+short(broken)+"-*.e2e.log")); len(logs) != 0 {
		t.Errorf("e2e ran after the check group failed: %v", logs)
	}
	rec = nil
	if err := flow.RecordChecks(ctx, &rec, "demo", broken, out); err != nil || len(rec) != 1 || rec[0].check != "check" || rec[0].state != "failure" {
		t.Errorf("recorded %+v, %v", rec, err)
	}

	// The flow's request, e2e alone, runs no check command: at the broken
	// head the e2e commands fail on the missing marker, not on test -f fixed.
	out, err = c.Check(ctx, flow.CheckRequest{Repo: "demo", Issue: 9, PR: 12, SHA: broken, Groups: []string{flow.GroupE2E}})
	if err != nil || out.Passed || strings.Join(out.Ran(), ",") != "e2e" || !strings.HasPrefix(out.Description, "failed: test -f checked (exit 1)") {
		t.Fatalf("e2e alone: %+v, %v", out, err)
	}

	// check = [] on the default branch: the check group is skipped and says
	// so, and e2e runs.
	write(".bottown.toml", "check = []\ne2e = [\"echo e2e ok\"]\n")
	git(remote, "commit", "-q", "-am", "no check")
	req.SHA = fixed
	out, err = c.Check(ctx, req)
	if err != nil || !out.Passed || out.Skipped || strings.Join(out.Ran(), ",") != "e2e" || len(out.Groups) != 2 || !out.Groups[0].Skipped {
		t.Fatalf("no check commands: %+v, %v", out, err)
	}
	if !strings.HasPrefix(out.Description, "check: the repository names no check commands; e2e: passed: echo e2e ok") {
		t.Errorf("no check commands: description %q", out.Description)
	}
	rec = nil
	if err := flow.RecordChecks(ctx, &rec, "demo", fixed, out); err != nil || len(rec) != 1 || rec[0].check != "e2e" {
		t.Errorf("recorded %+v, %v", rec, err)
	}

	// Neither group has commands: the whole check is skipped.
	write(".bottown.toml", "check = []\ne2e = []\n")
	git(remote, "commit", "-q", "-am", "no checks")
	out, err = c.Check(ctx, req)
	if err != nil || !out.Skipped || len(out.Groups) != 2 || out.Description != "check: the repository names no check commands; e2e: the repository names no e2e commands" {
		t.Fatalf("no commands: %+v, %v", out, err)
	}
	if wts := git(ws.Mirror, "worktree", "list"); strings.Contains(wts, "flow-9-") {
		t.Errorf("check worktrees left behind:\n%s", wts)
	}
}

// Two checks at the same head (overlapping flows) get a worktree and a log
// each: the one that ends first removes only its own worktree, and the
// other one's commands still run in theirs. A check's cleanup removes the
// worktrees whose check is no longer live, and keeps the live ones.
func TestFlowCheckerOverlap(t *testing.T) {
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
	// Each check names its files in sync after its worktree, writes
	// started-<dir> and waits for release-<dir>; then a second command must
	// still find the worktree.
	toml := fmt.Sprintf("e2e = ['n=$(basename \"$PWD\"); echo \"check $n\"; touch %[1]s/started-$n; while [ ! -f %[1]s/release-$n ]; do sleep 0.05; done', 'test -f .bottown.toml']\n", sync)
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
	// What an interrupted check left: no record, so no longer live.
	leftover := filepath.Join(ws.RunsDir, "flow-9-"+head[:10]+"-deadbeef")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
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
	start := func(seen map[string]bool) (string, chan result) {
		t.Helper()
		done := make(chan result, 1)
		go func() {
			out, err := c.Check(ctx, req)
			done <- result{out, err}
		}()
		for {
			names, _ := filepath.Glob(filepath.Join(sync, "started-*"))
			for _, n := range names {
				if name := strings.TrimPrefix(filepath.Base(n), "started-"); !seen[name] {
					seen[name] = true
					return name, done
				}
			}
			select {
			case r := <-done:
				t.Fatalf("check ended early: %+v, %v", r.out, r.err)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	seen := map[string]bool{}
	first, firstDone := start(seen)
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("leftover worktree kept: %v", err)
	}
	second, secondDone := start(seen)
	if first == second || !strings.HasPrefix(first, "flow-9-"+head[:10]+"-") || !strings.HasPrefix(second, "flow-9-"+head[:10]+"-") {
		t.Fatalf("check worktrees %q and %q", first, second)
	}
	// The second check's cleanup kept the first one's live worktree.
	if _, err := os.Stat(filepath.Join(ws.RunsDir, first)); err != nil {
		t.Fatalf("first check's worktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(sync, "release-"+first), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r1 := <-firstDone
	if r1.err != nil || !r1.out.Passed || filepath.Base(r1.out.Log) != first+".e2e.log" {
		t.Fatalf("first check: %+v, %v", r1.out, r1.err)
	}
	if _, err := os.Stat(filepath.Join(ws.RunsDir, second)); err != nil {
		t.Fatalf("the first check removed the second one's worktree: %v", err)
	}
	// While the second runs, a resumed flow cites the finished check's log.
	if got := c.LogPath(req); got != r1.out.Log {
		t.Errorf("LogPath while the second runs = %q, want %q", got, r1.out.Log)
	}
	if err := os.WriteFile(filepath.Join(sync, "release-"+second), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := <-secondDone
	if r2.err != nil || !r2.out.Passed || r2.out.Log == r1.out.Log || filepath.Base(r2.out.Log) != second+".e2e.log" {
		t.Fatalf("second check: %+v, %v", r2.out, r2.err)
	}
	for _, r := range []struct{ log, name string }{{r1.out.Log, first}, {r2.out.Log, second}} {
		if log, _ := os.ReadFile(r.log); !strings.Contains(string(log), "check "+r.name) {
			t.Errorf("log %s:\n%s", r.log, log)
		}
	}
	if wts := git(ws.Mirror, "worktree", "list"); strings.Contains(wts, "flow-9-") {
		t.Errorf("check worktrees left behind:\n%s", wts)
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
	if rec.ID+".log" != filepath.Base(c.LogPath(req)) || rec.Group != "e2e" || rec.Issue != 9 || rec.PR != 12 || rec.SHA != head || rec.Repo != "demo" ||
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
