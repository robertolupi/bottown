// Package activity derives what happened in a repository over a time
// window from the server's run records and the forge: runs and the flow
// threads they group into, the flow's post-implement checks, its stops and
// the time each stop left a thread waiting on the owner, and which runs
// reported their usage. The web UI's timeline and statistics both read
// Load, so they cannot disagree; the browser shows what it returns and
// derives none of it.
//
// Nothing here is a store of its own: every fact is read again from the
// forge and the server's run records on each Load.
package activity

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// Window is the time range [Since, Until); a zero Until is now.
type Window struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

// Activity is what Load derives for a window.
type Activity struct {
	Window Window `json:"window"`
	// Runs are the runs that overlap the window, purged ones included,
	// oldest first.
	Runs []Run `json:"runs"`
	// Threads are the flow threads with something in the window: a run
	// that overlaps it, or a PR created, updated, merged or closed in it,
	// or still open. Standalone runs (discuss, retro) are not threads.
	Threads []Thread `json:"threads"`
	// Usage is how many of Runs reported their usage.
	Usage Coverage `json:"usage"`
	// Incomplete names the reads that failed, whose facts are missing.
	Incomplete []string `json:"incomplete,omitempty"`
}

// Run is one run, with its thread.
type Run struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Actor     string     `json:"actor"`
	Family    string     `json:"family,omitempty"`
	Harness   string     `json:"harness,omitempty"`
	Spec      string     `json:"spec,omitempty"`
	State     string     `json:"state"`
	Status    string     `json:"status,omitempty"`
	Outcome   string     `json:"outcome,omitempty"`
	Created   time.Time  `json:"created"`
	Ended     *time.Time `json:"ended,omitempty"`
	Purged    bool       `json:"purged"`
	Issue     int64      `json:"issue,omitempty"`
	PR        int64      `json:"pr,omitempty"`
	ThreadKey string     `json:"thread_key"`
	// Usage is what the harness reported, nil when it reported nothing;
	// a figure it did not report is nil, never zero.
	Usage *secrets.Usage `json:"usage,omitempty"`
	// UsageReported: the harness reported tokens or a cost.
	UsageReported bool `json:"usage_reported"`
}

// Coverage counts the runs that reported usage, for "n of m runs
// reported".
type Coverage struct {
	Runs     int `json:"runs"`
	Reported int `json:"reported"`
}

// CoverageOf is the usage coverage of runs.
func CoverageOf(runs []Run) Coverage {
	c := Coverage{Runs: len(runs)}
	for _, r := range runs {
		if r.UsageReported {
			c.Reported++
		}
	}
	return c
}

// usageReported is whether u has tokens or a cost.
func usageReported(u *secrets.Usage) bool {
	return u != nil && (u.InputTokens != nil || u.OutputTokens != nil || u.CacheReadTokens != nil || u.CacheWriteTokens != nil || u.CostUSD != nil)
}

// Thread is one flow thread: an issue's work on one PR, or before any.
type Thread struct {
	Key       string     `json:"key"`
	Issue     int64      `json:"issue,omitempty"`
	PR        int64      `json:"pr,omitempty"`
	PRCreated *time.Time `json:"pr_created,omitempty"`
	PRMerged  *time.Time `json:"pr_merged,omitempty"`
	PRClosed  *time.Time `json:"pr_closed,omitempty"`
	// Runs are the ids of the thread's runs in Activity.Runs, oldest
	// first.
	Runs []string `json:"runs"`
	// ImplementRounds counts the PR's REQUEST_CHANGES reviews, and
	// CheckRounds its commits whose flow check failed, as the flow counts
	// its rounds.
	ImplementRounds int     `json:"implement_rounds"`
	CheckRounds     int     `json:"check_rounds"`
	Checks          []Check `json:"checks"`
	Stops           []Stop  `json:"stops"`
	// OwnerWaits are the times the flow's stops left the thread waiting
	// on the owner.
	OwnerWaits []OwnerWait `json:"owner_waits"`
}

// Check is one run of the flow's post-implement checks, from its
// bottown/e2e status. The flow records a status only when the checks end,
// so End is the status's time, and Start is End less the duration the
// description gives: derived, and nil when the description gives none.
type Check struct {
	SHA         string     `json:"sha"`
	State       string     `json:"state"` // success or failure
	Description string     `json:"description,omitempty"`
	Start       *time.Time `json:"start,omitempty"`
	End         time.Time  `json:"end"`
	Derived     bool       `json:"derived"` // Start was derived from the description
}

// Stop is one of the flow's stop comments on the thread's issue.
type Stop struct {
	Time    time.Time       `json:"time"`
	Reason  flow.StopReason `json:"reason"`
	Comment int64           `json:"comment_id"`
}

// What ended an owner wait.
const (
	EndedByRun   = "run"   // the thread's next run started
	EndedByMerge = "merge" // the PR was merged
	EndedByClose = "close" // the PR was closed
	EndedByStop  = "stop"  // the flow stopped again, with no run between
)

// OwnerWait is the time from a flow stop until the thread's next run, its
// merge or its close, whichever came first; or its next stop, which starts
// the next wait. End is nil while the thread still waits.
type OwnerWait struct {
	Start   time.Time       `json:"start"`
	End     *time.Time      `json:"end,omitempty"`
	EndedBy string          `json:"ended_by,omitempty"`
	Reason  flow.StopReason `json:"reason"`
}

// Source is what Load reads: the server's API as the owner.
type Source interface {
	// Runs are the repository's runs, purged ones included
	// (GET /v1/runs?history=1).
	Runs(ctx context.Context) ([]server.RunItem, error)
	// PullRequests are the repository's PRs in every state
	// (GET /v1/forge/prs?state=all).
	PullRequests(ctx context.Context) ([]*server.PRItem, error)
	// PullRequest is PR n with its reviews (GET /v1/forge/pr).
	PullRequest(ctx context.Context, n int64) (*server.PROut, error)
	// Gate is the flow's gate on PR n (GET /v1/forge/pr/gate).
	Gate(ctx context.Context, n int64) (server.GateOut, error)
	// Issue is issue n with its comments (GET /v1/forge/issue).
	Issue(ctx context.Context, n int64) (*server.IssueOut, error)
}

// Facts are what Derive works from, as Load reads them.
type Facts struct {
	Runs []server.RunItem
	PRs  []*server.PRItem
	// By number, for the threads in the window: each issue with its
	// comments, each PR with its reviews, and each PR's gate.
	Issues  map[int64]*server.IssueOut
	Reviews map[int64]*server.PROut
	Gates   map[int64]server.GateOut
}

// Load reads the facts for w from src and derives its activity.
//
// Its reads are bounded by the threads in the window, whatever its length:
// one run list (the server's own records, all of them, so threads group as
// History groups them and a wait that began before the window ends where
// it did), one PR list, and then one issue read (its comments) per issue
// and one PR read and one gate read per PR of those threads. For 7 days
// that is 2 + issues + 2 × PRs reads, each issue and PR read once. A read
// that fails leaves its facts out and is named in Incomplete.
func Load(ctx context.Context, src Source, w Window, now time.Time) (*Activity, error) {
	w = w.resolve(now)
	runs, err := src.Runs(ctx)
	if err != nil {
		return nil, fmt.Errorf("read runs: %w", err)
	}
	prs, err := src.PullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("read pull requests: %w", err)
	}
	f := Facts{Runs: runs, PRs: prs, Issues: map[int64]*server.IssueOut{}, Reviews: map[int64]*server.PROut{}, Gates: map[int64]server.GateOut{}}
	var incomplete []string
	for _, t := range windowThreads(w, Group(runs, prs), runs, prs) {
		if t.Issue > 0 && f.Issues[t.Issue] == nil {
			is, err := src.Issue(ctx, t.Issue)
			if err != nil {
				incomplete = append(incomplete, fmt.Sprintf("issue #%d: %v", t.Issue, err))
				// Read each issue once, even when it fails.
				is = &server.IssueOut{Number: t.Issue}
			}
			f.Issues[t.Issue] = is
		}
		if t.PR > 0 {
			pr, err := src.PullRequest(ctx, t.PR)
			if err != nil {
				incomplete = append(incomplete, fmt.Sprintf("pull request !%d: %v", t.PR, err))
			} else {
				f.Reviews[t.PR] = pr
			}
			gate, err := src.Gate(ctx, t.PR)
			if err != nil {
				incomplete = append(incomplete, fmt.Sprintf("gate on !%d: %v", t.PR, err))
			} else {
				f.Gates[t.PR] = gate
			}
		}
	}
	a := Derive(w, f, now)
	a.Incomplete = incomplete
	return a, nil
}

func (w Window) resolve(now time.Time) Window {
	if w.Until.IsZero() {
		w.Until = now
	}
	return w
}

// overlaps is whether [start, end) meets w; a nil end is still going.
func (w Window) overlaps(start time.Time, end *time.Time) bool {
	return start.Before(w.Until) && (end == nil || !end.Before(w.Since))
}

func (w Window) contains(t *time.Time) bool {
	return t != nil && !t.Before(w.Since) && t.Before(w.Until)
}

// windowThreads are the flow threads with something in w, ordered by key.
func windowThreads(w Window, g Grouping, runs []server.RunItem, prs []*server.PRItem) []RunThread {
	seen := map[string]RunThread{}
	for _, r := range runs {
		rt := g.Runs[r.ID]
		if !Standalone(r.Kind) && w.overlaps(r.Created, r.Ended) {
			seen[rt.Key] = rt
		}
	}
	issueOf := map[int64]int64{}
	for issue, cands := range g.PRsForIssue {
		for _, pr := range cands {
			issueOf[pr] = issue
		}
	}
	for _, pr := range prs {
		if pr == nil || pr.Number <= 0 {
			continue
		}
		if PROpen(pr) || w.contains(pr.Created) || w.contains(pr.Updated) || w.contains(pr.MergedAt) || w.contains(pr.ClosedAt) {
			rt := RunThread{Issue: issueOf[pr.Number], PR: pr.Number}
			rt.Key = ThreadKey(rt.Issue, rt.PR, "")
			seen[rt.Key] = rt
		}
	}
	out := make([]RunThread, 0, len(seen))
	for _, rt := range seen {
		out = append(out, rt)
	}
	slices.SortFunc(out, func(a, b RunThread) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// Derive is the activity in w from f, at now.
func Derive(w Window, f Facts, now time.Time) *Activity {
	w = w.resolve(now)
	g := Group(f.Runs, f.PRs)
	a := &Activity{Window: w, Runs: []Run{}, Threads: []Thread{}}

	runs := slices.Clone(f.Runs)
	slices.SortFunc(runs, func(x, y server.RunItem) int {
		return cmp.Or(x.Created.Compare(y.Created), strings.Compare(x.ID, y.ID))
	})
	for _, r := range runs {
		if !w.overlaps(r.Created, r.Ended) {
			continue
		}
		rt := g.Runs[r.ID]
		a.Runs = append(a.Runs, Run{
			ID: r.ID, Kind: r.Kind, Actor: r.Actor, Family: r.Family, Harness: r.Harness, Spec: r.Spec,
			State: r.State, Status: r.Status, Outcome: r.Outcome, Created: r.Created, Ended: r.Ended, Purged: r.Purged,
			Issue: rt.Issue, PR: rt.PR, ThreadKey: rt.Key,
			Usage: r.Digest.Usage, UsageReported: usageReported(r.Digest.Usage),
		})
	}
	a.Usage = CoverageOf(a.Runs)

	prs := map[int64]*server.PRItem{}
	for _, pr := range f.PRs {
		if pr != nil {
			prs[pr.Number] = pr
		}
	}
	// Each issue's stops, by the thread the flow stopped in: the PR it
	// names, else the PR the issue's work belonged to at the time.
	stops := map[string][]Stop{}
	for issue, is := range f.Issues {
		if is == nil {
			continue
		}
		for _, c := range is.Comments {
			st, ok := flow.ParseStop(c.Body)
			if !ok {
				continue
			}
			key := ThreadKey(issue, g.PRAt(issue, c.Created, st.PR), "")
			stops[key] = append(stops[key], Stop{Time: c.Created, Reason: st.Reason, Comment: c.ID})
		}
	}

	for _, rt := range windowThreads(w, g, f.Runs, f.PRs) {
		t := Thread{Key: rt.Key, Issue: rt.Issue, PR: rt.PR, Runs: []string{}, Checks: []Check{}, Stops: []Stop{}, OwnerWaits: []OwnerWait{}}
		for _, r := range a.Runs {
			if r.ThreadKey == t.Key {
				t.Runs = append(t.Runs, r.ID)
			}
		}
		pr := prs[t.PR]
		if pr != nil {
			t.PRCreated, t.PRMerged, t.PRClosed = pr.Created, pr.MergedAt, pr.ClosedAt
		}
		if out := f.Reviews[t.PR]; out != nil {
			t.ImplementRounds = server.ChangesRequested(out.Reviews)
		}
		if gate, ok := f.Gates[t.PR]; ok {
			t.CheckRounds = len(gate.Failed)
			for _, st := range gate.Checks {
				t.Checks = append(t.Checks, check(st))
			}
		}
		t.Stops = append(t.Stops, stops[t.Key]...)
		slices.SortFunc(t.Stops, func(x, y Stop) int { return cmp.Or(x.Time.Compare(y.Time), cmp.Compare(x.Comment, y.Comment)) })

		var starts []time.Time
		for _, r := range f.Runs {
			if g.Runs[r.ID].Key == t.Key {
				starts = append(starts, r.Created)
			}
		}
		t.OwnerWaits = ownerWaits(t.Stops, starts, pr)
		a.Threads = append(a.Threads, t)
	}
	return a
}

// ownerWaits are the waits that stops leave a thread in: each from its stop
// until the first of the thread's next run, the PR's merge or close, and
// the next stop.
func ownerWaits(stops []Stop, runStarts []time.Time, pr *server.PRItem) []OwnerWait {
	out := []OwnerWait{}
	for i, st := range stops {
		w := OwnerWait{Start: st.Time, Reason: st.Reason}
		end := func(t time.Time, by string) {
			if t.After(st.Time) && (w.End == nil || t.Before(*w.End)) {
				w.End, w.EndedBy = &t, by
			}
		}
		for _, t := range runStarts {
			end(t, EndedByRun)
		}
		if pr != nil && pr.MergedAt != nil {
			end(*pr.MergedAt, EndedByMerge)
		}
		if pr != nil && pr.ClosedAt != nil {
			end(*pr.ClosedAt, EndedByClose)
		}
		if i+1 < len(stops) {
			end(stops[i+1].Time, EndedByStop)
		}
		out = append(out, w)
	}
	return out
}

// checkDuration matches the durations in a check's description: "passed:
// make e2e in 4m12s", "failed: make e2e (exit 2) after 1m03s", "timed out
// after 30m0s; …".
var checkDuration = regexp.MustCompile(`\b(?:in|after) ([0-9][0-9hms.]*)`)

// CheckDuration is the duration a check's description ends with: the last
// one that parses, since a timeout's description names the timeout first.
func CheckDuration(description string) (time.Duration, bool) {
	ms := checkDuration.FindAllStringSubmatch(description, -1)
	for i := len(ms) - 1; i >= 0; i-- {
		if d, err := time.ParseDuration(ms[i][1]); err == nil && d >= 0 {
			return d, true
		}
	}
	return 0, false
}

func check(st server.GateStatus) Check {
	c := Check{SHA: st.SHA, State: st.State, Description: st.Description, End: st.Created}
	if d, ok := CheckDuration(st.Description); ok {
		start := st.Created.Add(-d)
		c.Start, c.Derived = &start, true
	}
	return c
}
