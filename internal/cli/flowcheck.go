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

// defaultCheckTimeout limits the e2e checks of bottown flow at each head,
// and of bottown milestone finish.
const defaultCheckTimeout = time.Hour

// flowChecker runs the check groups a request names (the flow's
// post-implement checks are the e2e group alone): the check and e2e
// commands .bottown.toml names on the default branch (default: make check
// and make e2e), one group after the other in one clean worktree detached
// at the PR head, with no agent involved, stopping at the first group that
// fails. A group the repository names no commands for is skipped. Each
// check has a worktree and a log per group of its own, named for the
// issue, the SHA and a random check id, so that two checks at the same
// head (overlapping flows) cannot remove or overwrite each other's:
// <runs>/flow-<issue>-<sha>-<check>, and next to it its logs,
// flow-<issue>-<sha>-<check>.check.log and .e2e.log, written as the
// commands write them. While a group runs, a runs.CheckRecord marks the
// check live, for the web UI's Live Runs and the leftover cleanup, and
// tail, if set (an attended flow on a terminal), follows the group's log
// as it does a run's.
type flowChecker struct {
	ws   *runs.Workspace
	tail *liveTail
	// progress, when set, is told as each group's commands start, with
	// the group, the commands and the log they write.
	progress func(req flow.CheckRequest, step string)
	// hold marks a check live; nil is runs.HoldCheck. Tests replace it.
	hold func(dir string, rec runs.CheckRecord) (release func(), err error)
}

// prefix starts the names of req's checks: flow-<issue>-<sha>.
func (c flowChecker) prefix(req flow.CheckRequest) string {
	return fmt.Sprintf("flow-%d-%s", req.Issue, short(req.SHA))
}

// checkDir matches a check worktree's name: flow-<issue>-<sha>-<check>, or,
// before checks had ids, flow-<issue>-<sha>. Its records' ids add
// .<group> (.check, .e2e), and its logs' names .<group>.log.
var checkDir = regexp.MustCompile(`^flow-[0-9]+-[0-9a-f]+(?:-[0-9a-f]+)?$`)

// LogPath is req's log for a resumed flow, which knows the head's recorded
// result but not which check recorded it: of the logs of the last of req's
// groups for the checks at req's head, the last written whose check is no
// longer live, else the last written. With none, it is the name checks had
// before check ids.
func (c flowChecker) LogPath(req flow.CheckRequest) string {
	groups := req.RunGroups()
	suffix := "." + groups[len(groups)-1] + ".log"
	prefix := c.prefix(req)
	paths, _ := filepath.Glob(filepath.Join(c.ws.RunsDir, prefix+"-*"+suffix))
	live := make(map[string]bool)
	for _, rec := range runs.LiveChecks(c.ws.RunsDir) {
		live[rec.ID+".log"] = true
	}
	var best string
	var bestTime time.Time
	bestLive := true
	for _, p := range paths {
		if !checkDir.MatchString(strings.TrimSuffix(filepath.Base(p), suffix)) {
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
		return filepath.Join(c.ws.RunsDir, prefix+suffix)
	}
	return best
}

func (c flowChecker) Check(ctx context.Context, req flow.CheckRequest) (flow.CheckOutcome, error) {
	groups := req.RunGroups()
	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return flow.CheckOutcome{}, err
	}
	// The check worktree is not a run's: it is named for the issue, SHA and
	// check id, not a run id, so it does not go through Workspace.WorktreeDir.
	name := c.prefix(req) + "-" + hex.EncodeToString(id[:])
	dir := filepath.Join(c.ws.RunsDir, name)
	out := flow.CheckOutcome{Log: dir + "." + groups[len(groups)-1] + ".log"}
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
	commands := map[string][]string{flow.GroupCheck: settings.Check, flow.GroupE2E: settings.E2E}
	first := "" // the first group with commands
	for _, g := range groups {
		cmds, ok := commands[g]
		if !ok {
			return out, fmt.Errorf("unknown check group %q", g)
		}
		if first == "" && len(cmds) > 0 {
			first = g
		}
	}
	if first == "" {
		out.Skipped = true
		for _, g := range groups {
			out.Groups = append(out.Groups, skippedGroup(g))
		}
		out.Description = outcomeDescription(len(groups), out.Groups)
		return out, nil
	}
	out.Log = dir + "." + first + ".log"
	if _, err := runs.Git(c.ws.Mirror, nil, "cat-file", "-e", req.SHA+"^{commit}"); err != nil {
		return out, fmt.Errorf("the PR head %s is not in the mirror: %w", short(req.SHA), err)
	}
	// A group's record marks the check live while the group runs; the next
	// group's is held before the last one's goes, so the check stays live
	// throughout. The first record marks the check live before its worktree
	// exists, so another check's leftover cleanup never takes the worktree
	// for an interrupted check's. Without a record, the cleanup of a check
	// that starts meanwhile would remove this one's worktree while its
	// commands run: the check does not start.
	hold := c.hold
	if hold == nil {
		hold = runs.HoldCheck
	}
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	mark := func(rec runs.CheckRecord) error {
		r, err := hold(c.ws.RunsDir, rec)
		if err != nil {
			return fmt.Errorf("marking the check live: %w", err)
		}
		if release != nil {
			release()
		}
		release = r
		return nil
	}
	record := func(g string) runs.CheckRecord {
		rec := runs.CheckRecord{ID: name + "." + g, Group: g, Repo: req.Repo, Issue: req.Issue, PR: req.PR, SHA: req.SHA, Commands: commands[g], Started: time.Now()}
		if deadline, ok := ctx.Deadline(); ok {
			rec.Expires = deadline
		}
		return rec
	}
	firstRec := record(first)
	if err := mark(firstRec); err != nil {
		return out, err
	}
	c.removeLeftovers()
	if err := c.ws.AddDetached(dir, req.SHA); err != nil {
		return out, err
	}
	defer func() {
		if err := c.ws.RemoveWorktree(dir); err != nil {
			_ = os.RemoveAll(dir)
			_ = c.ws.Prune()
		}
	}()

	for _, g := range groups {
		if len(commands[g]) == 0 {
			out.Groups = append(out.Groups, skippedGroup(g))
			continue
		}
		rec := firstRec
		if g != first {
			rec = record(g)
			if err := mark(rec); err != nil {
				return out, err
			}
		}
		if c.progress != nil {
			c.progress(req, fmt.Sprintf("%s: %s (log: %s.%s.log)", g, strings.Join(commands[g], "; "), dir, g))
		}
		res, err := c.runGroup(ctx, req, dir, rec, g)
		out.Groups = append(out.Groups, res)
		if err != nil {
			return out, err
		}
		if !res.Passed || ctx.Err() != nil {
			break
		}
	}
	out.Passed = out.Failed() == ""
	for _, g := range out.Groups {
		if g.Skipped {
			continue
		}
		out.Log, out.Summary = g.Log, g.Summary
		if !g.Passed {
			break
		}
	}
	out.Description = outcomeDescription(len(groups), out.Groups)
	return out, ctx.Err()
}

// runGroup runs group g's commands in dir, logging them to dir.<g>.log,
// for a check whose live record for the group is rec.
func (c flowChecker) runGroup(ctx context.Context, req flow.CheckRequest, dir string, rec runs.CheckRecord, g string) (flow.GroupOutcome, error) {
	res := flow.GroupOutcome{Group: g, Log: dir + "." + g + ".log"}
	f, err := os.OpenFile(res.Log, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return res, err
	}
	defer f.Close()
	// Unbuffered: the live tails read the log while the commands run.
	l := &checkLog{w: f}
	// A milestone's final checks have no PR yet: they are its epic's.
	what, target := fmt.Sprintf("#%d (!%d)", req.Issue, req.PR), fmt.Sprintf("!%d at %s", req.PR, short(req.SHA))
	if req.PR == 0 {
		what, target = fmt.Sprintf("#%d", req.Issue), fmt.Sprintf("#%d at %s", req.Issue, short(req.SHA))
	}
	started := time.Now()
	l.line(fmt.Sprintf("bottown flow: %s commands for %s at %s, %s", g, what, req.SHA, started.Format(time.RFC3339)))
	if c.tail != nil {
		var limit time.Duration
		if deadline, ok := ctx.Deadline(); ok {
			limit = deadline.Sub(started)
		}
		ended := c.tail.Follow(tailRun{
			ID: rec.ID, Target: target,
			Check: strings.Join(rec.Commands, "; "), Started: started, Limit: limit,
		}, "", res.Log)
		defer ended()
	}
	specs := make([]checkSpec, len(rec.Commands))
	for i, cmd := range rec.Commands {
		specs[i] = checkSpec{g, cmd}
	}
	cr := checkResult{SHA: req.SHA, Log: res.Log, Steps: runChecks(ctx, dir, specs, l, time.Now, true)}
	res.Passed = !cr.failed()
	res.Description = cr.groupDescription(g)
	res.Summary = checkSummary(cr)
	return res, nil
}

// skippedGroup is group g of a repository that names no commands for it.
func skippedGroup(g string) flow.GroupOutcome {
	return flow.GroupOutcome{Group: g, Skipped: true, Description: fmt.Sprintf("the repository names no %s commands", g)}
}

// outcomeDescription is a check's description: for a request of one group,
// its description, or, of more, each listed group's after its name.
func outcomeDescription(requested int, groups []flow.GroupOutcome) string {
	if requested == 1 && len(groups) == 1 {
		return groups[0].Description
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = g.Group + ": " + g.Description
	}
	return strings.Join(parts, "; ")
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
		// The id is the worktree's name and .<group>.
		d, _, _ := strings.Cut(rec.ID, ".")
		live[d] = true
	}
	for _, d := range dirs {
		if !live[d] {
			_ = os.RemoveAll(filepath.Join(c.ws.RunsDir, d))
		}
	}
	_ = c.ws.Prune()
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
