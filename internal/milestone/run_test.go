package milestone

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/agents"
	"github.com/robertolupi/bottown/internal/config"
)

var runActors = []config.Actor{
	{Name: "claude", Family: "anthropic"},
	{Name: "agy", Family: "google"},
	{Name: "kimi", Family: "moonshot"},
}

// runFake is RunForge over the apply fake, with a fake Land: landing
// closes the child, unless landings holds the outcomes of its next
// landings.
type runFake struct {
	*fakeForge
	events   []string // "branch", "restart", "pair #n a/b", "land #n", "merge #n", "comment #n"
	comments map[int64][]string
	final    int64 // the open final PR
	restart  Restart
	// landings are each child's next landings' outcomes, the first one
	// first; Landed, or none left, merges it.
	landings map[int64][]Landing
	// onLand runs before each landing.
	onLand func(n int64)
	// order is Order's answer for ns; nil keeps ns.
	order  func(ns []int64) []int64
	orders [][]int64 // what Order was asked
}

func newRunFake(f *fakeForge) *runFake {
	return &runFake{fakeForge: f, comments: map[int64][]string{}, landings: map[int64][]Landing{}}
}

func (f *runFake) Branch(context.Context) (bool, error) {
	f.events = append(f.events, "branch")
	return true, nil
}

func (f *runFake) Restart(context.Context) (Restart, error) {
	f.events = append(f.events, "restart")
	return f.restart, nil
}

func (f *runFake) SetPair(_ context.Context, n int64, implement, verify string) error {
	is := f.issues[n]
	is.Labels = slices.DeleteFunc(is.Labels, func(l string) bool {
		return strings.HasPrefix(l, "implement/") || strings.HasPrefix(l, "verify/")
	})
	is.Labels = append(is.Labels, "implement/"+implement, "verify/"+verify)
	f.events = append(f.events, fmt.Sprintf("pair #%d %s/%s", n, implement, verify))
	return nil
}

func (f *runFake) Land(_ context.Context, n int64) Landing {
	if f.onLand != nil {
		f.onLand(n)
	}
	f.events = append(f.events, fmt.Sprintf("land #%d", n))
	if q := f.landings[n]; len(q) > 0 {
		f.landings[n] = q[1:]
		if q[0].Kind != Landed {
			return q[0]
		}
	}
	f.issues[n].Closed = true
	f.events = append(f.events, fmt.Sprintf("merge #%d", n))
	return Landing{Kind: Landed, Merge: Merge{PR: 100 + n, HeadSHA: "abcdef0123456789", Base: "milestone/demo"}}
}

func (f *runFake) Order(_ context.Context, ns []int64) ([]int64, error) {
	f.orders = append(f.orders, slices.Clone(ns))
	if f.order == nil {
		return ns, nil
	}
	return f.order(ns), nil
}

// lands are the children landed, in the order Land was asked.
func (f *runFake) lands() []int64 {
	var out []int64
	for _, e := range f.events {
		var n int64
		if _, err := fmt.Sscanf(e, "land #%d", &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (f *runFake) FinalPR(context.Context) (int64, error) { return f.final, nil }

func (f *runFake) Comment(_ context.Context, n int64, body string) error {
	f.comments[n] = append(f.comments[n], body)
	f.events = append(f.events, fmt.Sprintf("comment #%d", n))
	return nil
}

// fakeLauncher runs no flow: each child's flow ends with outcome(issue),
// ready to merge by default, after a short wait so flows overlap.
type fakeLauncher struct {
	mu       sync.Mutex
	outcome  func(FlowRequest) Outcome
	requests []FlowRequest
	running  int
	most     int
	returned int // flows that have ended
}

func (l *fakeLauncher) Flow(_ context.Context, req FlowRequest) Outcome {
	l.mu.Lock()
	l.requests = append(l.requests, req)
	l.running++
	l.most = max(l.most, l.running)
	l.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running--
	l.returned++
	if l.outcome != nil {
		return l.outcome(req)
	}
	return Outcome{Kind: FlowReady, Reason: "ready to merge"}
}

func (l *fakeLauncher) issues() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []int64
	for _, r := range l.requests {
		out = append(out, r.Issue)
	}
	return out
}

type runResult struct {
	rep      *RunReport
	err      error
	out      string
	finished int
}

func runOnce(t *testing.T, f *runFake, l *fakeLauncher, spec string, parallel int) runResult {
	t.Helper()
	pool, err := agents.Parse(spec, runActors)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	var r runResult
	r.rep, r.err = Run(context.Background(), f, RunOptions{Slug: "demo", Owner: owner, Pool: pool, Parallel: parallel, Launcher: l, Out: &out,
		Finish: func(context.Context) error { r.finished++; return nil }})
	r.out = out.String()
	return r
}

// planIndependent has three children with no dependencies: #3, #4, #5,
// and the epic #6.
const planIndependent = `+++
title = "Demo epic"
kickoff = 1
+++

## Alpha

'''issue
key = "a"
'''

Body of a.

## Bravo

'''issue
key = "b"
'''

Body of b.

## Charlie

'''issue
key = "c"
'''

Body of c.
`

// Children are dispatched in issue order once their blocked_by issues are
// closed; each merge unblocks the next, and the last one finishes.
func TestRunReadinessAndFinish(t *testing.T) {
	f := newRunFake(withPlan(plan1)) // a #3, b #4 (a, #2), c #5 (b), d #6 (c), epic #7
	mustApply(t, f.fakeForge, nil)
	l := &fakeLauncher{}

	// #2 is open: b waits on it, and c and d on b.
	r := runOnce(t, f, l, "claude=*,agy=*", 4)
	var unfinished *UnfinishedError
	if !errors.As(r.err, &unfinished) || r.finished != 0 {
		t.Fatalf("err = %v, finished %d\n%s", r.err, r.finished, r.out)
	}
	if got := l.issues(); !slices.Equal(got, []int64{3}) {
		t.Fatalf("dispatched %v, want #3 alone", got)
	}
	want := []BlockedChild{{Issue: 4, By: []int64{2}}, {Issue: 5, By: []int64{4}}, {Issue: 6, By: []int64{5}}}
	if !slices.EqualFunc(r.rep.Blocked, want, func(a, b BlockedChild) bool { return a.Issue == b.Issue && slices.Equal(a.By, b.By) }) {
		t.Errorf("blocked %+v", r.rep.Blocked)
	}
	summary := strings.Join(f.comments[7], "")
	for _, s := range []string{"1 of 4 active children closed", "Merged:\n\n- #3: !103", "Blocked:\n\n- #4: by #2\n- #5: by #4\n- #6: by #5\n"} {
		if !strings.Contains(summary, s) {
			t.Errorf("summary lacks %q:\n%s", s, summary)
		}
	}
	if f.events[0] != "branch" || f.events[1] != "restart" {
		t.Errorf("events %v: the branch and the restart come first", f.events)
	}

	// #2 closed: the rest run one after the other, and finish follows.
	f.issues[2].Closed = true
	l2 := &fakeLauncher{}
	r = runOnce(t, f, l2, "claude=*,agy=*", 4)
	if r.err != nil || r.finished != 1 || !r.rep.Finished {
		t.Fatalf("err = %v, finished %d\n%s", r.err, r.finished, r.out)
	}
	if got := l2.issues(); !slices.Equal(got, []int64{4, 5, 6}) {
		t.Errorf("dispatched %v, want #4 #5 #6", got)
	}
	if len(f.comments[7]) != 1 {
		t.Errorf("finishing commented a summary on the epic: %q", f.comments[7])
	}
}

// --parallel caps the flows in flight; pairs are cross-family and
// round-robin.
func TestRunParallelAndPairs(t *testing.T) {
	for _, parallel := range []int{1, 2} {
		t.Run(fmt.Sprint(parallel), func(t *testing.T) {
			f := newRunFake(withPlan(planIndependent))
			mustApply(t, f.fakeForge, nil)
			l := &fakeLauncher{}
			r := runOnce(t, f, l, "claude=*,agy=*,kimi=*", parallel)
			if r.err != nil || r.finished != 1 {
				t.Fatalf("err = %v\n%s", r.err, r.out)
			}
			if l.most != parallel {
				t.Errorf("at most %d flows at once, want %d", l.most, parallel)
			}
			family := map[string]string{"claude": "anthropic", "agy": "google", "kimi": "moonshot"}
			for _, req := range l.requests {
				if family[req.Implement] == family[req.Verify] {
					t.Errorf("#%d paired %s with %s", req.Issue, req.Implement, req.Verify)
				}
			}
			got := map[int64]string{}
			for _, req := range l.requests {
				got[req.Issue] = req.Implement + "/" + req.Verify
			}
			if want := map[int64]string{3: "claude/agy", 4: "agy/kimi", 5: "kimi/claude"}; fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("pairs %v, want %v", got, want)
			}
			if !slices.Contains(f.issues[4].Labels, "implement/agy") || !slices.Contains(f.issues[4].Labels, "verify/kimi") {
				t.Errorf("#4 labels %v", f.issues[4].Labels)
			}
		})
	}
}

// An entry's limit counts the flows it is paired in: with claude=1, only
// one flow runs at a time, however high --parallel.
func TestRunEntryLimit(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	l := &fakeLauncher{}
	if r := runOnce(t, f, l, "claude=1,agy=*", 3); r.err != nil {
		t.Fatalf("err = %v\n%s", r.err, r.out)
	}
	if l.most != 1 {
		t.Errorf("at most %d flows at once with claude=1 the only anthropic entry", l.most)
	}
}

// A child's labelled pair is kept while both entries are in the pool;
// one naming an actor outside the pool is replaced, and the model pin
// comes from the pool.
func TestRunKeepsPairs(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	f.issues[3].Labels = []string{"implement/kimi", "verify/claude"}
	f.issues[4].Labels = []string{"implement/codex", "verify/claude"}
	l := &fakeLauncher{}
	if r := runOnce(t, f, l, "claude=*,agy:fast=*,kimi=*", 1); r.err != nil {
		t.Fatalf("err = %v\n%s", r.err, r.out)
	}
	got := map[int64]string{}
	for _, req := range l.requests {
		got[req.Issue] = req.Implement + "/" + req.Verify
	}
	if got[3] != "kimi/claude" || got[4] != "claude/agy:fast" {
		t.Errorf("pairs %v", got)
	}
}

// An agent that did nothing loses its entry for the rest of the
// invocation, its child stays stopped, and the others continue; on the
// next invocation the entry is back.
func TestRunAgentDidNothing(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	l := &fakeLauncher{outcome: func(req FlowRequest) Outcome {
		if req.Issue == 3 {
			return Outcome{Kind: FlowIdle, Reason: "agent did nothing", Detail: "the implement run r-1 by claude failed without a tool call", Actor: "claude"}
		}
		return Outcome{Kind: FlowReady}
	}}
	r := runOnce(t, f, l, "claude=*,agy=*,kimi=*", 1)
	var unfinished *UnfinishedError
	if !errors.As(r.err, &unfinished) || !unfinished.AgentDidNothing() {
		t.Fatalf("err = %v\n%s", r.err, r.out)
	}
	if got := l.issues(); !slices.Equal(got, []int64{3, 4, 5}) {
		t.Errorf("dispatched %v: #3 once, then the others", got)
	}
	for _, req := range l.requests[1:] {
		if req.Implement == "claude" || req.Verify == "claude" {
			t.Errorf("#%d paired with the removed claude: %+v", req.Issue, req)
		}
	}
	if !f.issues[4].Closed || !f.issues[5].Closed || f.issues[3].Closed {
		t.Errorf("closed: #3 %v #4 %v #5 %v", f.issues[3].Closed, f.issues[4].Closed, f.issues[5].Closed)
	}
	summary := f.comments[6][0]
	for _, s := range []string{"Stopped:\n\n- #3: agent did nothing (the implement run r-1 by claude", "Removed from the pool:\n\n- claude: its agent did nothing on #3"} {
		if !strings.Contains(summary, s) {
			t.Errorf("summary lacks %q:\n%s", s, summary)
		}
	}

	// The next invocation has claude back, and #3 keeps its pair.
	l2 := &fakeLauncher{}
	r = runOnce(t, f, l2, "claude=*,agy=*,kimi=*", 1)
	if r.err != nil || r.finished != 1 {
		t.Fatalf("rerun: err = %v\n%s", r.err, r.out)
	}
	if len(l2.requests) != 1 || l2.requests[0] != (FlowRequest{Issue: 3, Implement: "claude", Verify: "agy"}) {
		t.Errorf("rerun dispatched %+v", l2.requests)
	}
}

// A rerun after apply added a child dispatches the new child and the
// stopped one alone.
func TestRunRerunAfterApply(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	l := &fakeLauncher{outcome: func(req FlowRequest) Outcome {
		if req.Issue == 4 {
			return Outcome{Kind: FlowStopped, Reason: "out of rounds"}
		}
		return Outcome{Kind: FlowReady}
	}}
	r := runOnce(t, f, l, "claude=*,agy=*", 2)
	if !strings.Contains(strings.Join(f.comments[6], ""), "Stopped:\n\n- #4: out of rounds\n") {
		t.Errorf("err = %v; summary %q", r.err, f.comments[6])
	}
	f.files[planFile] += string(md("\n## Delta\n\n'''issue\nkey = \"d\"\ndepends = [\"a\"]\n'''\n\nBody of d.\n"))
	mustApply(t, f.fakeForge, nil) // d is #7
	l2 := &fakeLauncher{}
	r = runOnce(t, f, l2, "claude=*,agy=*", 2)
	if r.err != nil || r.finished != 1 {
		t.Fatalf("rerun: err = %v\n%s", r.err, r.out)
	}
	got := l2.issues()
	slices.Sort(got)
	if !slices.Equal(got, []int64{4, 7}) {
		t.Errorf("rerun dispatched %v, want #4 and #7", got)
	}
}

// An unmarked issue linked to the epic is ignored and reported; an issue
// whose key the plan dropped is flagged, neither dispatched nor needed to
// finish.
func TestRunIgnoredAndRemoved(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	plain := f.add(&ForgeIssue{Title: "Linked by hand", Poster: owner}) // #7
	f.deps[6] = append(f.deps[6], plain)
	f.files[planFile] = string(md(strings.Replace(planIndependent, "## Charlie\n\n'''issue\nkey = \"c\"\n'''\n\nBody of c.\n", "", 1)))
	mustApply(t, f.fakeForge, nil)
	if f.issues[5].Closed {
		t.Fatal("apply closed the dropped key's issue")
	}
	l := &fakeLauncher{}
	r := runOnce(t, f, l, "claude=*,agy=*", 2)
	if r.err != nil || r.finished != 1 {
		t.Fatalf("err = %v\n%s", r.err, r.out)
	}
	got := l.issues()
	slices.Sort(got)
	if !slices.Equal(got, []int64{3, 4}) {
		t.Errorf("dispatched %v, want #3 #4", got)
	}
	if !slices.Equal(numbers(r.rep.Ignored), []int64{7}) || !slices.Equal(numbers(r.rep.Removed), []int64{5}) {
		t.Errorf("ignored %v, removed %v", numbers(r.rep.Ignored), numbers(r.rep.Removed))
	}
	for _, s := range []string{"Ignored (they block the epic, but carry no child marker of the plan):\n\n- #7 Linked by hand\n", "Flagged (removed from the plan; not dispatched, not required to finish):\n\n- #5 Charlie\n"} {
		if !strings.Contains(r.out, s) {
			t.Errorf("summary lacks %q:\n%s", s, r.out)
		}
	}
}

// A merge with a final PR open says on it that its head is not gated.
func TestRunMergeWithOpenFinalPR(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	f.final = 50
	if r := runOnce(t, f, &fakeLauncher{}, "claude=*,agy=*", 1); r.err != nil {
		t.Fatalf("err = %v\n%s", r.err, r.out)
	}
	if len(f.comments[50]) != 3 || !strings.Contains(f.comments[50][0], "merged !103 (#3) into `milestone/demo`") || !strings.Contains(f.comments[50][0], "has not been gated") {
		t.Errorf("final PR comments %q", f.comments[50])
	}
}

// A run after the final PR merged starts from the default branch: the
// restart comes before any flow. A failed merge leaves the child stopped.
func TestRunRestartAndFailedMerge(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	f.restart = Restart{Merged: true, Final: 40, Base: "main", HeadSHA: "0123456789abcdef"}
	f.landings[4] = []Landing{{Kind: LandFailed, Reason: "!104 at abcdef0: refused: PR must remain open, approved, synced and on its original base and head branch"}}
	r := runOnce(t, f, &fakeLauncher{}, "claude=*,agy=*", 1)
	if !strings.Contains(r.out, "the final PR !40 was merged: merged main into milestone/demo") {
		t.Errorf("output:\n%s", r.out)
	}
	if i := slices.Index(f.events, "restart"); i < 0 || i > slices.IndexFunc(f.events, func(e string) bool { return strings.HasPrefix(e, "pair") }) {
		t.Errorf("events %v: restart before the first dispatch", f.events)
	}
	if len(r.rep.Stopped) != 1 || r.rep.Stopped[0].Issue != 4 || r.rep.Stopped[0].Reason != "merge failed" {
		t.Errorf("stopped %+v", r.rep.Stopped)
	}
}

func TestRunRefusals(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	if r := runOnce(t, f, &fakeLauncher{}, "claude=*,agy=*", 1); !errors.Is(r.err, ErrNotFound) || len(f.events) != 0 {
		t.Errorf("no epic: %v, events %v", r.err, f.events)
	}
	mustApply(t, f.fakeForge, nil)
	if r := runOnce(t, f, &fakeLauncher{}, "claude=*,agy=*", 0); r.err == nil || len(f.events) != 0 {
		t.Errorf("parallel 0: %v, events %v", r.err, f.events)
	}
}

// An approved child whose landing conflicts after a sibling landed goes
// back to its flow once, with its recorded pair: the flow runs an
// implement round and then a verify run, and the child lands.
func TestRunConflictRedispatchedOnce(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	f.landings[4] = []Landing{{Kind: LandRepair, Reason: "!104 at abcdef0: conflict: x.txt"}}
	var mu sync.Mutex
	var repair []string // the second flow's runs on #4
	flows := map[int64]int{}
	l := &fakeLauncher{outcome: func(req FlowRequest) Outcome {
		mu.Lock()
		defer mu.Unlock()
		flows[req.Issue]++
		if req.Issue == 4 && flows[4] == 2 {
			repair = append(repair, "implement "+req.Implement, "verify "+req.Verify)
		}
		return Outcome{Kind: FlowReady}
	}}
	f.onLand = func(n int64) {
		mu.Lock()
		defer mu.Unlock()
		if n == 4 && len(f.landings[4]) == 0 && len(repair) == 0 {
			t.Error("#4 landed again before its flow ran")
		}
	}
	r := runOnce(t, f, l, "claude=*,agy=*", 1)
	if r.err != nil || r.finished != 1 {
		t.Fatalf("err = %v, finished %d\n%s", r.err, r.finished, r.out)
	}
	if got := l.issues(); !slices.Equal(got, []int64{3, 4, 4, 5}) {
		t.Errorf("dispatched %v, want #4 twice", got)
	}
	if l.requests[1] != l.requests[2] {
		t.Errorf("#4 went back with another pair: %+v, then %+v", l.requests[1], l.requests[2])
	}
	if want := []string{"implement " + l.requests[1].Implement, "verify " + l.requests[1].Verify}; !slices.Equal(repair, want) {
		t.Errorf("#4's second flow ran %v, want %v", repair, want)
	}
	if got := f.lands(); !slices.Equal(got, []int64{3, 4, 4, 5}) {
		t.Errorf("landed %v", got)
	}
	if !strings.Contains(r.out, "#4: ready to merge, but !104 at abcdef0: conflict: x.txt: back to its flow") {
		t.Errorf("output:\n%s", r.out)
	}
	if len(r.rep.Stopped) != 0 || len(r.rep.Merged) != 3 {
		t.Errorf("stopped %+v, merged %+v", r.rep.Stopped, r.rep.Merged)
	}
}

// A child whose landing conflicts, or loses its approval, again after its
// flow repaired it stops as merge failed, with the reason; the others
// land.
func TestRunConflictAgainStops(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	f.landings[4] = []Landing{
		{Kind: LandRepair, Reason: "!104 at abcdef0: conflict: x.txt"},
		{Kind: LandRepair, Reason: "!104 at 1234567: needs verify: approval did not carry"},
	}
	l := &fakeLauncher{}
	r := runOnce(t, f, l, "claude=*,agy=*", 1)
	var unfinished *UnfinishedError
	if !errors.As(r.err, &unfinished) {
		t.Fatalf("err = %v\n%s", r.err, r.out)
	}
	if got := l.issues(); !slices.Equal(got, []int64{3, 4, 4, 5}) {
		t.Errorf("dispatched %v, want #4 twice and no more", got)
	}
	want := []StoppedChild{{Issue: 4, Reason: "merge failed", Detail: "!104 at 1234567: needs verify: approval did not carry"}}
	if !slices.Equal(r.rep.Stopped, want) {
		t.Errorf("stopped %+v, want %+v", r.rep.Stopped, want)
	}
	if !f.issues[3].Closed || f.issues[4].Closed || !f.issues[5].Closed {
		t.Errorf("closed: #3 %v #4 %v #5 %v", f.issues[3].Closed, f.issues[4].Closed, f.issues[5].Closed)
	}
	if !strings.Contains(f.comments[6][0], "- #4: merge failed (!104 at 1234567: needs verify") {
		t.Errorf("summary:\n%s", f.comments[6][0])
	}
}

// Failed checks at the landing commit stop the child at once, with the
// log, for the owner.
func TestRunChecksFailedStops(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	reason := "!104 at 1234567: checks failed: make check failed; log: /runs/flow-4-1234567.check.log"
	f.landings[4] = []Landing{{Kind: LandChecksFailed, Reason: reason}}
	l := &fakeLauncher{}
	r := runOnce(t, f, l, "claude=*,agy=*", 1)
	var unfinished *UnfinishedError
	if !errors.As(r.err, &unfinished) {
		t.Fatalf("err = %v\n%s", r.err, r.out)
	}
	if got := l.issues(); !slices.Equal(got, []int64{3, 4, 5}) {
		t.Errorf("dispatched %v, want #4 once", got)
	}
	want := []StoppedChild{{Issue: 4, Reason: "checks failed", Detail: reason}}
	if !slices.Equal(r.rep.Stopped, want) {
		t.Errorf("stopped %+v, want %+v", r.rep.Stopped, want)
	}
	if !strings.Contains(f.comments[6][0], "- #4: checks failed ("+reason+")") {
		t.Errorf("summary:\n%s", f.comments[6][0])
	}
}

// Children ready together (a resume) land in Order's order, recomputed
// before each landing.
func TestRunReadyTogetherLandInOrder(t *testing.T) {
	f := newRunFake(withPlan(planIndependent))
	mustApply(t, f.fakeForge, nil)
	l := &fakeLauncher{}
	// The first landing waits for every flow to end, so the others are
	// ready together by the next one.
	f.onLand = func(int64) {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			l.mu.Lock()
			n := l.returned
			l.mu.Unlock()
			if n == 3 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Highest issue first: not the order the flows ended in.
	f.order = func(ns []int64) []int64 {
		out := slices.Clone(ns)
		slices.Sort(out)
		slices.Reverse(out)
		return out
	}
	r := runOnce(t, f, l, "claude=*,agy=*,kimi=*", 3)
	if r.err != nil || r.finished != 1 {
		t.Fatalf("err = %v\n%s", r.err, r.out)
	}
	got := f.lands()
	if len(got) != 3 || got[1] < got[2] {
		t.Fatalf("landed %v: after the first, the others in Order's order", got)
	}
	if len(f.orders) == 0 {
		t.Fatal("Order was never asked")
	}
	last := f.orders[len(f.orders)-1]
	slices.Sort(last)
	rest := slices.Clone(got[1:])
	slices.Sort(rest)
	if !slices.Equal(last, rest) {
		t.Errorf("the last order was of %v, want the two left %v", last, rest)
	}
}
