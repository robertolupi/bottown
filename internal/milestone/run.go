package milestone

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/robertolupi/bottown/internal/agents"
)

// RunForge is what Run needs of the forge and the server: Reader's reads,
// and the owner's operations on the milestone and its children.
type RunForge interface {
	Reader
	// Branch creates the milestone branch from the default branch's head
	// if it is missing, as `bottown milestone branch` does.
	Branch(ctx context.Context) (created bool, err error)
	// Restart merges the default branch into the milestone branch when the
	// last final PR was merged and the branch does not contain its merge.
	Restart(ctx context.Context) (Restart, error)
	// SetPair labels issue n implement/<implement> and verify/<verify>,
	// removing its other implement/* and verify/* labels.
	SetPair(ctx context.Context, n int64, implement, verify string) error
	// Land lands issue n's approved PR on the milestone branch through
	// land.Land with the milestone policy: the landing commit, the check
	// group at it, and the fast-forward-only merge as the manager, which
	// closes n.
	Land(ctx context.Context, n int64) Landing
	// Order is the issues ns, ready to land together, in land.Order's
	// order, from fresh trial merges of the branch into their PRs.
	Order(ctx context.Context, ns []int64) ([]int64, error)
	// FinalPR is the open final PR from the milestone branch into the
	// default branch, 0 if there is none.
	FinalPR(ctx context.Context) (int64, error)
	Comment(ctx context.Context, n int64, body string) error
	// EditIssue sets issue n's title and body: Run checks the epic's boxes
	// with it.
	EditIssue(ctx context.Context, n int64, title, body string) error
}

// Restart is what RunForge.Restart did: Merged is whether it merged Base
// into the branch, after the final PR Final.
type Restart struct {
	Merged  bool
	Final   int64
	Base    string
	HeadSHA string
}

// Merge is a child's PR merged into the milestone branch.
type Merge struct {
	PR      int64
	HeadSHA string
	Base    string
}

// LandKind is how a child's landing ended, as the scheduler acts on it.
type LandKind int

const (
	// LandFailed is a refusal, or a base that kept moving: the child stops
	// as merge failed.
	LandFailed LandKind = iota
	// Landed: the PR merged into the milestone branch.
	Landed
	// LandRepair is land's Conflict or NeedsVerify: the child goes back to
	// its flow, once per invocation.
	LandRepair
	// LandChecksFailed: the checks failed at the landing commit.
	LandChecksFailed
)

// Landing is how one child's landing ended.
type Landing struct {
	Kind LandKind
	// Merge is what landed, for Landed.
	Merge Merge
	// Reason is the landing's outcome as land prints it, with the check
	// log for LandChecksFailed.
	Reason string
}

// FlowRequest asks a Launcher for one child's flow; Implement and Verify
// are ACTOR[:MODEL].
type FlowRequest struct {
	Issue     int64
	Implement string
	Verify    string
}

// OutcomeKind is how a flow ended, as the scheduler acts on it.
type OutcomeKind int

const (
	// FlowStopped is any stop that leaves the child to the owner, and a
	// flow that ended without a stop.
	FlowStopped OutcomeKind = iota
	// FlowReady: the flow stopped ready to merge.
	FlowReady
	// FlowIdle: the flow stopped because Actor's agent did nothing.
	FlowIdle
)

// Outcome is how one child's flow ended.
type Outcome struct {
	Kind OutcomeKind
	// Reason is the flow's stop reason, or what ended a flow without one.
	Reason string
	Detail string
	Actor  string // FlowIdle: the actor whose agent did nothing
}

// Launcher runs one child's flow to its end. It returns when the flow
// ends, or, its context cancelled, when the flow has stopped.
type Launcher interface {
	Flow(ctx context.Context, req FlowRequest) Outcome
}

// RunOptions configures Run.
type RunOptions struct {
	Slug  string
	Owner string // the owner's login, for trusted markers
	// Pool is the agents the children are paired from. Its removals last
	// this invocation only.
	Pool *agents.Pool
	// Parallel caps the children in flight.
	Parallel int
	Launcher Launcher
	// Finish is `bottown milestone finish`: it gates the branch and opens
	// or updates the final PR.
	Finish func(ctx context.Context) error
	Out    io.Writer
}

// RunReport is what one invocation of Run did and left.
type RunReport struct {
	Epic    int64
	Branch  string
	Merged  []MergedChild
	Stopped []StoppedChild
	// Blocked are open children with an open dependency, when Run ended.
	Blocked []BlockedChild
	// Unpaired are ready children for which the pool had no pair.
	Unpaired []int64
	// PoolRemoved are the entries removed after their agent did nothing.
	PoolRemoved []RemovedEntry
	Ignored     []*ForgeIssue
	Removed     []*ForgeIssue
	Finished    bool
}

// MergedChild is a child whose PR Run merged.
type MergedChild struct {
	Issue int64
	Merge
}

// StoppedChild is a child whose flow stopped short of a merge.
type StoppedChild struct {
	Issue  int64
	Reason string
	Detail string
}

// BlockedChild is an open child waiting on open dependencies.
type BlockedChild struct {
	Issue int64
	By    []int64
}

// RemovedEntry is a pool entry removed after its agent did nothing on
// Issue.
type RemovedEntry struct {
	Entry string
	Issue int64
}

// UnfinishedError is Run's error when nothing more could be dispatched and
// some active children are still open.
type UnfinishedError struct{ Report *RunReport }

func (e *UnfinishedError) Error() string {
	r := e.Report
	return fmt.Sprintf("milestone run stopped for epic #%d with children open: %d merged, %d stopped, %d blocked, %d unpaired; see the summary on #%d",
		r.Epic, len(r.Merged), len(r.Stopped), len(r.Blocked), len(r.Unpaired), r.Epic)
}

// AgentDidNothing reports whether the run lost a pool entry to an agent
// that did nothing.
func (e *UnfinishedError) AgentDidNothing() bool { return len(e.Report.PoolRemoved) > 0 }

// Run is `bottown milestone run`: it dispatches the epic's ready children
// in issue order, at most Parallel at a time, each as a flow with a
// cross-family pair from the pool; lands each child whose flow ends ready
// to merge (several ready together in land.Order's order), sending one
// whose landing conflicts or loses its approval back to its flow once;
// and, when every active child is closed, finishes the
// milestone. Otherwise, when nothing more can be dispatched, it comments a
// summary on the epic and returns an *UnfinishedError.
//
// Run keeps no state of its own: what it does is rebuilt from the forge on
// every call, so a stopped run can be run again.
func Run(ctx context.Context, f RunForge, o RunOptions) (*RunReport, error) {
	if o.Parallel < 1 {
		return nil, fmt.Errorf("parallel must be at least 1, not %d", o.Parallel)
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	st, err := Epic(ctx, f, o.Owner, o.Slug)
	if err != nil {
		return nil, err
	}
	rep := &RunReport{Epic: st.Epic.Number, Branch: "milestone/" + o.Slug}
	created, err := f.Branch(ctx)
	if err != nil {
		return rep, fmt.Errorf("creating %s: %w", rep.Branch, err)
	}
	if created {
		fmt.Fprintf(out, "created %s\n", rep.Branch)
	}
	rs, err := f.Restart(ctx)
	if err != nil {
		return rep, fmt.Errorf("starting the new round of %s from the default branch: %w", rep.Branch, err)
	}
	if rs.Merged {
		fmt.Fprintf(out, "the final PR !%d was merged: merged %s into %s at %s\n", rs.Final, rs.Base, rep.Branch, short(rs.HeadSHA))
	}

	type done struct {
		issue int64
		pair  agents.Pair
		out   Outcome
	}
	results := make(chan done)
	inflight := map[int64]agents.Pair{}
	live := map[string]int{}
	ended := map[int64]bool{}    // dispatched, and its flow ended, in this invocation
	repaired := map[int64]bool{} // sent back to its flow after a landing, in this invocation
	var ready []int64            // flows ended ready to merge, not landed yet, in the order they ended
	receive := func(d done) {
		delete(inflight, d.issue)
		live[d.pair.Implement.Actor]--
		live[d.pair.Verify.Actor]--
		ended[d.issue] = true
		if ctx.Err() != nil {
			return // drain the flows that are stopping
		}
		if d.out.Kind == FlowReady {
			ready = append(ready, d.issue)
			return
		}
		handle(o, rep, out, d.issue, d.out)
	}
	var loopErr error
	for {
		if loopErr == nil && ctx.Err() == nil {
			if st, err = Epic(ctx, f, o.Owner, o.Slug); err != nil {
				loopErr = err
			} else {
				tick(ctx, f, out, st)
			}
		}
		if loopErr == nil && ctx.Err() == nil {
			rep.Blocked, rep.Unpaired = nil, nil
			for _, c := range st.Open() {
				if _, ok := inflight[c.Number]; ok || ended[c.Number] {
					continue
				}
				by, err := openDeps(ctx, f, st, c.Number)
				if err != nil {
					loopErr = err
					break
				}
				if len(by) > 0 {
					rep.Blocked = append(rep.Blocked, BlockedChild{Issue: c.Number, By: by})
					continue
				}
				if len(inflight) >= o.Parallel {
					continue
				}
				pair, ok := pick(o.Pool, c, live)
				if !ok {
					rep.Unpaired = append(rep.Unpaired, c.Number)
					continue
				}
				if err := f.SetPair(ctx, c.Number, pair.Implement.Actor, pair.Verify.Actor); err != nil {
					ended[c.Number] = true
					rep.Stopped = append(rep.Stopped, StoppedChild{Issue: c.Number, Reason: "labelling failed", Detail: err.Error()})
					continue
				}
				inflight[c.Number] = pair
				live[pair.Implement.Actor]++
				live[pair.Verify.Actor]++
				fmt.Fprintf(out, "#%d: flow started: implement %s, verify %s\n", c.Number, pair.Implement, pair.Verify)
				req := FlowRequest{Issue: c.Number, Implement: pair.Implement.String(), Verify: pair.Verify.String()}
				go func() { results <- done{req.Issue, pair, o.Launcher.Flow(ctx, req)} }()
			}
		}
		if ctx.Err() != nil {
			ready = nil
		}
		if len(inflight) == 0 && len(ready) == 0 {
			break
		}
		if len(ready) == 0 {
			receive(<-results)
		}
		// Every flow that has ended by now is ready together with the
		// others (a resume), and lands in land.Order's order.
	drain:
		for {
			select {
			case d := <-results:
				receive(d)
			default:
				break drain
			}
		}
		if len(ready) == 0 || ctx.Err() != nil {
			continue
		}
		n := next(ctx, f, out, ready)
		ready = slices.DeleteFunc(ready, func(r int64) bool { return r == n })
		if land(ctx, f, rep, out, n, repaired) {
			// Back to its flow, with its recorded pair.
			delete(ended, n)
		}
	}
	if ctx.Err() != nil {
		return rep, ctx.Err()
	}
	if loopErr != nil {
		return rep, loopErr
	}
	rep.Ignored, rep.Removed = st.Ignored, st.Removed
	// A stop on a child closed since, a flow that found it merged, is no
	// stop.
	rep.Stopped = slices.DeleteFunc(rep.Stopped, func(s StoppedChild) bool {
		is := st.Issue(s.Issue)
		return is != nil && is.Closed
	})
	if len(st.Open()) == 0 {
		fmt.Fprintf(out, "every active child of epic #%d is closed: finishing %s\n", rep.Epic, rep.Branch)
		fmt.Fprint(out, summary(rep, st))
		if err := o.Finish(ctx); err != nil {
			return rep, err
		}
		rep.Finished = true
		return rep, nil
	}
	body := summary(rep, st)
	fmt.Fprint(out, body)
	if err := f.Comment(ctx, rep.Epic, body); err != nil {
		return rep, fmt.Errorf("commenting the summary on epic #%d: %w", rep.Epic, err)
	}
	return rep, &UnfinishedError{Report: rep}
}

// tick checks the boxes of the epic's checklist for its closed children,
// as apply renders them, so the epic shows what has landed without another
// apply. A failed edit is a warning: the next tick, or the next apply,
// checks them.
func tick(ctx context.Context, f RunForge, out io.Writer, st *EpicState) {
	var closed []int64
	for _, c := range st.Children {
		if c.Closed {
			closed = append(closed, c.Number)
		}
	}
	m, content, _ := ParseMarker(st.Epic.Body)
	content, ticked := tickBoxes(content, closed)
	if len(ticked) == 0 {
		return
	}
	// The marker's sha is the hash apply writes for this content, so the
	// next apply finds nothing to change in the epic.
	m.SHA = Hash(st.Epic.Title, content)
	body := withMarker(content, m)
	if err := f.EditIssue(ctx, st.Epic.Number, st.Epic.Title, body); err != nil {
		fmt.Fprintf(out, "warning: checking %s in epic #%d: %v\n", refs(ticked), st.Epic.Number, err)
		return
	}
	st.Epic.Body = body
	fmt.Fprintf(out, "checked %s in epic #%d\n", refs(ticked), st.Epic.Number)
}

// tickBoxes checks the boxes of the epic's checklist lines, as epicContent
// writes them, for the issues closed, and returns the body and the issues
// it checked. It leaves every other line as it is, and returns a body it
// checks nothing in unchanged.
func tickBoxes(body string, closed []int64) (string, []int64) {
	lines := strings.Split(normalize(body), "\n")
	var ticked []int64
	for i, l := range lines {
		rest, ok := strings.CutPrefix(l, "- [ ] #")
		if !ok || !checklistRE.MatchString(l) {
			continue
		}
		num, _, _ := strings.Cut(rest, " ")
		n, err := strconv.ParseInt(num, 10, 64)
		if err != nil || !slices.Contains(closed, n) {
			continue
		}
		lines[i] = "- [x] #" + rest
		ticked = append(ticked, n)
	}
	if len(ticked) == 0 {
		return body, nil
	}
	return strings.Join(lines, "\n"), ticked
}

// next is the first of the children ready to land, in land.Order's order
// when there are several; in the order their flows ended when that fails.
func next(ctx context.Context, f RunForge, out io.Writer, ready []int64) int64 {
	if len(ready) == 1 {
		return ready[0]
	}
	order, err := f.Order(ctx, slices.Clone(ready))
	if err == nil && (len(order) == 0 || !slices.Contains(ready, order[0])) {
		err = fmt.Errorf("the order %v is not of %v", order, ready)
	}
	if err != nil {
		fmt.Fprintf(out, "warning: ordering %s for landing: %v; landing them as their flows ended\n", refs(ready), err)
		return ready[0]
	}
	return order[0]
}

// land lands child n, whose flow ended ready to merge, and reports whether
// it goes back to its flow: a conflict, or an approval that did not carry
// to the landing commit, the first time in this invocation (repaired).
func land(ctx context.Context, f RunForge, rep *RunReport, out io.Writer, n int64, repaired map[int64]bool) bool {
	l := f.Land(ctx, n)
	switch l.Kind {
	case Landed:
		m := l.Merge
		rep.Merged = append(rep.Merged, MergedChild{Issue: n, Merge: m})
		fmt.Fprintf(out, "#%d: merged !%d into %s at %s\n", n, m.PR, m.Base, short(m.HeadSHA))
		final, err := f.FinalPR(ctx)
		if err == nil && final > 0 {
			err = f.Comment(ctx, final, fmt.Sprintf("**bottown milestone run:** merged !%d (#%d) into `%s` at `%s`, so this PR's head is past the commit `bottown milestone finish` gated: **the head has not been gated; do not merge it.** When every active child is closed, finish re-gates the head and updates this PR.",
				m.PR, n, rep.Branch, m.HeadSHA))
		}
		if err != nil {
			fmt.Fprintf(out, "warning: telling the final PR that its head is not gated: %v\n", err)
		}
		return false
	case LandRepair:
		if !repaired[n] {
			repaired[n] = true
			fmt.Fprintf(out, "#%d: ready to merge, but %s: back to its flow\n", n, l.Reason)
			return true
		}
	case LandChecksFailed:
		fmt.Fprintf(out, "#%d: ready to merge, but the checks failed at the landing commit: %s\n", n, l.Reason)
		rep.Stopped = append(rep.Stopped, StoppedChild{Issue: n, Reason: "checks failed", Detail: l.Reason})
		return false
	}
	fmt.Fprintf(out, "#%d: ready to merge, but the merge failed: %s\n", n, l.Reason)
	rep.Stopped = append(rep.Stopped, StoppedChild{Issue: n, Reason: "merge failed", Detail: l.Reason})
	return false
}

// handle acts on the end of child n's flow, short of ready to merge.
func handle(o RunOptions, rep *RunReport, out io.Writer, n int64, oc Outcome) {
	switch oc.Kind {
	case FlowIdle:
		entry := oc.Actor
		if e, ok := o.Pool.Lookup(oc.Actor); ok {
			entry = e.String()
		}
		if o.Pool.Remove(oc.Actor) {
			rep.PoolRemoved = append(rep.PoolRemoved, RemovedEntry{Entry: entry, Issue: n})
			fmt.Fprintf(out, "#%d: %s did nothing: removed it from the pool for this run\n", n, entry)
		}
		rep.Stopped = append(rep.Stopped, StoppedChild{Issue: n, Reason: oc.Reason, Detail: oc.Detail})
	default:
		fmt.Fprintf(out, "#%d: flow stopped: %s\n", n, oc.Reason)
		rep.Stopped = append(rep.Stopped, StoppedChild{Issue: n, Reason: oc.Reason, Detail: oc.Detail})
	}
}

// openDeps lists child n's dependencies that are not closed: open, or
// unknown to the issue list.
func openDeps(ctx context.Context, f RunForge, st *EpicState, n int64) ([]int64, error) {
	deps, err := f.BlockedBy(ctx, n)
	if err != nil {
		return nil, fmt.Errorf("fetching #%d's dependencies: %w", n, err)
	}
	var by []int64
	for _, d := range deps {
		if is := st.Issue(d); is == nil || !is.Closed {
			by = append(by, d)
		}
	}
	slices.Sort(by)
	return slices.Compact(by), nil
}

// pick is child c's pair: the one its labels record while both entries
// are in the pool, of different families, waiting for their capacity;
// otherwise the pool's next.
func pick(pool *agents.Pool, c *ForgeIssue, live map[string]int) (agents.Pair, bool) {
	var implement, verify string
	for _, l := range c.Labels {
		if a, ok := strings.CutPrefix(l, "implement/"); ok {
			implement = a
		}
		if a, ok := strings.CutPrefix(l, "verify/"); ok {
			verify = a
		}
	}
	i, iok := pool.Lookup(implement)
	v, vok := pool.Lookup(verify)
	if iok && vok && i.Family != v.Family {
		if pool.Spare(implement, live) && pool.Spare(verify, live) {
			return agents.Pair{Implement: i, Verify: v}, true
		}
		return agents.Pair{}, false
	}
	return pool.Next(live)
}

// summary is the run's summary, as the epic's comment and the output.
func summary(rep *RunReport, st *EpicState) string {
	var b strings.Builder
	closed := len(st.Children) - len(st.Open())
	if rep.Finished || len(st.Open()) == 0 {
		fmt.Fprintf(&b, "**bottown milestone run:** every active child of `%s` is closed (%d).\n", rep.Branch, closed)
	} else {
		fmt.Fprintf(&b, "**bottown milestone run:** stopped with %d of %d active children closed: nothing more can be dispatched on `%s`.\n", closed, len(st.Children), rep.Branch)
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n\n", title)
		for _, l := range lines {
			b.WriteString("- " + l + "\n")
		}
	}
	var lines []string
	for _, m := range rep.Merged {
		lines = append(lines, fmt.Sprintf("#%d: !%d at `%s`", m.Issue, m.PR, short(m.HeadSHA)))
	}
	section("Merged", lines)
	lines = nil
	for _, s := range rep.Stopped {
		l := fmt.Sprintf("#%d: %s", s.Issue, s.Reason)
		if s.Detail != "" {
			l += " (" + s.Detail + ")"
		}
		lines = append(lines, l)
	}
	section("Stopped", lines)
	lines = nil
	for _, bl := range rep.Blocked {
		lines = append(lines, fmt.Sprintf("#%d: by %s", bl.Issue, refs(bl.By)))
	}
	section("Blocked", lines)
	lines = nil
	for _, n := range rep.Unpaired {
		lines = append(lines, fmt.Sprintf("#%d", n))
	}
	section("Ready, but the pool has no pair for them", lines)
	lines = nil
	for _, r := range rep.PoolRemoved {
		lines = append(lines, fmt.Sprintf("%s: its agent did nothing on #%d; back in the pool on the next run", r.Entry, r.Issue))
	}
	section("Removed from the pool", lines)
	lines = nil
	for _, is := range st.Ignored {
		lines = append(lines, issueLine(is))
	}
	section("Ignored (they block the epic, but carry no child marker of the plan)", lines)
	lines = nil
	for _, is := range st.Removed {
		lines = append(lines, issueLine(is))
	}
	section("Flagged (removed from the plan; not dispatched, not required to finish)", lines)
	return b.String()
}

func issueLine(is *ForgeIssue) string {
	l := fmt.Sprintf("#%d", is.Number)
	if is.Title != "" {
		l += " " + is.Title
	}
	if is.Closed {
		l += " (closed)"
	}
	return l
}

func refs(ns []int64) string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(out, ", ")
}
