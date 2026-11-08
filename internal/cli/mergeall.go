package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/land"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
	"github.com/spf13/cobra"
)

func newMergeAll(g *globals) *cobra.Command {
	var actor, verify, repo string
	var dryRun bool
	c := &cobra.Command{
		Use:   "merge-all [PR...] [--actor A] [--verify V] [--dry-run] [--repo R]",
		Short: "Land every approved pull request into the default branch (owner only)",
		Long: `Land approved pull requests one at a time, each through the shared
landing step (internal/land): bring the base in, run the checks at the exact
combined commit, and merge fast-forward-only as the manager.

First it fetches the default branch from the forge and fast-forwards the
local main of the owner's checkout; it stops when main has diverged or has
uncommitted changes while checked out. It does it again when the batch is
done, so the checkout holds what landed. It never runs after_pull. With
--repo R for another repository of the instance, the base is R's default
branch on the forge and the local checkout is left alone.

With no arguments the batch is every open pull request into the default
branch that pr list assesses approved, not a draft, with no live run or
held flow lock, and not part of a milestone (a child issue's pull request,
or a milestone/* head). Those land through milestone run, or by naming
them: arguments restrict the batch to those numbers, milestone pull
requests included, and one that is not a candidate is reported and
skipped. A named pull request lands into its own base, a milestone/*
branch included, not into the default branch.

Each round it orders the remaining candidates (land.Order, with fresh
trial merges): those whose base merges cleanly first, then the fewest
overlapping candidates, then the lowest number, and lands the first.

Without --actor a conflict or a lost approval (a landing commit whose
review did not carry over) is reported and left. With --actor A, it runs
the flow on each remaining issue/<N> candidate after the clean candidates
landed, with implementer A and verifier --verify V or the candidate's
recorded pair (the issue's implement/ and verify/ labels, else the pull
request's author and the actor whose approval the assessment counted), and
then lands again. A final milestone pull request is never repaired. Exit 0
when every candidate landed, 1 otherwise.

Owner only.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if verify != "" && actor == "" {
				return errors.New("--verify needs --actor")
			}
			numbers, err := parsePRNumbers(args)
			if err != nil {
				return err
			}
			rc, err := openRunContextRepo(g, repo)
			if err != nil {
				return err
			}
			ctx, out := cmd.Context(), cmd.OutOrStdout()
			pf := newProxyForge(rc.inst.ServerURL(), rc.key, rc.org, rc.repo)
			// The base is the repository's default branch on the forge. Only
			// this checkout's own repository has a local branch of it to
			// fast-forward.
			base := ""
			if rc.repo == rc.checkout {
				if base, err = syncMainCheckout(ctx, out, rc.ws.Top, rc.inst.Remote(), dryRun); err != nil {
					return err
				}
			} else {
				if base, _, err = pf.DefaultBranch(ctx); err != nil {
					return err
				}
				if dryRun {
					fmt.Fprintf(out, "%s: not this checkout's repository; would leave its local branches alone\n", rc.repo)
				}
			}
			owner, err := pf.ownerLogin(ctx)
			if err != nil {
				return fmt.Errorf("finding the owner's login: %w", err)
			}
			m := &mergeAll{
				out: out, repo: rc.repo, owner: owner, base: base, api: client.New(rc.inst.ServerURL(), rc.key).WithTimeout(5 * time.Minute),
				ws: rc.ws, actor: actor, verify: verify, dryRun: dryRun,
				timeout: time.Hour, checkTimeout: defaultCheckTimeout,
				rounds: repairRounds, checkRounds: flow.DefaultCheckRounds,
			}
			m.runFlow = mergeAllFlow(cmd, g, rc, m)
			landed, err := m.run(ctx, numbers)
			if err != nil {
				return err
			}
			if !dryRun && rc.repo == rc.checkout {
				if _, err := syncMainCheckout(ctx, out, rc.ws.Top, rc.inst.Remote(), false); err != nil {
					return err
				}
			}
			if !landed {
				return errors.New("not every candidate landed")
			}
			return nil
		},
	}
	c.Flags().StringVar(&actor, "actor", "", "also repair a conflicting or unverified candidate by running the flow with this implementer")
	c.Flags().StringVar(&verify, "verify", "", "with --actor, the verifier to use instead of the candidate's recorded one")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the order and the trial merges, touching nothing")
	c.Flags().StringVar(&repo, "repo", "", "repository name within the instance's org (default: this checkout)")
	return c
}

// parsePRNumbers reads the command's optional pull request arguments: !N,
// #N or N, in the order given, each once.
func parsePRNumbers(args []string) ([]int64, error) {
	var out []int64
	for _, a := range args {
		n, err := parseNumber(a)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// syncMainCheckout fetches the default branch from the forge and
// fast-forwards the local branch of the checkout at top to it, returning
// the branch's name. It stops when the local branch has diverged, or is
// checked out with uncommitted changes. A dry run prints what it would do
// and touches nothing.
func syncMainCheckout(ctx context.Context, out io.Writer, top, remote string, dryRun bool) (string, error) {
	base, err := runs.RemoteDefaultBranch(ctx, top, remote)
	if err != nil {
		return "", err
	}
	if dryRun {
		fmt.Fprintf(out, "main: would fetch %s %s and fast-forward %s\n", remote, base, base)
		return base, nil
	}
	if _, err := runs.GitCtx(ctx, top, nil, "fetch", "-q", remote, base); err != nil {
		return "", err
	}
	remoteSHA, err := runs.GitCtx(ctx, top, nil, "rev-parse", "refs/remotes/"+remote+"/"+base)
	if err != nil {
		return "", fmt.Errorf("%s/%s is not on %s: %w", remote, base, remote, err)
	}
	localSHA, err := runs.GitCtx(ctx, top, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+base)
	switch {
	case err != nil:
		// No local branch yet: create it at the remote's tip.
		if _, err := runs.GitCtx(ctx, top, nil, "update-ref", "refs/heads/"+base, remoteSHA); err != nil {
			return "", err
		}
		fmt.Fprintf(out, "%s: created at %s\n", base, shortSHA(remoteSHA))
		return base, nil
	case localSHA == remoteSHA:
		return base, nil
	case !isAncestor(ctx, top, localSHA, remoteSHA):
		return "", fmt.Errorf("local %s has diverged from %s/%s; resolve it by hand", base, remote, base)
	}
	current, err := runs.GitCtx(ctx, top, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if current == base {
		if dirty, err := trackedChanges(ctx, top); err != nil {
			return "", err
		} else if dirty {
			return "", fmt.Errorf("%s is checked out with uncommitted changes; commit or stash them", base)
		}
		if _, err := runs.GitCtx(ctx, top, nil, "merge", "--ff-only", "-q", remoteSHA); err != nil {
			return "", err
		}
	} else if _, err := runs.GitCtx(ctx, top, nil, "update-ref", "refs/heads/"+base, remoteSHA); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "%s: fast-forwarded to %s\n", base, shortSHA(remoteSHA))
	return base, nil
}

// isAncestor reports whether ancestor is an ancestor of descendant.
func isAncestor(ctx context.Context, dir, ancestor, descendant string) bool {
	_, err := runs.GitCtx(ctx, dir, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

// trackedChanges reports whether the checkout at top has uncommitted
// changes to tracked files; untracked files do not block a fast-forward.
func trackedChanges(ctx context.Context, top string) (bool, error) {
	out, err := runs.GitCtx(ctx, top, nil, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// mergeCandidate is one pull request merge-all may land.
type mergeCandidate struct {
	number     int64
	author     string
	headBranch string
	headSHA    string
	base       string
	milestone  bool // part of a milestone, landed only when named
	files      []string
	clean      bool
	conflicts  []string
}

// mergeAll lands a batch of approved pull requests.
type mergeAll struct {
	out    io.Writer
	repo   string
	owner  string
	base   string
	api    *client.Client
	ws     *runs.Workspace
	actor  string
	verify string
	dryRun bool

	timeout      time.Duration
	checkTimeout time.Duration
	// rounds and checkRounds are the repair flow's budgets, as `bottown
	// flow`'s --rounds and --check-rounds.
	rounds      int
	checkRounds int

	// runFlow runs the flow's supervisor in-process on one issue, for the
	// --actor repair. Tests replace it.
	runFlow func(ctx context.Context, issue int64, implement, verify string) error
	// checker runs the landing checks; nil uses the flow's grouped checker
	// over ws. Tests replace it.
	checker flow.Checker
}

// repairRounds is the review budget of a repair flow, as `bottown flow`'s
// --rounds default.
const repairRounds = 3

// landChecker is the checker the landing primitive runs the policy's
// groups with.
func (m *mergeAll) landChecker() flow.Checker {
	if m.checker != nil {
		return m.checker
	}
	return flowChecker{ws: m.ws}
}

// run builds the batch, orders and lands it, repairs it when asked, prints
// the table and reports whether every candidate landed.
func (m *mergeAll) run(ctx context.Context, numbers []int64) (bool, error) {
	batch, skipped, err := m.batch(ctx, numbers)
	if err != nil {
		return false, err
	}
	total := len(batch)
	if total == 0 {
		fmt.Fprintln(m.out, "no candidates to land")
		return skipped == 0, nil
	}
	if m.dryRun {
		return m.dryRunOrder(ctx, batch)
	}
	results := &results{status: map[int64]string{}, landed: map[int64]bool{}}
	var repair []*mergeCandidate
	remaining := slices.Clone(batch)
	for len(remaining) > 0 {
		ordered, err := m.order(ctx, remaining)
		if err != nil {
			return false, err
		}
		c := ordered[0]
		oc := land.Land(ctx, m.api, m.landChecker(), m.policy(), m.prOut(c))
		fmt.Fprintln(m.out, oc)
		results.record(c, oc)
		remaining = removeCandidate(remaining, c.number)
		switch oc.(type) {
		case land.Conflict, land.NeedsVerify:
			repair = append(repair, c)
		}
	}
	if m.actor != "" && len(repair) > 0 {
		repaired, err := m.repairAll(ctx, repair)
		if err != nil {
			return false, err
		}
		if err := m.reland(ctx, repaired, results); err != nil {
			return false, err
		}
	}
	m.table(batch, results)
	return skipped == 0 && len(results.landed) == total, nil
}

// results is each candidate's printed outcome, and which landed.
type results struct {
	status map[int64]string
	landed map[int64]bool
}

func (r *results) record(c *mergeCandidate, oc land.Outcome) {
	r.status[c.number] = oc.String()
	if _, ok := oc.(land.Landed); ok {
		r.landed[c.number] = true
	}
}

func (m *mergeAll) policy() land.Policy {
	return land.Policy{Repo: m.repo, RunsDir: m.ws.RunsDir, Kind: land.Main, CheckTimeout: m.checkTimeout}
}

// prOut is the pull request land.Land re-reads and revalidates: only its
// number, branches and head are read before the server is asked.
func (m *mergeAll) prOut(c *mergeCandidate) server.PROut {
	return server.PROut{Number: c.number, HeadBranch: c.headBranch, HeadSHA: c.headSHA, BaseBranch: c.base}
}

// batch is the candidate set and how many named numbers were skipped:
// numbers select and may include milestone pull requests; with none, the
// default batch leaves milestone work out.
func (m *mergeAll) batch(ctx context.Context, numbers []int64) (batch []*mergeCandidate, skipped int, err error) {
	prs, err := m.openPRs(ctx)
	if err != nil {
		return nil, 0, err
	}
	live, err := m.liveRuns(ctx)
	if err != nil {
		return nil, 0, err
	}
	byNumber := map[int64]server.PRItem{}
	for _, pr := range prs {
		byNumber[pr.Number] = pr
	}
	if len(numbers) > 0 {
		for _, n := range numbers {
			pr, ok := byNumber[n]
			if !ok {
				fmt.Fprintf(m.out, "!%d: not an open pull request; skipped\n", n)
				skipped++
				continue
			}
			c, ok, err := m.candidate(ctx, pr, live)
			if err != nil {
				return nil, 0, err
			}
			if !ok {
				fmt.Fprintf(m.out, "!%d: not a candidate: open, approved, not a draft, with no live run or flow lock\n", n)
				skipped++
				continue
			}
			batch = append(batch, c)
		}
		return batch, skipped, nil
	}
	for _, pr := range prs {
		if pr.BaseBranch != m.base {
			continue
		}
		c, ok, err := m.candidate(ctx, pr, live)
		if err != nil {
			return nil, 0, err
		}
		if !ok || c.milestone {
			continue
		}
		batch = append(batch, c)
	}
	return batch, 0, nil
}

// candidate reports whether pr may be landed, and describes it.
func (m *mergeAll) candidate(ctx context.Context, pr server.PRItem, live []secrets.Run) (*mergeCandidate, bool, error) {
	if pr.State != "open" || pr.Merged || pr.Draft || pr.HeadSHA == "" {
		return nil, false, nil
	}
	if pr.Assessment == nil || pr.Assessment.Decision != review.Approved {
		return nil, false, nil
	}
	issue := issueOf(pr.HeadBranch)
	for _, r := range live {
		if r.Ended != nil || (!r.Expires.IsZero() && time.Now().After(r.Expires)) {
			continue
		}
		if r.PR == pr.Number || (pr.HeadBranch != "" && r.Branch == pr.HeadBranch) || (issue > 0 && r.Issue == issue) {
			return nil, false, nil
		}
	}
	c := &mergeCandidate{number: pr.Number, author: pr.Author, headBranch: pr.HeadBranch, headSHA: pr.HeadSHA, base: pr.BaseBranch}
	if strings.HasPrefix(pr.HeadBranch, server.MilestoneBranchPrefix) {
		c.milestone = true
		return c, true, nil
	}
	if issue > 0 {
		if _, held := runs.FlowHolder(m.ws.RunsDir, m.repo, issue); held {
			return nil, false, nil
		}
		mil, err := m.milestoneChild(ctx, issue)
		if err != nil {
			return nil, false, err
		}
		c.milestone = mil
	}
	return c, true, nil
}

// milestoneChild reports whether issue carries the owner's trusted
// bottown:milestone child marker.
func (m *mergeAll) milestoneChild(ctx context.Context, issue int64) (bool, error) {
	is, err := m.issue(ctx, issue)
	if err != nil {
		return false, err
	}
	if !milestone.Trusted(is.Author, m.owner) {
		return false, nil
	}
	mk, _, ok := milestone.ParseMarker(is.Body)
	return ok && !mk.Epic && mk.Key != "", nil
}

// issue reads one issue alone.
func (m *mergeAll) issue(ctx context.Context, number int64) (server.IssueOut, error) {
	q := url.Values{"repo": {m.repo}, "number": {fmt.Sprint(number)}, "comments": {"0"}}
	var is server.IssueOut
	err := m.api.Call(ctx, "GET", "/v1/forge/issue?"+q.Encode(), nil, &is)
	return is, err
}

// openPRs lists the open pull requests of the repository.
func (m *mergeAll) openPRs(ctx context.Context) ([]server.PRItem, error) {
	q := url.Values{"repo": {m.repo}, "state": {"open"}}
	var prs []server.PRItem
	if err := m.api.Call(ctx, "GET", "/v1/forge/prs?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

// liveRuns lists the live runs of the repository.
func (m *mergeAll) liveRuns(ctx context.Context) ([]secrets.Run, error) {
	q := url.Values{"repo": {m.repo}, "state": {"live"}}
	var live []secrets.Run
	if err := m.api.Call(ctx, "GET", "/v1/runs?"+q.Encode(), nil, &live); err != nil {
		return nil, err
	}
	return live, nil
}

// order trial-merges each candidate's base and orders them with land.Order.
func (m *mergeAll) order(ctx context.Context, batch []*mergeCandidate) ([]*mergeCandidate, error) {
	for _, c := range batch {
		q := url.Values{"repo": {m.repo}, "number": {fmt.Sprint(c.number)}}
		var st server.PRMergeStatus
		if err := m.api.Call(ctx, "GET", "/v1/forge/pr/merge-base?"+q.Encode(), nil, &st); err != nil {
			return nil, err
		}
		c.clean, c.conflicts, c.files = st.Clean, st.Conflicts, st.Files
	}
	byNumber := map[int64]*mergeCandidate{}
	for _, c := range batch {
		byNumber[c.number] = c
	}
	ordered := land.Order(candidatesOf(batch))
	out := make([]*mergeCandidate, len(ordered))
	for i, c := range ordered {
		out[i] = byNumber[c.PR]
	}
	return out, nil
}

// dryRunOrder prints the order, each trial merge and overlap count; the
// main step was printed by syncMainCheckout. It touches nothing.
func (m *mergeAll) dryRunOrder(ctx context.Context, batch []*mergeCandidate) (bool, error) {
	ordered, err := m.order(ctx, batch)
	if err != nil {
		return false, err
	}
	overlaps := land.Overlaps(candidatesOf(ordered))
	ok := true
	for _, c := range ordered {
		overlap := overlaps[c.number]
		touch := "clean"
		if !c.clean {
			touch = "conflict: " + strings.Join(c.conflicts, ", ")
			ok = false
		}
		fmt.Fprintf(m.out, "!%d: %s, overlaps %d other candidate(s)\n", c.number, touch, overlap.Count)
	}
	return ok, nil
}

// repairAll runs the flow on each repairable candidate and returns those
// to land again.
func (m *mergeAll) repairAll(ctx context.Context, batch []*mergeCandidate) ([]*mergeCandidate, error) {
	var out []*mergeCandidate
	for _, c := range batch {
		issue := issueOf(c.headBranch)
		if issue <= 0 {
			fmt.Fprintf(m.out, "!%d: %s is not an issue branch; reported, not repaired\n", c.number, c.headBranch)
			continue
		}
		implement, verify, err := m.pair(ctx, c, issue)
		if err != nil {
			fmt.Fprintf(m.out, "!%d: %v\n", c.number, err)
			continue
		}
		fmt.Fprintf(m.out, "!%d: repairing with %s and %s\n", c.number, implement, verify)
		if err := m.runFlow(ctx, issue, implement, verify); err != nil {
			fmt.Fprintf(m.out, "!%d: repair failed: %v\n", c.number, err)
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// pair is the implementer and verifier for a candidate's repair: the flags
// when given, else the issue's implement/ and verify/ labels, else the
// pull request's author and the actor whose approval the assessment
// counted.
func (m *mergeAll) pair(ctx context.Context, c *mergeCandidate, issue int64) (implement, verify string, err error) {
	implement, verify = m.actor, m.verify
	if implement == "" {
		return "", "", errors.New("no --actor")
	}
	is, err := m.issue(ctx, issue)
	if err != nil {
		return "", "", err
	}
	if verify == "" {
		verify = labelValue(is.Labels, "verify/")
	}
	if verify == "" {
		approver, err := m.approver(ctx, c.number)
		if err != nil {
			return "", "", err
		}
		verify = approver
	}
	if verify == "" {
		return "", "", errors.New("no verifier on record (the approval is the owner's); pass --verify")
	}
	return implement, verify, nil
}

// approver is the reviewer whose approval the PR's assessment counted, or
// "" when it rests on no review (an owner approval).
func (m *mergeAll) approver(ctx context.Context, number int64) (string, error) {
	q := url.Values{"repo": {m.repo}, "number": {fmt.Sprint(number)}}
	var pr server.PROut
	if err := m.api.Call(ctx, "GET", "/v1/forge/pr?"+q.Encode(), nil, &pr); err != nil {
		return "", err
	}
	if pr.Assessment.Review == nil {
		return "", nil
	}
	return pr.Assessment.Review.Reviewer, nil
}

// reland lands the repaired candidates, in a fresh order, recording each.
func (m *mergeAll) reland(ctx context.Context, batch []*mergeCandidate, out *results) error {
	remaining := slices.Clone(batch)
	for len(remaining) > 0 {
		ordered, err := m.order(ctx, remaining)
		if err != nil {
			return err
		}
		c := ordered[0]
		oc := land.Land(ctx, m.api, m.landChecker(), m.policy(), m.prOut(c))
		fmt.Fprintln(m.out, oc)
		out.record(c, oc)
		remaining = removeCandidate(remaining, c.number)
	}
	return nil
}

// table prints one row per candidate at the end of the batch.
func (m *mergeAll) table(batch []*mergeCandidate, r *results) {
	fmt.Fprintln(m.out, "\nPR     outcome")
	for _, c := range batch {
		state, ok := r.status[c.number]
		if !ok {
			state = "did not land"
		}
		if r.landed[c.number] {
			state = "landed"
		}
		fmt.Fprintf(m.out, "!%-5d %s\n", c.number, state)
	}
}

func removeCandidate(batch []*mergeCandidate, number int64) []*mergeCandidate {
	return slices.DeleteFunc(batch, func(c *mergeCandidate) bool { return c.number == number })
}

func candidatesOf(batch []*mergeCandidate) []land.Candidate {
	out := make([]land.Candidate, len(batch))
	for i, c := range batch {
		out[i] = land.Candidate{PR: c.number, Clean: c.clean, Files: c.files}
	}
	return out
}

// issueOf is the issue number of an issue/<N> branch, or 0.
func issueOf(branch string) int64 {
	s, ok := strings.CutPrefix(branch, "issue/")
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// labelValue is the first label with the prefix, without it.
func labelValue(labels []string, prefix string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, prefix); ok {
			return v
		}
	}
	return ""
}

// repairSupervisor is the in-process flow supervisor that repairs one
// candidate: the flow over api and runner, with the landing checks, the
// output and the issue's flow lock.
func (m *mergeAll) repairSupervisor(api flow.API, runner flow.Runner) *flow.Supervisor {
	return &flow.Supervisor{
		API:     api,
		Runner:  runner,
		Checker: m.landChecker(),
		Out:     m.out,
		Lock: func(repo string, issue int64) (func(), error) {
			return runs.HoldFlow(m.ws.RunsDir, repo, issue)
		},
	}
}

// repairOptions are the flow options a repair runs with: the review and
// check budgets, the run timeouts, and no serialization (a repair is the
// only run merge-all starts).
func (m *mergeAll) repairOptions(issue int64, implement, verify string) flow.Options {
	return flow.Options{
		Issue: issue, Repo: m.repo, Implement: implement, Verify: verify,
		Rounds: m.rounds, CheckRounds: m.checkRounds, Timeout: m.timeout, CheckTimeout: m.checkTimeout,
	}
}

// repairFlow is the --actor repair: the flow's supervisor over api and
// runner, run on each candidate's issue. Tests call it with the fake
// server as a flow.API and a recording flow.RunnerFunc, so the real
// supervisor and its options run.
func repairFlow(m *mergeAll, api flow.API, runner flow.Runner) func(context.Context, int64, string, string) error {
	sup := m.repairSupervisor(api, runner)
	return func(ctx context.Context, issue int64, implement, verify string) error {
		_, err := sup.Run(ctx, m.repairOptions(issue, implement, verify))
		return err
	}
}

// mergeAllFlow is repairFlow over the owner's server client and the CLI's
// run launcher.
func mergeAllFlow(cmd *cobra.Command, g *globals, rc *runContext, m *mergeAll) func(context.Context, int64, string, string) error {
	return repairFlow(m, flow.NewClient(rc.inst, rc.key), flow.RunnerFunc(func(ctx context.Context, req flow.RunRequest) error {
		target := req.Issue
		if req.PR > 0 {
			target = req.PR
		}
		previous := cmd.Context()
		cmd.SetContext(ctx)
		err := startRun(cmd, g, req.Kind, fmt.Sprint(target), flowRunOpts(req, "", nil))
		cmd.SetContext(previous)
		return err
	}))
}
