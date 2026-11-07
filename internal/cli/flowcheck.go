package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/runs"
)

// flowChecker runs the post-implement checks for bottown flow: the e2e
// commands .bottown.toml names on the default branch (default: make e2e),
// in a clean worktree detached at the PR head, with no agent involved. Each
// check has a worktree and a log of its own, named for the issue, the SHA
// and a random check id, so that two checks at the same head (overlapping
// flows) cannot remove or overwrite each other's:
// <runs>/flow-<issue>-<sha>-<check>, and next to it its log,
// flow-<issue>-<sha>-<check>.e2e.log, written as the commands write it. While they run, a runs.CheckRecord marks the check
// live, for the web UI's Live Runs and the leftover cleanup, and tail, if
// set (an attended flow on a terminal), follows the log as it does a run's.
type flowChecker struct {
	ws   *runs.Workspace
	tail *liveTail
	// hold marks a check live; nil is runs.HoldCheck. Tests replace it.
	hold func(dir string, rec runs.CheckRecord) (release func(), err error)
}

// prefix starts the names of req's checks: flow-<issue>-<sha>.
func (c flowChecker) prefix(req flow.CheckRequest) string {
	return fmt.Sprintf("flow-%d-%s", req.Issue, short(req.SHA))
}

// checkDir matches a check worktree's name: flow-<issue>-<sha>-<check>, or,
// before checks had ids, flow-<issue>-<sha>. Its record's id adds .e2e,
// and its log's name .e2e.log.
var checkDir = regexp.MustCompile(`^flow-[0-9]+-[0-9a-f]+(?:-[0-9a-f]+)?$`)

// LogPath is req's log for a resumed flow, which knows the head's recorded
// result but not which check recorded it: of the logs of the checks at
// req's head, the last written whose check is no longer live, else the
// last written. With none, it is the name checks had before check ids.
func (c flowChecker) LogPath(req flow.CheckRequest) string {
	prefix := c.prefix(req)
	paths, _ := filepath.Glob(filepath.Join(c.ws.RunsDir, prefix+"-*.e2e.log"))
	live := make(map[string]bool)
	for _, rec := range runs.LiveChecks(c.ws.RunsDir) {
		live[rec.ID+".log"] = true
	}
	var best string
	var bestTime time.Time
	bestLive := true
	for _, p := range paths {
		if !checkDir.MatchString(strings.TrimSuffix(filepath.Base(p), ".e2e.log")) {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		isLive := live[filepath.Base(p)]
		if best == "" || (bestLive && !isLive) || (bestLive == isLive && fi.ModTime().After(bestTime)) {
			best, bestTime, bestLive = p, fi.ModTime(), isLive
		}
	}
	if best == "" {
		return filepath.Join(c.ws.RunsDir, prefix+".e2e.log")
	}
	return best
}

func (c flowChecker) Check(ctx context.Context, req flow.CheckRequest) (flow.CheckOutcome, error) {
	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return flow.CheckOutcome{}, err
	}
	// The check worktree is not a run's: it is named for the issue, SHA and
	// check id, not a run id, so it does not go through Workspace.WorktreeDir.
	name := c.prefix(req) + "-" + hex.EncodeToString(id[:])
	dir := filepath.Join(c.ws.RunsDir, name)
	out := flow.CheckOutcome{Log: dir + ".e2e.log"}
	if err := c.ws.Sync(ctx); err != nil {
		return out, err
	}
	base, err := c.ws.DefaultBranch(ctx)
	if err != nil {
		return out, err
	}
	settings, err := runs.RepoSettingsAt(c.ws.Mirror, c.ws.Remote+"/"+base)
	if err != nil {
		return out, err
	}
	if len(settings.E2E) == 0 {
		out.Skipped = true
		return out, nil
	}
	if _, err := runs.Git(c.ws.Mirror, nil, "cat-file", "-e", req.SHA+"^{commit}"); err != nil {
		return out, fmt.Errorf("the PR head %s is not in the mirror: %w", short(req.SHA), err)
	}
	// The record marks the check live before its worktree exists, so another
	// check's leftover cleanup never takes the worktree for an interrupted
	// check's.
	started := time.Now()
	rec := runs.CheckRecord{ID: name + ".e2e", Repo: req.Repo, Issue: req.Issue, PR: req.PR, SHA: req.SHA, Commands: settings.E2E, Started: started}
	var limit time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		rec.Expires, limit = deadline, deadline.Sub(started)
	}
	// Without it, the cleanup of a check that starts meanwhile would remove
	// this one's worktree while its commands run: the check does not start.
	hold := c.hold
	if hold == nil {
		hold = runs.HoldCheck
	}
	release, err := hold(c.ws.RunsDir, rec)
	if err != nil {
		return out, fmt.Errorf("marking the check live: %w", err)
	}
	defer release()
	c.removeLeftovers()
	if _, err := runs.Git(c.ws.Mirror, nil, "worktree", "add", "-q", "--detach", dir, req.SHA); err != nil {
		return out, err
	}
	defer func() {
		if _, err := runs.Git(c.ws.Mirror, nil, "worktree", "remove", "--force", dir); err != nil {
			_ = os.RemoveAll(dir)
			_, _ = runs.Git(c.ws.Mirror, nil, "worktree", "prune")
		}
	}()

	f, err := os.OpenFile(out.Log, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return out, err
	}
	defer f.Close()
	// Unbuffered: the live tails read the log while the commands run.
	l := &checkLog{w: f}
	l.line(fmt.Sprintf("bottown flow: post-implement checks for #%d (!%d) at %s, %s", req.Issue, req.PR, req.SHA, started.Format(time.RFC3339)))
	if c.tail != nil {
		ended := c.tail.Follow(tailRun{
			ID: rec.ID, Target: fmt.Sprintf("!%d at %s", req.PR, short(req.SHA)),
			Check: strings.Join(settings.E2E, "; "), Started: started, Limit: limit,
		}, "", out.Log)
		defer ended()
	}
	specs := make([]checkSpec, len(settings.E2E))
	for i, cmd := range settings.E2E {
		specs[i] = checkSpec{"e2e", cmd}
	}
	res := checkResult{SHA: req.SHA, Log: out.Log, Steps: runChecks(ctx, dir, specs, l, time.Now, true)}
	out.Passed = !res.failed()
	out.Description = res.groupDescription("e2e")
	out.Summary = checkSummary(res)
	return out, ctx.Err()
}

// removeLeftovers removes the check worktrees in the runs directory whose
// check is no longer live (its record is gone, or nobody holds it): what
// interrupted checks left behind. It lists the worktrees before the live
// checks, and a check is live before its worktree exists, so a worktree it
// removes is never a running check's.
func (c flowChecker) removeLeftovers() {
	entries, err := os.ReadDir(c.ws.RunsDir)
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && checkDir.MatchString(e.Name()) {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		return
	}
	live := make(map[string]bool)
	for _, rec := range runs.LiveChecks(c.ws.RunsDir) {
		live[strings.TrimSuffix(rec.ID, ".e2e")] = true
	}
	for _, d := range dirs {
		if !live[d] {
			_ = os.RemoveAll(filepath.Join(c.ws.RunsDir, d))
		}
	}
	_, _ = runs.Git(c.ws.Mirror, nil, "worktree", "prune")
}

// checkSummary is the failed steps and their first failing lines, with
// their line numbers in the log.
func checkSummary(res checkResult) string {
	var b strings.Builder
	for _, s := range res.Steps {
		if s.Status != stepFailed {
			continue
		}
		fmt.Fprintf(&b, "%s failed (exit %d)", s.Command, s.Exit)
		if s.FirstLine > 0 {
			fmt.Fprintf(&b, ", log lines %d-%d", s.FirstLine, s.LastLine)
		}
		b.WriteString("; first failing lines:\n")
		for _, ll := range s.Failing {
			fmt.Fprintf(&b, "%6d  %s\n", ll.N, ll.Text)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
