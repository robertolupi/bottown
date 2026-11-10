package webui

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/runs"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// The live view (monitor-ui design §7): what is running now, assembled
// once, on the server, for the Now view and the terminal alike.

const (
	// quietAfter is how long an item may write nothing before it is quiet
	// (monitor-ui design §7.2). The one place the threshold is set.
	quietAfter = 5 * time.Minute
	// liveTail is how many rendered log lines an item carries by default,
	// and maxLiveTail the most a caller may ask for.
	liveTail    = 12
	maxLiveTail = 500
	// endedWithin is how recently a thread must have ended to be in Ended,
	// and maxEnded how many it holds.
	endedWithin = time.Hour
	maxEnded    = 5
	// liveInboxEvery is how often the summary's inbox counts are
	// recomputed; between, a poll reuses the last ones.
	liveInboxEvery = 30 * time.Second
)

// Live is GET /api/live: the live batch runs and check groups as items,
// what is live with no tail as rows, the threads that just ended, and the
// counts the Now view's summary line shows.
type Live struct {
	Items   []LiveItem  `json:"items"`
	Rows    []LiveRow   `json:"rows"`
	Ended   []LiveEnded `json:"ended"`
	Summary LiveSummary `json:"summary"`
}

// LiveFlow is the flow an item or row belongs to: the issue its lock is
// held for, and the flow's own account of its round and step (its status
// file). Round and MaxRounds are 0 when the flow has not reported them.
type LiveFlow struct {
	Issue     int64  `json:"issue"`
	Round     int    `json:"round,omitempty"`
	MaxRounds int    `json:"max_rounds,omitempty"`
	Step      string `json:"step,omitempty"`
}

// LiveItem is one tile: a live batch run (a landing included) or a live
// check group.
type LiveItem struct {
	ID string `json:"id"`
	// Kind is the run's kind (implement, verify, land, …), or check or e2e
	// for a check group.
	Kind      string    `json:"kind"`
	Target    string    `json:"target"` // "#42", "!57", or the run's own
	TargetURL string    `json:"target_url,omitempty"`
	Issue     int64     `json:"issue,omitempty"`
	IssueURL  string    `json:"issue_url,omitempty"`
	PR        int64     `json:"pr,omitempty"`
	PRURL     string    `json:"pr_url,omitempty"`
	Title     string    `json:"title,omitempty"` // the issue's, else the PR's
	SHA       string    `json:"sha,omitempty"`   // a check group's head
	Flow      *LiveFlow `json:"flow,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Family    string    `json:"family,omitempty"`
	Spec      string    `json:"spec,omitempty"`
	Started   time.Time `json:"started"`
	// Limit is how long the item may take, in seconds: a run's key
	// lifetime, a check group's --check-timeout; 0 when it has none.
	Limit int64 `json:"limit,omitempty"`
	// LastOutput is when it last wrote what its tail is read from
	// (lastOutput); nil before it wrote anything.
	LastOutput *time.Time     `json:"last_output,omitempty"`
	Quiet      bool           `json:"quiet"`
	Tail       []runs.LogLine `json:"tail"`
	Denials    int            `json:"denials"`
}

// LiveRow is one line under the tiles: a held flow with no live run or
// check, or an interactive run.
type LiveRow struct {
	Key       string    `json:"key"`          // flow-<issue>, or the run's id
	Kind      string    `json:"kind"`         // flow or interactive
	ID        string    `json:"id,omitempty"` // an interactive run's
	Target    string    `json:"target"`
	TargetURL string    `json:"target_url,omitempty"`
	Issue     int64     `json:"issue,omitempty"`
	PR        int64     `json:"pr,omitempty"`
	Title     string    `json:"title,omitempty"`
	Flow      *LiveFlow `json:"flow,omitempty"`
	// What is the flow's step, or "interactive".
	What string `json:"what"`
	// Detail is what a flow waits for ("waiting for agy (busy with
	// r-2b3c4d5e)"), or an interactive run's kind and actor.
	Detail string     `json:"detail,omitempty"`
	Since  *time.Time `json:"since,omitempty"`
}

// LiveEnded is a thread whose last run ended in the past hour.
type LiveEnded struct {
	Key    string    `json:"key"`
	Kind   string    `json:"kind"` // flow, or a standalone run's kind
	Issue  int64     `json:"issue,omitempty"`
	PR     int64     `json:"pr,omitempty"`
	RunID  string    `json:"run_id"` // the last run to end
	Target string    `json:"target"`
	Title  string    `json:"title,omitempty"`
	Ended  time.Time `json:"ended"`
}

// LiveSummary counts what Live holds, and the owner inbox by kind.
type LiveSummary struct {
	Running     int                   `json:"running"`
	Quiet       int                   `json:"quiet"`
	Waiting     int                   `json:"waiting"`
	Interactive int                   `json:"interactive"`
	Inbox       map[InboxItemKind]int `json:"inbox"`
	InboxError  string                `json:"inbox_error,omitempty"`
}

// liveInboxCache keeps the summary's inbox counts between polls.
type liveInboxCache struct {
	mu     sync.Mutex
	at     time.Time
	counts map[InboxItemKind]int
	err    string
}

// isQuiet is whether an item that last wrote at last (nil: never, so
// counting from started) is quiet at now.
func isQuiet(last *time.Time, started, now time.Time) bool {
	from := started
	if last != nil {
		from = *last
	}
	return now.Sub(from) >= quietAfter
}

// Live computes the live view, each item with its last tailLines rendered
// log lines (liveTail when 0 or less, at most maxLiveTail).
func (s *Server) Live(ctx context.Context, tailLines int) (Live, error) {
	return s.live(ctx, tailLines, time.Now().UTC())
}

func (s *Server) live(ctx context.Context, tailLines int, now time.Time) (Live, error) {
	if tailLines <= 0 {
		tailLines = liveTail
	}
	tailLines = min(tailLines, maxLiveTail)
	org, repo := s.orgAndRepo()
	repoURL := fmt.Sprintf("%s/%s/%s", s.cfg.Instance.ForgejoURL(), org, repo)

	liveRuns, err := s.computeLiveRuns(ctx, tailLines)
	if err != nil {
		return Live{}, err
	}
	checks := s.computeLiveChecks(ctx, tailLines)
	held := map[int64]runs.FlowStatusView{}
	heldFlows := runs.HeldFlows(filepath.Join(s.cfg.RepoTop, ".bottown", "runs"), repo)
	for _, f := range heldFlows {
		held[f.Issue] = f
	}
	flowOf := func(issue int64, step string) *LiveFlow {
		st, ok := held[issue]
		if issue <= 0 || !ok {
			return nil
		}
		if st.Step != "" {
			step = st.Step
		}
		return &LiveFlow{Issue: issue, Round: st.Round, MaxRounds: st.Rounds, Step: step}
	}

	out := Live{Items: []LiveItem{}, Rows: []LiveRow{}}
	var interactive []LiveRow
	for _, r := range liveRuns {
		if r.Interactive {
			since := r.Created
			interactive = append(interactive, LiveRow{
				Key: r.ID, Kind: "interactive", ID: r.ID, Target: r.Target, TargetURL: r.TargetURL,
				Issue: r.Issue, PR: r.PR, Title: cmp.Or(r.IssueTitle, r.PRTitle),
				What: "interactive", Detail: r.Kind + " · " + r.Actor, Since: &since,
			})
			continue
		}
		it := LiveItem{
			ID: r.ID, Kind: r.Kind, Target: r.Target, TargetURL: r.TargetURL,
			Issue: r.Issue, IssueURL: r.IssueURL, PR: r.PR, PRURL: r.PRURL,
			Title: cmp.Or(r.IssueTitle, r.PRTitle), Actor: r.Actor, Family: r.Family, Spec: r.Spec,
			Started: r.Created, LastOutput: r.LastOutput, Tail: r.LogTail, Denials: len(r.Denials),
		}
		if r.Expires.After(r.Created) {
			it.Limit = int64(r.Expires.Sub(r.Created) / time.Second)
		}
		switch r.Kind {
		case "implement":
			it.Flow = flowOf(r.Issue, runs.StepImplementing)
		case "verify":
			it.Flow = flowOf(r.Issue, runs.StepVerifying)
		}
		if it.Flow != nil && it.Flow.Round == 0 && r.Flow != nil {
			it.Flow.Round, it.Flow.MaxRounds = r.Flow.Round, r.Flow.MaxRounds
		}
		out.Items = append(out.Items, it)
	}
	for _, c := range checks {
		it := LiveItem{
			ID: c.ID, Kind: c.Group, Issue: c.Issue, IssueURL: c.IssueURL, PR: c.PR, PRURL: c.PRURL,
			Title: c.IssueTitle, SHA: c.SHA, Started: c.Created, LastOutput: c.LastOutput, Tail: c.LogTail,
			Flow: flowOf(c.Issue, runs.StepChecks),
		}
		switch {
		case c.Issue > 0:
			it.Target, it.TargetURL = fmt.Sprintf("#%d", c.Issue), c.IssueURL
		case c.PR > 0:
			it.Target, it.TargetURL = fmt.Sprintf("!%d", c.PR), c.PRURL
		default:
			it.Target = c.ID
		}
		if c.expires.After(c.Created) {
			it.Limit = int64(c.expires.Sub(c.Created) / time.Second)
		}
		out.Items = append(out.Items, it)
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if !a.Started.Equal(b.Started) {
			return a.Started.Before(b.Started)
		}
		return a.ID < b.ID
	})
	for i := range out.Items {
		it := &out.Items[i]
		it.Quiet = isQuiet(it.LastOutput, it.Started, now)
		if it.Tail == nil {
			it.Tail = []runs.LogLine{}
		}
		if it.Quiet {
			out.Summary.Quiet++
		}
	}

	// A held flow with nothing live on its issue is a row, saying what it
	// is doing as it reports it.
	liveIssue := map[int64]bool{}
	for _, it := range out.Items {
		liveIssue[it.Issue] = true
		if it.Flow != nil {
			liveIssue[it.Flow.Issue] = true
		}
	}
	for _, f := range heldFlows {
		if liveIssue[f.Issue] {
			continue
		}
		row := LiveRow{
			Key: fmt.Sprintf("flow-%d", f.Issue), Kind: "flow", Issue: f.Issue,
			Target: fmt.Sprintf("#%d", f.Issue), TargetURL: fmt.Sprintf("%s/issues/%d", repoURL, f.Issue),
			Flow: &LiveFlow{Issue: f.Issue, Round: f.Round, MaxRounds: f.Rounds, Step: f.Step},
			What: f.Step, Detail: f.Detail,
		}
		if !f.Since.IsZero() {
			since := f.Since.UTC()
			row.Since = &since
		}
		if iss, err := s.issue(ctx, f.Issue, false); err == nil {
			row.Title = iss.Title
		}
		out.Rows = append(out.Rows, row)
		out.Summary.Waiting++
	}
	sort.SliceStable(interactive, func(i, j int) bool { return interactive[i].Since.Before(*interactive[j].Since) })
	out.Rows = append(out.Rows, interactive...)
	out.Summary.Interactive = len(interactive)
	out.Summary.Running = len(out.Items)

	out.Ended = s.liveEnded(ctx, now, held)
	out.Summary.Inbox, out.Summary.InboxError = s.liveInboxCounts(ctx, now)
	return out, nil
}

// liveEnded lists up to maxEnded threads whose runs have all ended, the
// last within endedWithin of now, newest first. A flow thread whose flow
// still holds its lock has not ended. Threads are activity.Group's, as
// History groups them.
func (s *Server) liveEnded(ctx context.Context, now time.Time, held map[int64]runs.FlowStatusView) []LiveEnded {
	out := []LiveEnded{}
	all, err := s.serverRuns(ctx, "", false)
	if err != nil {
		return out
	}
	recent := func(t *time.Time) bool {
		return t != nil && !t.After(now) && now.Sub(*t) <= endedWithin
	}
	// Without a run that ended recently there is nothing to group, and no
	// PR list to read.
	if !slices.ContainsFunc(all, func(r serverapi.RunItem) bool { return recent(r.Ended) }) {
		return out
	}
	prs, _ := s.pullRequests(ctx, "all", false)
	g := activity.Group(all, prs)
	type thread struct {
		rt   activity.RunThread
		last serverapi.RunItem
		open bool
	}
	threads := map[string]*thread{}
	for _, r := range all {
		rt := g.Runs[r.ID]
		t := threads[rt.Key]
		if t == nil {
			t = &thread{rt: rt}
			threads[rt.Key] = t
		}
		if r.Ended == nil {
			t.open = true
			continue
		}
		if t.last.Ended == nil || r.Ended.After(*t.last.Ended) {
			t.last = r
		}
	}
	prTitle := map[int64]string{}
	for _, pr := range prs {
		if pr != nil {
			prTitle[pr.Number] = pr.Title
		}
	}
	org, repo := s.orgAndRepo()
	repoURL := fmt.Sprintf("%s/%s/%s", s.cfg.Instance.ForgejoURL(), org, repo)
	for key, t := range threads {
		standalone := activity.IsStandaloneKey(key)
		if t.open || !recent(t.last.Ended) {
			continue
		}
		if _, ok := held[t.rt.Issue]; ok && !standalone && t.rt.Issue > 0 {
			continue
		}
		e := LiveEnded{Key: key, Kind: "flow", Issue: t.rt.Issue, PR: t.rt.PR, RunID: t.last.ID, Ended: t.last.Ended.UTC()}
		if standalone {
			e.Kind = t.last.Kind
		}
		switch {
		case !standalone && e.Issue > 0:
			e.Target = fmt.Sprintf("#%d", e.Issue)
		case !standalone && e.PR > 0:
			e.Target = fmt.Sprintf("!%d", e.PR)
		default:
			e.Target, _ = runTarget(t.last.Run, repoURL)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Ended.Equal(out[j].Ended) {
			return out[i].Ended.After(out[j].Ended)
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > maxEnded {
		out = out[:maxEnded]
	}
	for i := range out {
		e := &out[i]
		if e.Issue > 0 {
			if iss, err := s.issue(ctx, e.Issue, false); err == nil {
				e.Title = iss.Title
			}
		}
		if e.Title == "" && e.PR > 0 {
			e.Title = prTitle[e.PR]
		}
	}
	return out
}

// liveInboxCounts counts the owner inbox by kind, recomputing it at most
// every liveInboxEvery: Now polls far more often than the inbox changes.
func (s *Server) liveInboxCounts(ctx context.Context, now time.Time) (map[InboxItemKind]int, string) {
	c := &s.liveInbox
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts != nil && now.Sub(c.at) < liveInboxEvery && !now.Before(c.at) {
		return c.counts, c.err
	}
	counts := map[InboxItemKind]int{}
	items, err := s.ComputeInbox(ctx)
	c.err = ""
	if err != nil {
		c.err = err.Error()
	}
	for _, it := range items {
		counts[it.Kind]++
	}
	c.at, c.counts = now, counts
	return counts, c.err
}
