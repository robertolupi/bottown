package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// bottown check: run the repository's checks (.bottown.toml check and e2e,
// read from the forge's default branch), keep their full output in a log
// next to the run's record, print a short summary, and record the result as
// commit statuses on HEAD through the server.

// Step states.
const (
	stepPassed  = "passed"
	stepFailed  = "failed"
	stepSkipped = "skipped"
)

const (
	maxFailLines = 15 // failing lines quoted per failed step
	maxDepLines  = 10 // added and removed lines quoted per manifest
)

// checkStep is one command of a check group and how it went.
type checkStep struct {
	Group    string        `json:"group"` // check | e2e
	Command  string        `json:"command"`
	Status   string        `json:"status"`
	Exit     int           `json:"exit,omitempty"`
	Duration time.Duration `json:"duration"`
	// FirstLine and LastLine bound the step's output in the log (1-based).
	FirstLine int       `json:"first_line,omitempty"`
	LastLine  int       `json:"last_line,omitempty"`
	Failing   []logLine `json:"failing,omitempty"`
}

// logLine is a line of the log and its number.
type logLine struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

// depChange is a dependency manifest the branch changes.
type depChange struct {
	File    string   `json:"file"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	// NAdded and NRemoved count every changed line; Added and Removed
	// quote the first few.
	NAdded   int `json:"n_added"`
	NRemoved int `json:"n_removed"`
}

// checkResult is one `bottown check`, saved next to its log for --report.
type checkResult struct {
	SHA     string      `json:"sha"`
	Branch  string      `json:"branch,omitempty"`
	Dirty   bool        `json:"dirty,omitempty"`
	Started time.Time   `json:"started"`
	Log     string      `json:"log"`
	Steps   []checkStep `json:"steps"`
	Base    string      `json:"base,omitempty"` // what Deps compares with, e.g. origin/main
	Deps    []depChange `json:"deps,omitempty"`
	Notes   []string    `json:"notes,omitempty"`
}

// failed reports whether any step failed.
func (r checkResult) failed() bool {
	return slices.ContainsFunc(r.Steps, func(s checkStep) bool { return s.Status == stepFailed })
}

// groupState is the commit status for a group: success when every step
// passed, failure when one failed, "" when the group did not run.
func (r checkResult) groupState(group string) string {
	ran, failed := false, false
	for _, s := range r.Steps {
		if s.Group != group || s.Status == stepSkipped {
			continue
		}
		ran = true
		failed = failed || s.Status == stepFailed
	}
	switch {
	case !ran:
		return ""
	case failed:
		return "failure"
	}
	return "success"
}

// groupDescription is the commit status description for a group.
func (r checkResult) groupDescription(group string) string {
	var total time.Duration
	var cmds []string
	for _, s := range r.Steps {
		if s.Group != group || s.Status == stepSkipped {
			continue
		}
		total += s.Duration
		cmds = append(cmds, s.Command)
		if s.Status == stepFailed {
			return fmt.Sprintf("failed: %s (exit %d) after %s", s.Command, s.Exit, fmtDuration(total))
		}
	}
	d := fmt.Sprintf("passed: %s in %s", strings.Join(cmds, "; "), fmtDuration(total))
	if group == "check" && len(r.Deps) > 0 {
		files := make([]string, len(r.Deps))
		for i, dc := range r.Deps {
			files[i] = dc.File
		}
		d += "; dependencies changed: " + strings.Join(files, ", ")
	}
	return d
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// failRe matches lines that say why a check failed: Go test failures,
// compiler and vet positions, panics, make's error line, npm and vitest
// failures, svelte-check errors, gofmt's list.
var failRe = regexp.MustCompile(`^\s*(--- FAIL|FAIL\b|panic:|fatal:|Error:|error:|ERROR\b|npm ERR!|✗|×|gofmt needed)` +
	`|^make(\[\d+\])?: \*\*\*` +
	`|\.(go|ts|js|svelte):\d+(:\d+)?:? ` +
	`|\b[Ee]rror TS\d+`)

// checkLog writes a check's log and keeps, for the step being run, its line
// numbers, its first failing lines and its last lines.
type checkLog struct {
	w       io.Writer
	n       int // lines written
	partial []byte
	matches []logLine
	tail    []logLine
}

func (l *checkLog) line(s string) {
	l.n++
	fmt.Fprintln(l.w, s)
}

// startStep writes a step's header and resets what is kept per step.
func (l *checkLog) startStep(group, command string) {
	l.line(fmt.Sprintf("==> [%s] %s", group, command))
	l.matches, l.tail, l.partial = nil, nil, nil
}

// Write takes the step's output.
func (l *checkLog) Write(p []byte) (int, error) {
	l.partial = append(l.partial, p...)
	for {
		i := slices.Index(l.partial, '\n')
		if i < 0 {
			break
		}
		l.output(string(l.partial[:i]))
		l.partial = l.partial[i+1:]
	}
	return len(p), nil
}

func (l *checkLog) output(s string) {
	s = strings.TrimRight(s, "\r")
	l.line(s)
	ll := logLine{l.n, stripANSI(s)}
	if len(l.matches) < maxFailLines && failRe.MatchString(ll.Text) {
		l.matches = append(l.matches, ll)
	}
	l.tail = append(l.tail, ll)
	if len(l.tail) > maxFailLines {
		l.tail = l.tail[1:]
	}
}

// endStep flushes a partial last line and returns the step's failing lines:
// the first lines that look like failures, else its last lines.
func (l *checkLog) endStep() []logLine {
	if len(l.partial) > 0 {
		l.output(string(l.partial))
		l.partial = nil
	}
	if len(l.matches) > 0 {
		return l.matches
	}
	return l.tail
}

// checkSpec is a step to run.
type checkSpec struct{ Group, Command string }

// runChecks runs specs in order with sh -c in dir, writing all output to
// log. After a failure the remaining steps are skipped. With ownGroup each
// step runs in its own process group, killed whole when ctx ends (the
// flow's time limit), so a suite's children do not outlive it.
func runChecks(ctx context.Context, dir string, specs []checkSpec, log *checkLog, now func() time.Time, ownGroup bool) []checkStep {
	steps := make([]checkStep, 0, len(specs))
	failed := false
	for _, sp := range specs {
		st := checkStep{Group: sp.Group, Command: sp.Command}
		if failed {
			st.Status = stepSkipped
			steps = append(steps, st)
			continue
		}
		log.startStep(sp.Group, sp.Command)
		st.FirstLine = log.n + 1
		cmd := exec.CommandContext(ctx, "sh", "-c", sp.Command)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = log, log
		if ownGroup {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			cmd.WaitDelay = 10 * time.Second
		}
		start := now()
		err := cmd.Run()
		st.Duration = now().Sub(start)
		failing := log.endStep()
		st.LastLine = log.n
		if st.LastLine < st.FirstLine {
			st.FirstLine, st.LastLine = 0, 0
		}
		st.Status = stepPassed
		if err != nil {
			st.Status, st.Exit, st.Failing, failed = stepFailed, -1, failing, true
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() >= 0 {
				st.Exit = ee.ExitCode()
			} else {
				log.line("bottown check: " + err.Error())
			}
		}
		log.line(fmt.Sprintf("<== %s in %s (exit %d)", st.Status, fmtDuration(st.Duration), st.Exit))
		steps = append(steps, st)
	}
	return steps
}

// manifestNames are the dependency manifests checked for changes, by base
// name; lock files follow them and are not listed.
var manifestNames = []string{
	"go.mod", "go.work", "package.json", "Cargo.toml", "pyproject.toml", "Pipfile",
	"setup.py", "setup.cfg", "Gemfile", "composer.json", "pom.xml", "build.gradle", "build.gradle.kts",
}

func isManifest(file string) bool {
	base := path.Base(file)
	if slices.Contains(manifestNames, base) {
		return true
	}
	ok, _ := path.Match("requirements*.txt", base)
	return ok
}

// parseManifestDiff returns the lines a unified diff of one file adds and
// removes, quoting at most maxDepLines of each.
func parseManifestDiff(file, diff string) depChange {
	dc := depChange{File: file, Added: []string{}, Removed: []string{}}
	for _, ln := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
		case strings.HasPrefix(ln, "+"):
			if t := strings.TrimSpace(ln[1:]); t != "" {
				dc.NAdded++
				if len(dc.Added) < maxDepLines {
					dc.Added = append(dc.Added, t)
				}
			}
		case strings.HasPrefix(ln, "-"):
			if t := strings.TrimSpace(ln[1:]); t != "" {
				dc.NRemoved++
				if len(dc.Removed) < maxDepLines {
					dc.Removed = append(dc.Removed, t)
				}
			}
		}
	}
	return dc
}

// depChanges lists the manifests HEAD changes since its merge base with base.
func depChanges(top, base string) ([]depChange, error) {
	mb, err := runs.Git(top, nil, "merge-base", "HEAD", base)
	if err != nil {
		return nil, err
	}
	names, err := runs.Git(top, nil, "diff", "--name-only", "--no-renames", mb, "HEAD")
	if err != nil {
		return nil, err
	}
	var out []depChange
	for _, f := range strings.Split(names, "\n") {
		if f == "" || !isManifest(f) {
			continue
		}
		d, err := runs.Git(top, nil, "diff", "-U0", "--no-color", mb, "HEAD", "--", f)
		if err != nil {
			return nil, err
		}
		out = append(out, parseManifestDiff(f, d))
	}
	return out, nil
}

// checkPaths returns where a check's log and result go: next to a launched
// run's record (<runs>/<id>.check.log), else in the actor worktree's
// bottown directory (check.log, replaced by the next check).
func (a *agentCtx) checkPaths() (logPath, resultPath string, err error) {
	switch {
	case a.Worktree == "" && a.RunFile != "":
		base := strings.TrimSuffix(a.RunFile, ".json")
		return base + ".check.log", base + ".check.json", nil
	case a.KeyDir != "":
		return filepath.Join(a.KeyDir, "check.log"), filepath.Join(a.KeyDir, "check.json"), nil
	}
	return "", "", errors.New("no run or actor worktree to keep the check log in")
}

// checkRemote is the forge remote in the checkout at top: the agent's, or
// origin, which launched runs use.
func checkRemote(top, preferred string) string {
	out, _ := runs.Git(top, nil, "remote")
	remotes := strings.Fields(out)
	if slices.Contains(remotes, preferred) || !slices.Contains(remotes, "origin") {
		return preferred
	}
	return "origin"
}

func newCheck() *cobra.Command {
	var e2e, report, asJSON bool
	cmd := &cobra.Command{
		Use:   "check [--e2e] [--report]",
		Short: "Run the repository's checks, keep the log, and record the result on HEAD",
		Long: `Run the repository's checks in this checkout: the commands .bottown.toml
names in check (default: make check), then, with --e2e, those in e2e
(default: make e2e). bottown reads .bottown.toml from the forge's default
branch, never from the checkout. Each command runs with sh -c at the
checkout's root; after a failure the rest are skipped.

The full output goes to a log next to the run's record
(<runs>/<id>.check.log in a launched run, <gitdir>/bottown/check.log in an
actor worktree); the summary lists each step with its result and duration,
the first failing lines with their line numbers in the log, and the
dependency manifests (go.mod, package.json, ...) that HEAD changes since
the default branch, with the lines they add and remove.

In an implement, plan or verify run, the result is recorded on HEAD as the
commit statuses bottown/check and bottown/e2e, which pr show lists. HEAD
must be committed (a dirty worktree is not recorded) and, in an implement
or plan run, pushed. --report records the last result again, without
rerunning, once HEAD is pushed.

Exits non-zero when a check fails.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			top, err := runs.Git(".", nil, "rev-parse", "--show-toplevel")
			if err != nil {
				return errors.New("not inside a git checkout")
			}
			logPath, resultPath, err := a.checkPaths()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var res checkResult
			if report {
				b, err := os.ReadFile(resultPath)
				if err != nil {
					return fmt.Errorf("no check result to report: %w", err)
				}
				if err := json.Unmarshal(b, &res); err != nil {
					return fmt.Errorf("%s: %w", resultPath, err)
				}
				if head, _ := runs.Git(top, nil, "rev-parse", "HEAD"); head != res.SHA {
					return fmt.Errorf("the last check ran at %s, but HEAD is %s: run bottown check again", short(res.SHA), short(head))
				}
			} else {
				res, err = a.runCheck(cmd.Context(), cmd.ErrOrStderr(), top, logPath, e2e)
				if err != nil {
					return err
				}
				if b, err := json.MarshalIndent(res, "", "  "); err == nil {
					_ = os.WriteFile(resultPath, b, 0o600)
				}
			}
			recorded, recErr := a.recordCheck(cmd.Context(), top, res)
			if asJSON {
				m := map[string]any{"result": res, "recorded": recorded}
				if recErr != nil {
					m["not_recorded"] = recErr.Error()
				}
				if err := printJSON(cmd, m); err != nil {
					return err
				}
			} else {
				printCheck(colorOut(out), res, recorded, recErr)
			}
			switch {
			case res.failed():
				return errors.New("checks failed")
			case report && recErr != nil:
				return recErr
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&e2e, "e2e", false, "also run the e2e commands")
	cmd.Flags().BoolVar(&report, "report", false, "record the last check's result on HEAD again, without rerunning (after a push)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.MarkFlagsMutuallyExclusive("e2e", "report")
	return cmd
}

// runCheck reads the checks from the forge's default branch, runs them in
// top with their output in logPath, and looks for dependency changes.
func (a *agentCtx) runCheck(ctx context.Context, progress io.Writer, top, logPath string, e2e bool) (checkResult, error) {
	res := checkResult{Started: time.Now(), Log: logPath}
	var err error
	if res.SHA, err = runs.Git(top, nil, "rev-parse", "HEAD"); err != nil {
		return res, err
	}
	res.Branch, _ = runs.Git(top, nil, "symbolic-ref", "-q", "--short", "HEAD")
	st, _ := runs.Git(top, nil, "status", "--porcelain")
	res.Dirty = st != ""

	remote := checkRemote(top, a.Remote)
	settings := config.DefaultRepoSettings()
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, ferr := runs.GitCtx(fctx, top, nil, "fetch", "-q", remote)
	cancel()
	if ferr != nil {
		res.Notes = append(res.Notes, "could not fetch "+remote+": comparing with the last fetch")
	}
	if branch, err := runs.RemoteDefaultBranch(ctx, top, remote); err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("the default branch of %s is unknown (%v): running the default checks", remote, err))
	} else {
		res.Base = remote + "/" + branch
		if settings, err = runs.RepoSettingsAt(top, res.Base); err != nil {
			return res, err
		}
		if res.Deps, err = depChanges(top, res.Base); err != nil {
			res.Notes = append(res.Notes, "dependency changes unknown: "+err.Error())
		}
	}
	var specs []checkSpec
	for _, c := range settings.Check {
		specs = append(specs, checkSpec{"check", c})
	}
	if e2e {
		if len(settings.E2E) == 0 {
			res.Notes = append(res.Notes, config.RepoSettingsFile+" names no e2e commands")
		}
		for _, c := range settings.E2E {
			specs = append(specs, checkSpec{"e2e", c})
		}
	}
	if len(specs) == 0 {
		return res, fmt.Errorf("%s names no checks", config.RepoSettingsFile)
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return res, err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	defer bw.Flush()
	l := &checkLog{w: bw}
	l.line(fmt.Sprintf("bottown check at %s in %s, %s", res.SHA, top, res.Started.Format(time.RFC3339)))
	fmt.Fprintf(progress, "bottown check: running %d step(s), log: %s\n", len(specs), logPath)
	res.Steps = runChecks(ctx, top, specs, l, time.Now, false)
	return res, nil
}

// recordCheck records each group that ran as a commit status on HEAD. It
// returns the statuses written, and why it did not write (all of) them.
func (a *agentCtx) recordCheck(ctx context.Context, top string, res checkResult) ([]server.CommitStatus, error) {
	switch {
	case a.RunID == "":
		return nil, errors.New("no active run")
	case res.Dirty:
		return nil, errors.New("the worktree had uncommitted changes, so the checks did not run on HEAD")
	}
	var done []server.CommitStatus
	for _, g := range server.CheckNames {
		state := res.groupState(g)
		if state == "" {
			continue
		}
		var out server.CommitStatus
		if err := a.call(ctx, "POST", "/v1/forge/status", server.CommitStatusIn{SHA: res.SHA, Check: g, State: state, Description: res.groupDescription(g)}, &out); err != nil {
			// The server's refusal ("push HEAD first", …) is the reason
			// the user acts on; the method and path are noise here.
			var apiErr *client.APIError
			if errors.As(err, &apiErr) && apiErr.Message != "" {
				return done, errors.New(apiErr.Message)
			}
			return done, err
		}
		done = append(done, out)
	}
	return done, nil
}

func printCheck(out io.Writer, res checkResult, recorded []server.CommitStatus, recErr error) {
	st := newStyles()
	at := short(res.SHA)
	if res.Branch != "" {
		at += " on " + res.Branch
	}
	if res.Dirty {
		at += " " + st.Warning.Render("(with uncommitted changes)")
	}
	fmt.Fprintf(out, "bottown check at %s\nlog: %s\n", st.Bold.Render(at), res.Log)
	tw := newTableWriter(out)
	for _, s := range res.Steps {
		status := st.Success.Render(s.Status)
		switch s.Status {
		case stepFailed:
			status = st.Danger.Render(s.Status)
		case stepSkipped:
			status = st.Dim.Render(s.Status)
		}
		dur, where := "", ""
		if s.Status != stepSkipped {
			dur = fmtDuration(s.Duration)
		}
		if s.Status == stepFailed {
			where = fmt.Sprintf("exit %d", s.Exit)
			if s.FirstLine > 0 {
				where += fmt.Sprintf(", log lines %d-%d", s.FirstLine, s.LastLine)
			}
		}
		row := []string{"  " + s.Group, st.Bold.Render(s.Command), status}
		if dur != "" {
			row = append(row, dur)
		}
		if where != "" {
			row = append(row, where)
		}
		tw.Row(row...)
	}
	_ = tw.Flush()
	for _, s := range res.Steps {
		if len(s.Failing) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s failed; first failing lines:\n", st.Bold.Render(s.Command))
		for _, ll := range s.Failing {
			fmt.Fprintf(out, "  %s  %s\n", st.Dim.Render(fmt.Sprintf("%6d", ll.N)), ll.Text)
		}
	}
	if len(res.Deps) > 0 {
		fmt.Fprintf(out, "\n%s since %s: check any new third-party packages\n", st.Warning.Render("dependencies changed"), res.Base)
		for _, dc := range res.Deps {
			fmt.Fprintf(out, "  %s  +%d -%d\n", st.Bold.Render(dc.File), dc.NAdded, dc.NRemoved)
			for _, l := range dc.Added {
				fmt.Fprintf(out, "    + %s\n", l)
			}
			if n := dc.NAdded - len(dc.Added); n > 0 {
				fmt.Fprintf(out, "    + … %d more\n", n)
			}
			for _, l := range dc.Removed {
				fmt.Fprintf(out, "    - %s\n", l)
			}
			if n := dc.NRemoved - len(dc.Removed); n > 0 {
				fmt.Fprintf(out, "    - … %d more\n", n)
			}
		}
	}
	for _, n := range res.Notes {
		fmt.Fprintln(out, st.Warning.Render("note: "+n))
	}
	if len(recorded) > 0 {
		parts := make([]string, len(recorded))
		for i, c := range recorded {
			parts[i] = c.Context + " " + c.State
		}
		fmt.Fprintf(out, "recorded on %s: %s\n", short(res.SHA), strings.Join(parts, ", "))
	}
	if recErr != nil {
		msg := "not recorded: " + recErr.Error()
		if strings.Contains(recErr.Error(), "push HEAD first") {
			msg += "; then `bottown check --report` records this result"
		}
		fmt.Fprintln(out, st.Warning.Render(msg))
	}
	if res.failed() {
		fmt.Fprintln(out, st.Danger.Render("FAILED"))
	} else {
		fmt.Fprintln(out, st.Success.Render("PASSED"))
	}
}
