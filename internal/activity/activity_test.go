package activity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

var t0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// at is t0 plus h hours.
func at(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }

func ptr[T any](v T) *T { return &v }

// live is the end of a run that has not ended.
const live = -1e9

func run(id, kind string, issue, pr int64, created, ended float64) server.RunItem {
	r := server.RunItem{Run: secrets.Run{ID: id, Kind: kind, Actor: "claude", Issue: issue, PR: pr, Created: at(created)}, State: secrets.RunEnded}
	if issue > 0 && kind == "implement" {
		r.Branch = fmt.Sprintf("issue/%d", issue)
	}
	if ended != live {
		r.Ended, r.Status = ptr(at(ended)), "succeeded"
	} else {
		r.State = secrets.RunActive
	}
	return r
}

func pr(n, issue int64, created float64) *server.PRItem {
	return &server.PRItem{Number: n, State: "open", HeadBranch: fmt.Sprintf("issue/%d", issue), Created: ptr(at(created)), Updated: ptr(at(created))}
}

func merged(p *server.PRItem, h float64) *server.PRItem {
	p.State, p.Merged, p.MergedAt, p.ClosedAt, p.Updated = "closed", true, ptr(at(h)), ptr(at(h)), ptr(at(h))
	return p
}

func closed(p *server.PRItem, h float64) *server.PRItem {
	p.State, p.ClosedAt, p.Updated = "closed", ptr(at(h)), ptr(at(h))
	return p
}

// stopComment is a stop as the flow writes it (flow's stop_test ties
// ParseStop to Supervisor.stop).
func stopComment(id int64, reason flow.StopReason, pr int64, h float64) server.CommentOut {
	body := fmt.Sprintf("**bottown flow: %s**\n\nsome detail", reason)
	if pr > 0 {
		body += fmt.Sprintf("\n\nPull request: !%d", pr)
	}
	return server.CommentOut{ID: id, Author: "rlupi", Body: body, Created: at(h)}
}

func issueWith(n int64, comments ...server.CommentOut) *server.IssueOut {
	return &server.IssueOut{Number: n, Comments: comments}
}

// listed is issue n's entry in the issue list, open.
func listed(n int64, created, updated float64) *server.IssueItem {
	return &server.IssueItem{Number: n, State: "open", Created: at(created), Updated: at(updated)}
}

func closedIssue(is *server.IssueItem, h float64) *server.IssueItem {
	is.State, is.Closed, is.Updated = "closed", ptr(at(h)), at(h)
	return is
}

var week = Window{Since: t0, Until: t0.Add(7 * 24 * time.Hour)}

func thread(t *testing.T, a *Activity, key string) Thread {
	t.Helper()
	for _, th := range a.Threads {
		if th.Key == key {
			return th
		}
	}
	var keys []string
	for _, th := range a.Threads {
		keys = append(keys, th.Key)
	}
	t.Fatalf("no thread %s in %v", key, keys)
	return Thread{}
}

func runByID(t *testing.T, a *Activity, id string) Run {
	t.Helper()
	for _, r := range a.Runs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no run %s", id)
	return Run{}
}

func TestThreadTitleAndInteractive(t *testing.T) {
	impl := run("r-impl", "implement", 7, 0, 1, 2)
	impl.Interactive = true
	pre := run("r-pre", "implement", 0, 9, 3, 4)
	titled := listed(7, 0, 2)
	titled.Title = "the issue"
	p := pr(9, 0, 3)
	p.Title = "the PR"
	a := Derive(week, Facts{Runs: []server.RunItem{impl, pre}, PRs: []*server.PRItem{p}, IssueList: []*server.IssueItem{titled}}, week.Until)
	if r := runByID(t, a, "r-impl"); !r.Interactive {
		t.Errorf("interactive run = %+v", r)
	}
	if th := thread(t, a, "flow:issue:7"); th.Title != "the issue" {
		t.Errorf("issue thread title = %q", th.Title)
	}
	// A PR without an issue is titled by the PR.
	if th := thread(t, a, "flow:pr:9"); th.Title != "the PR" {
		t.Errorf("PR thread title = %q", th.Title)
	}
}

func TestRunsPurgedThreadlessAndUsage(t *testing.T) {
	purged := run("r-purged", "implement", 5, 0, 1, 2)
	purged.Purged = true
	purged.Digest.Usage = &secrets.Usage{InputTokens: ptr(int64(0))} // reported, as zero
	lone := run("r-lone", "implement", 0, 0, 3, 4)
	lone.Digest.Usage = &secrets.Usage{Turns: ptr(int64(3))} // no tokens, no cost
	discuss := run("r-discuss", "discuss", 5, 0, 5, 6)
	discuss.Digest.Usage = &secrets.Usage{CostUSD: ptr(0.25)}
	before := run("r-before", "implement", 6, 0, -48, -47)

	a := Derive(week, Facts{Runs: []server.RunItem{discuss, lone, purged, before}}, week.Until)

	if got := []string{a.Runs[0].ID, a.Runs[1].ID, a.Runs[2].ID}; len(a.Runs) != 3 || !slices.Equal(got, []string{"r-purged", "r-lone", "r-discuss"}) {
		t.Fatalf("runs = %+v, want the three in the window, oldest first", a.Runs)
	}
	if r := runByID(t, a, "r-purged"); !r.Purged || r.ThreadKey != "flow:issue:5" || !r.UsageReported {
		t.Errorf("purged run = %+v", r)
	}
	if r := runByID(t, a, "r-lone"); r.ThreadKey != "flow:run:r-lone" || r.UsageReported {
		t.Errorf("run without a thread = %+v", r)
	}
	if r := runByID(t, a, "r-discuss"); r.ThreadKey != "discuss:r-discuss" || !r.UsageReported {
		t.Errorf("discuss run = %+v", r)
	}
	if a.Usage != (Coverage{Runs: 3, Reported: 2}) {
		t.Errorf("usage = %+v, want 2 of 3", a.Usage)
	}
	if th := thread(t, a, "flow:run:r-lone"); th.Issue != 0 || th.PR != 0 || !slices.Equal(th.Runs, []string{"r-lone"}) {
		t.Errorf("lone thread = %+v", th)
	}
	for _, th := range a.Threads {
		if IsStandaloneKey(th.Key) {
			t.Errorf("standalone run became a thread: %+v", th)
		}
	}
	if CoverageOf(nil) != (Coverage{}) {
		t.Errorf("coverage of no runs = %+v", CoverageOf(nil))
	}
}

func TestThreadRoundsAndChecks(t *testing.T) {
	impl := run("r-impl", "implement", 7, 0, 1, 2)
	ver := run("r-ver", "verify", 0, 12, 3, 4)
	gate := server.GateOut{Failed: []string{"aaa"}, Checks: []server.GateStatus{
		{SHA: "aaa", CommitStatus: server.CommitStatus{Context: "bottown/e2e", State: "failure", Description: "failed: make e2e (exit 2) after 1m03s (flow)", Created: at(2.5), Gate: true}},
		{SHA: "bbb", CommitStatus: server.CommitStatus{Context: "bottown/e2e", State: "success", Description: "passed: make e2e in 4m12s (flow)", Created: at(2.9), Gate: true}},
		{SHA: "ccc", CommitStatus: server.CommitStatus{Context: "bottown/e2e", State: "success", Description: "(flow)", Created: at(3.5), Gate: true}},
	}}
	reviews := &server.PROut{Number: 12, Reviews: []server.ReviewOut{{State: "REQUEST_CHANGES"}, {State: "COMMENT"}, {State: "REQUEST_CHANGES", Dismissed: true}, {State: "APPROVED"}}}
	a := Derive(week, Facts{
		Runs: []server.RunItem{impl, ver}, PRs: []*server.PRItem{pr(12, 7, 1.5)},
		Reviews: map[int64]*server.PROut{12: reviews}, Gates: map[int64]server.GateOut{12: gate},
	}, week.Until)

	th := thread(t, a, "flow:issue:7:pr:12")
	if th.Issue != 7 || th.PR != 12 || !th.PRCreated.Equal(at(1.5)) || th.PRMerged != nil || !slices.Equal(th.Runs, []string{"r-impl", "r-ver"}) {
		t.Errorf("thread = %+v", th)
	}
	if th.ImplementRounds != 2 || th.CheckRounds != 1 {
		t.Errorf("rounds = %d implement, %d check; want 2, 1", th.ImplementRounds, th.CheckRounds)
	}
	if len(th.Checks) != 3 {
		t.Fatalf("checks = %+v", th.Checks)
	}
	if c := th.Checks[0]; !c.Derived || !c.Start.Equal(at(2.5).Add(-63*time.Second)) || !c.End.Equal(at(2.5)) || c.State != "failure" {
		t.Errorf("failed check = %+v", c)
	}
	if c := th.Checks[1]; !c.Derived || !c.Start.Equal(at(2.9).Add(-252*time.Second)) {
		t.Errorf("passed check = %+v", c)
	}
	if c := th.Checks[2]; c.Derived || c.Start != nil || !c.End.Equal(at(3.5)) {
		t.Errorf("check without a duration = %+v, want only its end", c)
	}
}

func TestCheckDuration(t *testing.T) {
	for desc, want := range map[string]time.Duration{
		"passed: make e2e in 4m12s (flow)":                                          4*time.Minute + 12*time.Second,
		"passed: make e2e in 12.5s (flow)":                                          12500 * time.Millisecond,
		"failed: make e2e (exit 2) after 1h02m (flow)":                              time.Hour + 2*time.Minute,
		"timed out after 30m0s; failed: make e2e (exit -1) after 29m59s (flow)":     29*time.Minute + 59*time.Second,
		"timed out after 30m0s;  (flow)":                                            30 * time.Minute,
		"passed: make e2e in docker; go test in 3m00s (flow)":                       3 * time.Minute,
		"passed: ./scripts/run-in-container in 9.0s (flow)":                         9 * time.Second,
		"passed: make e2e in 4m12s; dependencies changed: go.mod, web/package.json": 4*time.Minute + 12*time.Second,
	} {
		if got, ok := CheckDuration(desc); !ok || got != want {
			t.Errorf("CheckDuration(%q) = %v, %v; want %v", desc, got, ok, want)
		}
	}
	for _, desc := range []string{"", "(flow)", "passed: make e2e in docker (flow)", "failed: make e2e (exit 2) after…"} {
		if got, ok := CheckDuration(desc); ok {
			t.Errorf("CheckDuration(%q) = %v, want none", desc, got)
		}
	}
}

// Every stop reason parses into a stop, and only stops do: the flow's
// announcements of a round it starts are not.
func TestStopReasons(t *testing.T) {
	var comments []server.CommentOut
	for i, reason := range flow.StopReasons {
		comments = append(comments, stopComment(int64(i+1), reason, 12, 2+float64(i)))
	}
	comments = append(comments,
		server.CommentOut{ID: 100, Body: "**bottown flow: checks failed** at abc on !12; starting implement round 1 of 2 to fix them.", Created: at(1.5)},
		server.CommentOut{ID: 101, Body: "looks good to me", Created: at(1.6)})
	a := Derive(week, Facts{
		Runs: []server.RunItem{run("r-impl", "implement", 7, 0, 1, 1.4)}, PRs: []*server.PRItem{pr(12, 7, 1.2)},
		Issues: map[int64]*server.IssueOut{7: issueWith(7, comments...)},
	}, week.Until)
	th := thread(t, a, "flow:issue:7:pr:12")
	var got []flow.StopReason
	for _, st := range th.Stops {
		got = append(got, st.Reason)
	}
	if !slices.Equal(got, flow.StopReasons) {
		t.Fatalf("stops = %v, want %v", got, flow.StopReasons)
	}
	// Back-to-back stops with no run between: each wait ends at the next
	// stop, and the last one still waits. A merged stop leaves none.
	var waiting []Stop
	for _, st := range th.Stops {
		if st.Reason != flow.Merged {
			waiting = append(waiting, st)
		}
	}
	if len(th.OwnerWaits) != len(waiting) {
		t.Fatalf("waits = %+v", th.OwnerWaits)
	}
	for i, w := range th.OwnerWaits {
		if w.Reason != waiting[i].Reason || !w.Start.Equal(waiting[i].Time) {
			t.Errorf("wait %d = %+v, want from %+v", i, w, waiting[i])
		}
		next := slices.IndexFunc(th.Stops, func(st Stop) bool { return st.Time.After(w.Start) })
		if next < 0 {
			if w.End != nil || w.EndedBy != "" {
				t.Errorf("last wait = %+v, want still waiting", w)
			}
		} else if w.EndedBy != EndedByStop || !w.End.Equal(th.Stops[next].Time) {
			t.Errorf("wait %d = %+v, want ended by the stop at %v", i, w, th.Stops[next].Time)
		}
	}
}

func TestOwnerWaits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runs    []server.RunItem
		pr      *server.PRItem
		stops   []server.CommentOut
		end     *time.Time
		endedBy string
	}{
		{
			name:    "a stop followed by a resume",
			runs:    []server.RunItem{run("r-1", "implement", 7, 0, 1, 2), run("r-2", "verify", 0, 12, 2.5, 3), run("r-3", "implement", 7, 0, 10, 11)},
			pr:      pr(12, 7, 1.5),
			stops:   []server.CommentOut{stopComment(1, flow.RunFailed, 12, 3.1)},
			end:     ptr(at(10)),
			endedBy: EndedByRun,
		},
		{
			name:    "a stop followed by a merge",
			runs:    []server.RunItem{run("r-1", "implement", 7, 0, 1, 2), run("r-2", "verify", 0, 12, 2.5, 3)},
			pr:      merged(pr(12, 7, 1.5), 20),
			stops:   []server.CommentOut{stopComment(1, flow.ReadyToMerge, 12, 3.1)},
			end:     ptr(at(20)),
			endedBy: EndedByMerge,
		},
		{
			name:    "a stop followed by a close",
			runs:    []server.RunItem{run("r-1", "implement", 7, 0, 1, 2), run("r-2", "verify", 0, 12, 2.5, 3)},
			pr:      closed(pr(12, 7, 1.5), 30),
			stops:   []server.CommentOut{stopComment(1, flow.VerifierUndecided, 12, 3.1)},
			end:     ptr(at(30)),
			endedBy: EndedByClose,
		},
		{
			name:  "a stop still waiting",
			runs:  []server.RunItem{run("r-1", "implement", 7, 0, 1, 2)},
			pr:    pr(12, 7, 1.5),
			stops: []server.CommentOut{stopComment(1, flow.OutOfRounds, 12, 3.1)},
		},
		{
			// The flow stopped before opening a PR; the next run opened
			// one, and the stop is in the PR's thread with the runs.
			name:    "a stop before the PR",
			runs:    []server.RunItem{run("r-1", "implement", 7, 0, 1, 2), run("r-2", "implement", 7, 0, 5, 6)},
			pr:      pr(12, 7, 5.5),
			stops:   []server.CommentOut{stopComment(1, flow.RunFailed, 0, 2.1)},
			end:     ptr(at(5)),
			endedBy: EndedByRun,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Derive(week, Facts{Runs: tc.runs, PRs: []*server.PRItem{tc.pr}, Issues: map[int64]*server.IssueOut{7: issueWith(7, tc.stops...)}}, week.Until)
			th := thread(t, a, "flow:issue:7:pr:12")
			if len(th.Stops) != 1 || len(th.OwnerWaits) != 1 {
				t.Fatalf("stops %+v, waits %+v", th.Stops, th.OwnerWaits)
			}
			w := th.OwnerWaits[0]
			if !w.Start.Equal(th.Stops[0].Time) || w.Reason != th.Stops[0].Reason || w.EndedBy != tc.endedBy {
				t.Errorf("wait = %+v, want ended by %q", w, tc.endedBy)
			}
			if (w.End == nil) != (tc.end == nil) || (w.End != nil && !w.End.Equal(*tc.end)) {
				t.Errorf("wait end = %v, want %v", w.End, tc.end)
			}
		})
	}
}

// Two PRs of one issue: each stop waits in the thread of the PR it names.
func TestStopsByThread(t *testing.T) {
	first := closed(pr(12, 7, 1.5), 4)
	a := Derive(week, Facts{
		Runs: []server.RunItem{run("r-1", "implement", 7, 0, 1, 2), run("r-2", "implement", 7, 0, 5, 6)},
		PRs:  []*server.PRItem{first, pr(13, 7, 5.5)},
		Issues: map[int64]*server.IssueOut{7: issueWith(7,
			stopComment(1, flow.ConflictsWithMain, 12, 2.1),
			stopComment(2, flow.ChecksFailed, 13, 6.1))},
	}, week.Until)
	if th := thread(t, a, "flow:issue:7:pr:12"); len(th.Stops) != 1 || th.Stops[0].Reason != flow.ConflictsWithMain ||
		th.OwnerWaits[0].EndedBy != EndedByClose || !slices.Equal(th.Runs, []string{"r-1"}) {
		t.Errorf("first thread = %+v", th)
	}
	if th := thread(t, a, "flow:issue:7:pr:13"); len(th.Stops) != 1 || th.Stops[0].Reason != flow.ChecksFailed ||
		th.OwnerWaits[0].End != nil || !slices.Equal(th.Runs, []string{"r-2"}) {
		t.Errorf("second thread = %+v", th)
	}
}

// fakeSource counts Load's reads, which may run concurrently.
type fakeSource struct {
	mu    sync.Mutex
	facts Facts
	reads map[string]int
	fail  map[string]bool
}

func (f *fakeSource) read(what string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads[what]++
	if f.fail[what] {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeSource) Runs(context.Context) ([]server.RunItem, error) {
	return f.facts.Runs, f.read("runs")
}

func (f *fakeSource) PullRequests(context.Context) ([]*server.PRItem, error) {
	return f.facts.PRs, f.read("prs")
}

func (f *fakeSource) Issues(context.Context) ([]*server.IssueItem, error) {
	return f.facts.IssueList, f.read("issues")
}

func (f *fakeSource) PullRequest(_ context.Context, n int64) (*server.PROut, error) {
	return f.facts.Reviews[n], f.read(fmt.Sprintf("pr %d", n))
}

func (f *fakeSource) Gate(_ context.Context, n int64) (server.GateOut, error) {
	return f.facts.Gates[n], f.read(fmt.Sprintf("gate %d", n))
}

func (f *fakeSource) Issue(_ context.Context, n int64) (*server.IssueOut, error) {
	return f.facts.Issues[n], f.read(fmt.Sprintf("issue %d", n))
}

// Load reads each issue and PR of the window's threads once, each other
// open issue or issue updated in the window once, and nothing else.
func TestLoadReadsAreBounded(t *testing.T) {
	src := &fakeSource{reads: map[string]int{}, fail: map[string]bool{"gate 13": true}, facts: Facts{
		Runs: []server.RunItem{
			run("r-1", "implement", 7, 0, 1, 2), run("r-2", "implement", 7, 0, 5, 6),
			run("r-old", "implement", 3, 0, -100, -99), run("r-d", "discuss", 4, 0, 1, 2),
		},
		PRs: []*server.PRItem{closed(pr(12, 7, 1.5), 4), pr(13, 7, 5.5), merged(pr(9, 3, -99.5), -98)},
		IssueList: []*server.IssueItem{
			listed(7, 0, 6.1), closedIssue(listed(3, -101, -98), -98),
			listed(4, -200, 2),   // the discuss run commented: updated in the window
			listed(5, -200, -50), // open and unchanged: an older stop may hold it
		},
		Issues: map[int64]*server.IssueOut{7: issueWith(7, stopComment(1, flow.ReadyToMerge, 13, 6.1))},
	}}
	a, err := Load(context.Background(), src, nil, Window{Since: t0}, week.Until)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"runs": 1, "prs": 1, "issues": 1, "issue 4": 1, "issue 5": 1, "issue 7": 1, "pr 12": 1, "gate 12": 1, "pr 13": 1, "gate 13": 1}
	if fmt.Sprint(src.reads) != fmt.Sprint(want) {
		t.Errorf("reads = %v, want %v", src.reads, want)
	}
	if !a.Window.Until.Equal(week.Until) || len(a.Threads) != 2 || len(a.Incomplete) != 1 {
		t.Errorf("activity = %+v", a)
	}
	if th := thread(t, a, "flow:issue:7:pr:13"); len(th.Stops) != 1 {
		t.Errorf("thread = %+v", th)
	}

	src.fail = map[string]bool{"runs": true}
	if _, err := Load(context.Background(), src, nil, week, week.Until); err == nil {
		t.Error("Load without runs succeeded")
	}
}

// A flow without a PR whose runs ended before the window: Load reads its
// issue when the issue is still open, or changed in the window, and keeps
// the thread while a stop or a wait of it meets the window.
func TestLoadThreadWithoutPRBeforeWindow(t *testing.T) {
	old := run("r-old", "implement", 7, 0, -10, -9)
	for _, tc := range []struct {
		name    string
		issue   *server.IssueItem
		stop    float64
		reads   int // of issue 7
		shown   bool
		end     *time.Time
		endedBy string
	}{
		{name: "an ongoing wait from before the window", issue: listed(7, -20, -8), stop: -8, reads: 1, shown: true},
		{name: "a stop in the window", issue: listed(7, -20, 2), stop: 2, reads: 1, shown: true},
		{name: "a wait the issue's close ended in the window", issue: closedIssue(listed(7, -20, -8), 3), stop: -8, reads: 1, shown: true,
			end: ptr(at(3)), endedBy: EndedByClose},
		{name: "a wait the issue's close ended before the window", issue: closedIssue(listed(7, -20, -8), -5), stop: -8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{reads: map[string]int{}, facts: Facts{
				Runs:      []server.RunItem{old},
				PRs:       []*server.PRItem{merged(pr(9, 3, -99.5), -98)},
				IssueList: []*server.IssueItem{tc.issue},
				Issues:    map[int64]*server.IssueOut{7: issueWith(7, stopComment(1, flow.RunFailed, 0, tc.stop))},
			}}
			a, err := Load(context.Background(), src, nil, week, week.Until)
			if err != nil {
				t.Fatal(err)
			}
			if got := src.reads["issue 7"]; got != tc.reads {
				t.Errorf("issue 7 read %d times, want %d (reads %v)", got, tc.reads, src.reads)
			}
			if !tc.shown {
				if len(a.Threads) != 0 {
					t.Errorf("threads = %+v, want none", a.Threads)
				}
				return
			}
			th := thread(t, a, "flow:issue:7")
			if len(th.Runs) != 0 || len(th.Stops) != 1 || len(th.OwnerWaits) != 1 {
				t.Fatalf("thread = %+v", th)
			}
			w := th.OwnerWaits[0]
			if !w.Start.Equal(at(tc.stop)) || w.EndedBy != tc.endedBy || (w.End == nil) != (tc.end == nil) || (w.End != nil && !w.End.Equal(*tc.end)) {
				t.Errorf("wait = %+v, want end %v by %q", w, tc.end, tc.endedBy)
			}
		})
	}
}

// A stop before the flow's first run (its actor busy), with no PR: Load
// reads its issue while it is open, changed in the window or not, and
// keeps the thread while its wait goes on.
func TestLoadStopWithoutRuns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		issue *server.IssueItem
		stop  float64
	}{
		{name: "a stop in the window", issue: listed(8, -20, 4), stop: 4},
		{name: "a stop before the window, the issue unchanged since", issue: listed(8, -20, -8), stop: -8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{reads: map[string]int{}, facts: Facts{
				IssueList: []*server.IssueItem{tc.issue},
				Issues:    map[int64]*server.IssueOut{8: issueWith(8, stopComment(1, flow.ActorBusy, 0, tc.stop))},
			}}
			a, err := Load(context.Background(), src, nil, week, week.Until)
			if err != nil {
				t.Fatal(err)
			}
			if src.reads["issue 8"] != 1 {
				t.Errorf("reads = %v, want issue 8 once", src.reads)
			}
			th := thread(t, a, "flow:issue:8")
			if len(th.Stops) != 1 || th.Stops[0].Reason != flow.ActorBusy || len(th.OwnerWaits) != 1 {
				t.Fatalf("thread = %+v", th)
			}
			if w := th.OwnerWaits[0]; !w.Start.Equal(at(tc.stop)) || w.End != nil {
				t.Errorf("wait = %+v, want from hour %v and still going", w, tc.stop)
			}
		})
	}
}

// A merged stop, which the flow writes after the owner merged its PR mid-flow,
// leaves the owner no wait; the stop before it still ends at the merge.
func TestOwnerWaitsMergedStop(t *testing.T) {
	stops := []Stop{{Time: at(1), Reason: flow.ChecksFailed}, {Time: at(3), Reason: flow.Merged}}
	waits := ownerWaits(stops, nil, &server.PRItem{MergedAt: ptr(at(2))}, ptr(at(2)))
	if len(waits) != 1 || waits[0].Reason != flow.ChecksFailed || waits[0].End == nil || !waits[0].End.Equal(at(2)) || waits[0].EndedBy != EndedByMerge {
		t.Fatalf("waits = %+v", waits)
	}
}

// With a cache, Load reads an issue again only once its updated time
// changes; without one, or with an emptied one, it reads it each time.
func TestLoadCache(t *testing.T) {
	src := &fakeSource{reads: map[string]int{}, facts: Facts{
		IssueList: []*server.IssueItem{listed(8, -20, -8)},
		Issues:    map[int64]*server.IssueOut{8: issueWith(8, stopComment(1, flow.ActorBusy, 0, -8))},
	}}
	var cache Cache
	load := func(c *Cache) *Activity {
		t.Helper()
		a, err := Load(context.Background(), src, c, week, week.Until)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	load(&cache)
	if a := load(&cache); src.reads["issue 8"] != 1 || len(thread(t, a, "flow:issue:8").Stops) != 1 {
		t.Errorf("after two loads: reads = %v, activity = %+v", src.reads, a)
	}

	// A new stop updates the issue, and the next load reads it.
	src.facts.IssueList = []*server.IssueItem{listed(8, -20, 3)}
	src.facts.Issues[8] = issueWith(8, stopComment(1, flow.ActorBusy, 0, -8), stopComment(2, flow.RunFailed, 0, 3))
	if a := load(&cache); src.reads["issue 8"] != 2 || len(thread(t, a, "flow:issue:8").Stops) != 2 {
		t.Errorf("after the update: reads = %v, activity = %+v", src.reads, a)
	}

	load(nil)
	load(&Cache{})
	if src.reads["issue 8"] != 4 {
		t.Errorf("reads = %v, want issue 8 read by each load without the cache", src.reads)
	}
}

// With a cache, a merged or closed PR's reviews and gate are read again
// only once its updated time changes; an open PR's on every load, since
// the flow's check statuses do not update it.
func TestLoadPRCache(t *testing.T) {
	src := &fakeSource{reads: map[string]int{}, facts: Facts{
		Runs: []server.RunItem{run("r-1", "implement", 7, 0, 1, 2), run("r-2", "implement", 8, 0, 3, 4)},
		PRs:  []*server.PRItem{merged(pr(12, 7, 1.5), 4), pr(13, 8, 3.5)},
		Reviews: map[int64]*server.PROut{
			12: {Reviews: []server.ReviewOut{{State: "REQUEST_CHANGES"}}},
			13: {},
		},
		Gates: map[int64]server.GateOut{12: {Failed: []string{"abc"}}},
	}}
	var cache Cache
	load := func() *Activity {
		t.Helper()
		a, err := Load(context.Background(), src, &cache, week, week.Until)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	load()
	a := load()
	if src.reads["pr 12"] != 1 || src.reads["gate 12"] != 1 || src.reads["pr 13"] != 2 || src.reads["gate 13"] != 2 {
		t.Errorf("after two loads: reads = %v", src.reads)
	}
	if th := thread(t, a, "flow:issue:7:pr:12"); th.ImplementRounds != 1 || th.CheckRounds != 1 {
		t.Errorf("thread from the cache = %+v", th)
	}

	// A late review updates the closed PR, and the next load reads it.
	src.facts.PRs[0].Updated = ptr(at(5))
	load()
	if src.reads["pr 12"] != 2 || src.reads["gate 12"] != 2 {
		t.Errorf("after the update: reads = %v", src.reads)
	}

	// A failed read is not cached.
	src.fail = map[string]bool{"gate 12": true}
	src.facts.PRs[0].Updated = ptr(at(6))
	load()
	src.fail = nil
	load()
	if src.reads["gate 12"] != 4 || src.reads["pr 12"] != 3 {
		t.Errorf("after a failed gate read: reads = %v", src.reads)
	}
}

// slowSource holds each issue read a moment and counts how many ran at
// once.
type slowSource struct {
	*fakeSource
	running, peak atomic.Int32
}

func (s *slowSource) Issue(ctx context.Context, n int64) (*server.IssueOut, error) {
	now := s.running.Add(1)
	defer s.running.Add(-1)
	for {
		p := s.peak.Load()
		if now <= p || s.peak.CompareAndSwap(p, now) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	return s.fakeSource.Issue(ctx, n)
}

// Load's reads run concurrently, at most Concurrency at a time, and its
// Incomplete is sorted however they finish.
func TestLoadConcurrent(t *testing.T) {
	src := &slowSource{fakeSource: &fakeSource{reads: map[string]int{}, fail: map[string]bool{}, facts: Facts{Issues: map[int64]*server.IssueOut{}}}}
	for n := int64(1); n <= 3*Concurrency; n++ {
		src.facts.IssueList = append(src.facts.IssueList, listed(n, -20, 1))
		if n%5 == 0 {
			src.fail[fmt.Sprintf("issue %d", n)] = true
		}
	}
	a, err := Load(context.Background(), src, nil, week, week.Until)
	if err != nil {
		t.Fatal(err)
	}
	if p := src.peak.Load(); p < 2 || p > Concurrency {
		t.Errorf("peak concurrent reads = %d, want 2..%d", p, Concurrency)
	}
	if len(src.reads) != 3+3*Concurrency {
		t.Errorf("reads = %v", src.reads)
	}
	if len(a.Incomplete) != 4 || !slices.IsSorted(a.Incomplete) {
		t.Errorf("incomplete = %q, want 4 sorted", a.Incomplete)
	}
}

func TestParallel(t *testing.T) {
	var mu sync.Mutex
	var got []int
	var fns []func()
	for i := range 20 {
		fns = append(fns, func() { mu.Lock(); got = append(got, i); mu.Unlock() })
	}
	Parallel(3, fns)
	slices.Sort(got)
	if len(slices.Compact(got)) != 20 || got[0] != 0 || got[19] != 19 {
		t.Errorf("ran %v", got)
	}
	Parallel(0, nil)
}
