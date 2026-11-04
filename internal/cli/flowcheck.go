package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/runs"
)

// flowChecker runs the post-implement checks for bottown flow: the e2e
// commands .bottown.toml names on the default branch (default: make e2e),
// in a clean worktree detached at the PR head, with no agent involved. The
// log goes next to the run records, <runs>/flow-<issue>-<sha>.e2e.log.
type flowChecker struct {
	ws *runs.Workspace
}

func (c flowChecker) name(req flow.CheckRequest) string {
	return fmt.Sprintf("flow-%d-%s", req.Issue, short(req.SHA))
}

func (c flowChecker) LogPath(req flow.CheckRequest) string {
	return filepath.Join(c.ws.RunsDir, c.name(req)+".e2e.log")
}

func (c flowChecker) Check(ctx context.Context, req flow.CheckRequest) (flow.CheckOutcome, error) {
	out := flow.CheckOutcome{Log: c.LogPath(req)}
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
	// The check worktree is not a run's: it is named for the issue and SHA,
	// not a run id, so it does not go through Workspace.WorktreeDir.
	dir := filepath.Join(c.ws.RunsDir, c.name(req))
	_ = os.RemoveAll(dir) // what an interrupted check left behind
	_, _ = runs.Git(c.ws.Mirror, nil, "worktree", "prune")
	if _, err := runs.Git(c.ws.Mirror, nil, "worktree", "add", "-q", "--detach", dir, req.SHA); err != nil {
		return out, err
	}
	defer func() {
		if _, err := runs.Git(c.ws.Mirror, nil, "worktree", "remove", "--force", dir); err != nil {
			_ = os.RemoveAll(dir)
			_, _ = runs.Git(c.ws.Mirror, nil, "worktree", "prune")
		}
	}()

	f, err := os.OpenFile(out.Log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return out, err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	defer bw.Flush()
	l := &checkLog{w: bw}
	l.line(fmt.Sprintf("bottown flow: post-implement checks for #%d (!%d) at %s, %s", req.Issue, req.PR, req.SHA, time.Now().Format(time.RFC3339)))
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
