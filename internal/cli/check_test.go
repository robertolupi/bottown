package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/robertolupi/bottown/internal/server"
)

func TestCheckLogKeepsFailingLines(t *testing.T) {
	var buf bytes.Buffer
	l := &checkLog{w: &buf}
	l.line("header")
	l.startStep("check", "make check")
	fmt.Fprint(l, "ok  \tpkg/a\t0.1s\n--- FAIL: TestX (0.00s)\n    x_test.go:12: got 1, want 2\r\n")
	fmt.Fprint(l, "FAIL\tpkg/b\nmake: *** [test] Error 1") // no final newline
	got := l.endStep()
	want := []logLine{{4, "--- FAIL: TestX (0.00s)"}, {5, "    x_test.go:12: got 1, want 2"}, {6, "FAIL\tpkg/b"}, {7, "make: *** [test] Error 1"}}
	if !slices.Equal(got, want) {
		t.Errorf("failing lines = %+v, want %+v", got, want)
	}
	if l.n != 7 || !strings.HasSuffix(buf.String(), "==> [check] make check\nok  \tpkg/a\t0.1s\n--- FAIL: TestX (0.00s)\n    x_test.go:12: got 1, want 2\nFAIL\tpkg/b\nmake: *** [test] Error 1\n") {
		t.Errorf("log (%d lines):\n%s", l.n, buf.String())
	}

	// Without a line that looks like a failure, the step's last lines.
	l.startStep("e2e", "x")
	for i := range maxFailLines + 5 {
		fmt.Fprintf(l, "line %d\n", i)
	}
	got = l.endStep()
	if len(got) != maxFailLines || got[0].Text != "line 5" || got[len(got)-1].Text != fmt.Sprintf("line %d", maxFailLines+4) || got[len(got)-1].N != l.n {
		t.Errorf("tail = %+v", got)
	}

	// Colours are matched and quoted without their escapes; the log keeps them.
	l.startStep("check", "y")
	fmt.Fprint(l, "\x1b[31m×\x1b[0m test fails\n")
	if got := l.endStep(); len(got) != 1 || got[0].Text != "× test fails" || !strings.Contains(buf.String(), "\x1b[31m") {
		t.Errorf("coloured failure = %+v", got)
	}
}

func TestRunChecks(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	l := &checkLog{w: &buf}
	tick := time.Unix(0, 0)
	now := func() time.Time { tick = tick.Add(1500 * time.Millisecond); return tick }
	steps := runChecks(context.Background(), dir, []checkSpec{
		{"check", "echo hello; echo to-stderr >&2; touch made"},
		{"check", "echo 'main.go:3:1: undefined: x'; exit 3"},
		{"e2e", "echo never"},
	}, l, now)
	if len(steps) != 3 {
		t.Fatalf("steps = %+v", steps)
	}
	if s := steps[0]; s.Status != stepPassed || s.Exit != 0 || s.Duration != 1500*time.Millisecond || s.FirstLine != 2 || s.LastLine != 3 || s.Failing != nil {
		t.Errorf("passing step = %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "made")); err != nil {
		t.Errorf("the step did not run in dir: %v", err)
	}
	if s := steps[1]; s.Status != stepFailed || s.Exit != 3 || len(s.Failing) != 1 || s.Failing[0].Text != "main.go:3:1: undefined: x" || s.Failing[0].N != s.FirstLine {
		t.Errorf("failing step = %+v", s)
	}
	if s := steps[2]; s.Status != stepSkipped || s.FirstLine != 0 {
		t.Errorf("step after a failure = %+v", s)
	}
	log := buf.String()
	for _, want := range []string{"==> [check] echo hello;", "hello\n", "to-stderr\n", "<== passed in 1.5s (exit 0)", "<== failed in 1.5s (exit 3)"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "never") {
		t.Errorf("skipped step ran:\n%s", log)
	}
	if lines := strings.Split(log, "\n"); lines[steps[1].Failing[0].N-1] != "main.go:3:1: undefined: x" {
		t.Errorf("failing line number %d does not point into the log:\n%s", steps[1].Failing[0].N, log)
	}

	res := checkResult{Steps: steps}
	if !res.failed() || res.groupState("check") != "failure" || res.groupState("e2e") != "" {
		t.Errorf("states: failed %v, check %q, e2e %q", res.failed(), res.groupState("check"), res.groupState("e2e"))
	}
	if d := res.groupDescription("check"); d != "failed: echo 'main.go:3:1: undefined: x'; exit 3 (exit 3) after 3.0s" {
		t.Errorf("failure description = %q", d)
	}
}

func TestCheckResultStates(t *testing.T) {
	res := checkResult{
		Steps: []checkStep{
			{Group: "check", Command: "make vet", Status: stepPassed, Duration: 50 * time.Second},
			{Group: "check", Command: "make test", Status: stepPassed, Duration: 75 * time.Second},
			{Group: "e2e", Command: "make e2e", Status: stepPassed, Duration: 2 * time.Hour},
		},
		Deps: []depChange{{File: "go.mod"}, {File: "web/package.json"}},
	}
	if res.failed() || res.groupState("check") != "success" || res.groupState("e2e") != "success" {
		t.Errorf("passing result: %+v", res)
	}
	if d := res.groupDescription("check"); d != "passed: make vet; make test in 2m05s; dependencies changed: go.mod, web/package.json" {
		t.Errorf("check description = %q", d)
	}
	if d := res.groupDescription("e2e"); d != "passed: make e2e in 2h00m" {
		t.Errorf("e2e description = %q", d)
	}
}

func TestManifestDiff(t *testing.T) {
	for f, want := range map[string]bool{
		"go.mod": true, "web/package.json": true, "a/b/Cargo.toml": true, "requirements-dev.txt": true,
		"go.sum": false, "web/package-lock.json": false, "docs/go.mod.md": false, "package.json.bak": false,
	} {
		if got := isManifest(f); got != want {
			t.Errorf("isManifest(%q) = %v", f, got)
		}
	}
	diff := "diff --git a/go.mod b/go.mod\nindex 1..2 100644\n--- a/go.mod\n+++ b/go.mod\n@@ -3,1 +3,2 @@\n-\tgithub.com/old/x v1.0.0\n+\tgithub.com/new/y v1.2.3\n+\n+\tgithub.com/new/z v0.1.0 // indirect"
	dc := parseManifestDiff("go.mod", diff)
	if dc.File != "go.mod" || dc.NAdded != 2 || dc.NRemoved != 1 ||
		!slices.Equal(dc.Added, []string{"github.com/new/y v1.2.3", "github.com/new/z v0.1.0 // indirect"}) ||
		!slices.Equal(dc.Removed, []string{"github.com/old/x v1.0.0"}) {
		t.Errorf("manifest diff = %+v", dc)
	}
	var many strings.Builder
	for i := range maxDepLines + 3 {
		fmt.Fprintf(&many, "+\"dep%d\": \"1\",\n", i)
	}
	if dc := parseManifestDiff("package.json", many.String()); dc.NAdded != maxDepLines+3 || len(dc.Added) != maxDepLines {
		t.Errorf("long diff = %d quoted of %d", len(dc.Added), dc.NAdded)
	}
}

func TestDepChanges(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644)
	}
	run("init", "-q", "-b", "main")
	write("go.mod", "module x\n\nrequire a.example/a v1.0.0\n")
	write("web/package.json", "{}\n")
	run("add", ".")
	run("commit", "-q", "-m", "init")
	run("switch", "-q", "-c", "topic")
	write("go.mod", "module x\n\nrequire (\n\ta.example/a v1.0.0\n\tb.example/b v2.0.0\n)\n")
	write("go.sum", "b.example/b v2.0.0 h1:x\n")
	write("README", "docs\n")
	run("add", ".")
	run("commit", "-q", "-m", "add b")
	got, err := depChanges(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].File != "go.mod" || !slices.Contains(got[0].Added, "b.example/b v2.0.0") || !slices.Contains(got[0].Removed, "require a.example/a v1.0.0") {
		t.Errorf("dep changes = %+v", got)
	}
	// Changes on main since the branch point are not the branch's.
	run("switch", "-q", "main")
	write("web/package.json", "{\"dependencies\": {\"left-pad\": \"1\"}}\n")
	run("commit", "-q", "-am", "main dep")
	run("switch", "-q", "topic")
	if got, err := depChanges(dir, "main"); err != nil || len(got) != 1 || got[0].File != "go.mod" {
		t.Errorf("dep changes after main moved = %+v, %v", got, err)
	}
}

func TestCheckPaths(t *testing.T) {
	launched := &agentCtx{RunID: "r-1", RunFile: "/w/.bottown/runs/r-1.json"}
	if l, r, err := launched.checkPaths(); err != nil || l != "/w/.bottown/runs/r-1.check.log" || r != "/w/.bottown/runs/r-1.check.json" {
		t.Errorf("launched run: %s %s %v", l, r, err)
	}
	actor := &agentCtx{RunID: "r-2", Worktree: "/fam/claude", KeyDir: "/fam/main/.git/worktrees/claude/bottown", RunFile: "/fam/main/.git/worktrees/claude/bottown/run.json"}
	if l, _, err := actor.checkPaths(); err != nil || l != "/fam/main/.git/worktrees/claude/bottown/check.log" {
		t.Errorf("actor worktree: %s %v", l, err)
	}
	if _, _, err := (&agentCtx{}).checkPaths(); err == nil {
		t.Error("no run and no worktree: no error")
	}
}

func TestPrintCheck(t *testing.T) {
	r := lipgloss.NewRenderer(&bytes.Buffer{})
	r.SetColorProfile(termenv.Ascii)
	res := checkResult{
		SHA: "0123456789abcdef0123456789abcdef01234567", Branch: "issue/7", Log: "/runs/r-1.check.log", Base: "origin/main",
		Steps: []checkStep{
			{Group: "check", Command: "make check", Status: stepPassed, Duration: 62 * time.Second},
			{Group: "e2e", Command: "make e2e", Status: stepFailed, Exit: 2, Duration: 5 * time.Second, FirstLine: 10, LastLine: 40,
				Failing: []logLine{{31, "--- FAIL: TestE2E/x (1.00s)"}}},
			{Group: "e2e", Command: "make smoke", Status: stepSkipped},
		},
		Deps: []depChange{{File: "go.mod", Added: []string{"b.example/b v2.0.0"}, NAdded: 3, Removed: []string{}}},
	}
	var out bytes.Buffer
	printCheck(&out, r, res, []server.CommitStatus{{Context: "bottown/check", State: "success"}, {Context: "bottown/e2e", State: "failure"}}, nil)
	got := out.String()
	for _, want := range []string{
		"bottown check at 0123456789 on issue/7\nlog: /runs/r-1.check.log\n",
		"  check  make check  passed   1m02s",
		"  e2e    make e2e    failed   5.0s   exit 2, log lines 10-40",
		"  e2e    make smoke  skipped",
		"make e2e failed; first failing lines:\n      31  --- FAIL: TestE2E/x (1.00s)\n",
		"dependencies changed since origin/main: check any new third-party packages\n  go.mod  +3 -0\n    + b.example/b v2.0.0\n    + … 2 more\n",
		"recorded on 0123456789: bottown/check success, bottown/e2e failure\n",
		"FAILED\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}

	out.Reset()
	res.Steps = res.Steps[:1]
	printCheck(&out, r, res, nil, errors.New("issue/7 on the forge is at abc, not 012: push HEAD first"))
	if got := out.String(); !strings.Contains(got, "not recorded: issue/7 on the forge is at abc, not 012: push HEAD first; then `bottown check --report` records this result") ||
		!strings.HasSuffix(got, "PASSED\n") {
		t.Errorf("unpushed summary:\n%s", got)
	}
}
