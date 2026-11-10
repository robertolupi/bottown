package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/agents"
	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/land"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

func newMilestoneRun(g *globals) *cobra.Command {
	var spec, repo string
	var parallel, rounds int
	var timeout, checkTimeout time.Duration
	c := &cobra.Command{
		Use:   "run <slug> --agents SPEC --parallel N [--repo R]",
		Short: "Drive a milestone's child issues to completion on milestone/<slug>",
		Long: `Run the active child issues of the plan <slug>'s Implementation Epic, each
as a bottown flow on milestone/<slug>, and finish the milestone when they
are all closed. Owner only.

The epic must have been filed by ` + "`bottown milestone apply`" + `. Run creates
milestone/<slug> if it is missing; if the last final PR from it was merged,
it first merges the default branch into it, so the new round starts there
(on a conflict it stops).

A child is ready when it is open and every issue it is blocked by is
closed. Ready children are dispatched in issue order, at most --parallel at
a time: each gets an implementer and a verifier of another family from
--agents, recorded on it as implement/<actor> and verify/<actor> labels
(a child that has them keeps its pair while both are in the pool), and a
` + "`bottown flow N --unattended`" + ` process of its own,
logged to .bottown/flow-<N>.log. When a flow stops ready to merge, run
lands the PR on milestone/<slug> as ` + "`bottown milestone merge`" + ` does: the
check group at the landing commit, the branch's tip merged with the PR's
head, then the merge as the manager, which closes the child and may make
others ready. Run checks each closed child's box in the epic's checklist;
children ready together land in merge-all's order. If a
final PR is open, it comments there that its head is no longer gated. A
child whose landing conflicts, or whose approval does not carry to the
landing commit, goes back to its flow once (its pair kept); the second
time, and on failed checks, it stops. An actor whose agent did nothing is removed
from the pool until run exits, and its child stays stopped; any other stop
leaves the child to the owner.

--agents is a comma-separated list of actor[:model]=N entries, N the most
flows the entry is paired in at once, or * for no limit: for example
claude=*,agy:gemini-3.8-flash=1,kimi=2. Each actor appears once, and
every family needs a verifier of another family in the pool.

When every active child is closed, run is ` + "`bottown milestone finish`" + `.
Otherwise, when nothing more can be dispatched, it comments a summary on
the epic: merged, stopped, blocked, removed pool entries, and the issues
it ignored or flagged. Run keeps no state: run it again to resume.

Exit status: 0 when the final PR is open; 3 when run stopped short and an
agent did nothing; 1 otherwise.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if parallel < 1 {
				return fmt.Errorf("--parallel must be at least 1")
			}
			rc, err := openRunContextRepo(g, repo)
			if err != nil {
				return err
			}
			ctx, out := cmd.Context(), cmd.OutOrStdout()
			var infos []server.ActorInfo
			if err := callServer(ctx, rc.inst, rc.key, "GET", "/v1/actors", nil, &infos); err != nil {
				return err
			}
			actors := make([]config.Actor, len(infos))
			for i, a := range infos {
				actors[i] = config.Actor{Name: a.Name, Family: a.Family, Harness: a.Harness}
			}
			pool, err := agents.Parse(spec, actors)
			if err != nil {
				return err
			}
			pf := newProxyForge(rc.inst.ServerURL(), rc.key, rc.org, rc.repo)
			owner, err := pf.ownerLogin(ctx)
			if err != nil {
				return fmt.Errorf("finding the owner's login: %w", err)
			}
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			top, err := mainCheckout()
			if err != nil {
				return err
			}
			c := client.New(rc.inst.ServerURL(), rc.key).WithTimeout(5 * time.Minute)
			api := milestoneClient{c: c}
			f := &runForge{proxyForge: pf, api: api, c: c, slug: args[0],
				checker: flowChecker{ws: rc.ws}, runsDir: rc.ws.RunsDir, checkTimeout: checkTimeout}
			l := &flowLauncher{bin: bin, instance: rc.inst.Name, repo: rc.repo, dir: top, logDir: rc.ws.Dir, runsDir: rc.ws.RunsDir, owner: owner, c: c,
				rounds: rounds, timeout: timeout, checkTimeout: checkTimeout}
			_, err = milestone.Run(ctx, f, milestone.RunOptions{
				Slug: args[0], Owner: owner, Pool: pool, Parallel: parallel, Launcher: l, Out: out,
				Finish: func(ctx context.Context) error {
					return milestoneFinish(ctx, out, api, flowChecker{ws: rc.ws}, checkTimeout, rc.repo, args[0])
				},
			})
			return err
		},
	}
	c.Flags().StringVar(&spec, "agents", "", "the pool: comma-separated actor[:model]=N entries, N a number or * (required)")
	c.Flags().IntVar(&parallel, "parallel", 0, "the most child issues in flight at once, and so e2e suites running at once (required)")
	c.Flags().IntVar(&rounds, "rounds", 3, "each flow's --rounds")
	c.Flags().DurationVar(&timeout, "timeout", time.Hour, "each flow's --timeout, the time limit for each run")
	c.Flags().DurationVar(&checkTimeout, "check-timeout", defaultCheckTimeout, "the time limit for the e2e checks, in each flow and in finish")
	c.Flags().StringVar(&repo, "repo", "", "repository name within the instance's org (default: this checkout)")
	_ = c.MarkFlagRequired("agents")
	_ = c.MarkFlagRequired("parallel")
	return c
}

// runForge is milestone.RunForge with the owner's key: reads and labels
// through the API proxy, the milestone operations through the server.
type runForge struct {
	*proxyForge
	api  milestoneClient
	c    *client.Client
	slug string
	// checker, runsDir and checkTimeout are land's: the check group at each
	// landing commit, and where it takes the child's flow lock.
	checker      flow.Checker
	runsDir      string
	checkTimeout time.Duration
}

func (f *runForge) in() server.MilestoneIn { return server.MilestoneIn{Repo: f.repo, Slug: f.slug} }

func (f *runForge) Branch(ctx context.Context) (bool, error) {
	var out server.MilestoneBranchOut
	err := f.api.call(ctx, "branch", f.in(), &out)
	return out.Created, err
}

func (f *runForge) Restart(ctx context.Context) (milestone.Restart, error) {
	var out server.MilestoneRestartOut
	err := f.api.call(ctx, "restart", f.in(), &out)
	return milestone.Restart{Merged: out.Merged, Final: out.Final, Base: out.Base, HeadSHA: out.HeadSHA}, err
}

func (f *runForge) SetPair(ctx context.Context, n int64, implement, verify string) error {
	var have []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	path := f.repoPath(fmt.Sprintf("/issues/%d/labels", n))
	if err := f.call(ctx, "GET", path, nil, &have); err != nil {
		return err
	}
	add := []string{"implement/" + implement, "verify/" + verify}
	want := slices.Clone(add)
	for _, l := range have {
		switch {
		case slices.Contains(want, l.Name):
			add = slices.DeleteFunc(add, func(name string) bool { return name == l.Name })
		case strings.HasPrefix(l.Name, "implement/") || strings.HasPrefix(l.Name, "verify/"):
			if err := f.call(ctx, "DELETE", fmt.Sprintf("%s/%d", path, l.ID), nil, nil); err != nil {
				return fmt.Errorf("removing %s from #%d: %w", l.Name, n, err)
			}
		}
	}
	if len(add) == 0 {
		return nil
	}
	return f.call(ctx, "POST", path, map[string]any{"labels": add}, nil)
}

func (f *runForge) Land(ctx context.Context, n int64) milestone.Landing {
	l, err := milestoneLand(ctx, f.c, f.checker, f.runsDir, f.checkTimeout, f.repo, n, "")
	if err != nil {
		var ae *client.APIError
		if errors.As(err, &ae) && ae.Message != "" {
			err = errors.New(ae.Message)
		}
		return milestone.Landing{Kind: milestone.LandFailed, Reason: err.Error()}
	}
	return l
}

// Order orders the children ns by their PRs, as merge-all does
// (land.Order): from a trial merge of the milestone branch into each.
func (f *runForge) Order(ctx context.Context, ns []int64) ([]int64, error) {
	cands := make([]land.Candidate, 0, len(ns))
	issue := map[int64]int64{}
	for _, n := range ns {
		pr, err := childPR(ctx, f.c, f.repo, n)
		if err != nil {
			return nil, fmt.Errorf("#%d: %w", n, err)
		}
		q := url.Values{"repo": {f.repo}, "number": {fmt.Sprint(pr.Number)}}
		var st server.PRMergeStatus
		if err := f.c.Call(ctx, "GET", "/v1/forge/pr/merge-base?"+q.Encode(), nil, &st); err != nil {
			return nil, fmt.Errorf("trial merge of !%d: %w", pr.Number, err)
		}
		cands = append(cands, land.Candidate{PR: pr.Number, Clean: st.Clean, Files: st.Files})
		issue[pr.Number] = n
	}
	out := make([]int64, 0, len(ns))
	for _, c := range land.Order(cands) {
		out = append(out, issue[c.PR])
	}
	return out, nil
}

func (f *runForge) FinalPR(ctx context.Context) (int64, error) {
	q := url.Values{"repo": {f.repo}, "state": {"open"}}
	var prs []server.PRItem
	if err := f.c.Call(ctx, "GET", "/v1/forge/prs?"+q.Encode(), nil, &prs); err != nil {
		return 0, err
	}
	for _, pr := range prs {
		if pr.HeadBranch == server.MilestoneBranchPrefix+f.slug {
			return pr.Number, nil
		}
	}
	return 0, nil
}

func (f *runForge) Comment(ctx context.Context, n int64, body string) error {
	return f.api.Comment(ctx, f.repo, n, body)
}

// flowLauncher runs each child's flow as a `bottown flow` process of its
// own: the flow command swaps its cobra command's context per run, so one
// process holds one flow supervisor. Its output goes to the log the web UI
// shows for a flow, .bottown/flow-<N>.log, and a record next to it lets
// the web UI show and stop it.
type flowLauncher struct {
	bin, instance, repo string
	dir, logDir         string
	runsDir             string // where flows take their issue's lock
	owner               string // the stop comments' author
	c                   *client.Client
	rounds              int
	timeout             time.Duration
	checkTimeout        time.Duration
}

func (l *flowLauncher) Flow(ctx context.Context, req milestone.FlowRequest) milestone.Outcome {
	failed := func(err error) milestone.Outcome {
		return milestone.Outcome{Kind: milestone.FlowStopped, Reason: "flow failed", Detail: err.Error()}
	}
	// The stop is the flow's comment after the last one there now.
	_, after, err := l.lastStop(ctx, req.Issue, 0)
	if err != nil {
		return failed(fmt.Errorf("reading #%d's comments: %w", req.Issue, err))
	}
	logPath := filepath.Join(l.logDir, fmt.Sprintf("flow-%d.log", req.Issue))
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return failed(err)
	}
	defer log.Close()
	cmd := exec.Command(l.bin, "-i", l.instance, "flow", fmt.Sprint(req.Issue),
		"--implement", req.Implement, "--verify", req.Verify, "--unattended",
		"--repo", l.repo, "--rounds", fmt.Sprint(l.rounds), "--timeout", l.timeout.String(), "--check-timeout", l.checkTimeout.String())
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = l.dir, os.Environ(), log, log
	// A group of its own so this process can forward its stop to the flow.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// One flow per issue: leave a child another invocation is driving to it.
	if pid, held := runs.FlowHolder(l.runsDir, l.repo, req.Issue); held {
		busy := &runs.FlowBusyError{Repo: l.repo, Issue: req.Issue, PID: pid}
		return milestone.Outcome{Kind: milestone.FlowStopped, Reason: "flow busy", Detail: busy.Error()}
	}
	if err := cmd.Start(); err != nil {
		return failed(err)
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-pid, syscall.SIGINT)
		waitErr = <-done
	}
	if ctx.Err() != nil {
		return failed(ctx.Err())
	}
	// The stop is read with a context of its own: the flow has ended.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	stop, _, err := l.lastStop(rctx, req.Issue, after)
	switch {
	case err != nil:
		return failed(fmt.Errorf("the flow ended (%v), and reading its stop failed: %w; log: %s", waitErr, err, logPath))
	case stop == nil:
		detail := fmt.Sprintf("the flow ended without a stop comment (%v)", waitErr)
		if line := lastLine(logPath); line != "" {
			detail += ": " + line
		}
		return milestone.Outcome{Kind: milestone.FlowStopped, Reason: "flow failed", Detail: detail + "; log: " + logPath}
	}
	oc := milestone.Outcome{Kind: milestone.FlowStopped, Reason: string(stop.Reason), Detail: "log: " + logPath}
	switch {
	case stop.Reason == flow.ReadyToMerge:
		oc.Kind = milestone.FlowReady
	case stop.Reason == flow.AgentDidNothing && stop.Actor != "":
		oc.Kind, oc.Actor = milestone.FlowIdle, stop.Actor
	}
	return oc
}

// lastStop is the owner's last flow stop comment on issue n with an ID
// above after, nil if there is none, and the ID of the last comment.
func (l *flowLauncher) lastStop(ctx context.Context, n, after int64) (*flow.Stop, int64, error) {
	q := url.Values{"repo": {l.repo}, "number": {fmt.Sprint(n)}}
	var is server.IssueOut
	if err := l.c.Call(ctx, "GET", "/v1/forge/issue?"+q.Encode(), nil, &is); err != nil {
		return nil, 0, err
	}
	var stop *flow.Stop
	last := after
	for _, c := range is.Comments {
		last = max(last, c.ID)
		if c.ID <= after || c.Author != l.owner {
			continue
		}
		if s, ok := flow.ParseStop(c.Body); ok {
			stop = &s
		}
	}
	return stop, last, nil
}

// lastLine is the last non-blank line of the file at path.
func lastLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
