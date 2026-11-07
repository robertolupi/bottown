package webui

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/runs"
)

// Timeline is GET /api/timeline: the window's runs laid out in lanes, by
// actor and by flow thread, with the time each lane spent and how many
// batch runs were live at once. It is built from the repository's activity
// (internal/activity) and the flows' live checks, so the browser draws it
// without rules of its own. Every interval is clipped to the window; one
// still going is marked live, and ends at Now or the window's end.
type Timeline struct {
	Window activity.Window `json:"window"`
	Now    time.Time       `json:"now"`
	// Open: the window ends now (the request gave no until), so the view
	// grows it, and its live intervals, with the clock.
	Open bool `json:"open"`
	// Runs are the window's runs, as activity gives them; lanes name them
	// by ID.
	Runs        []activity.Run `json:"runs"`
	Actors      []ActorLane    `json:"actors"`
	Flows       []FlowLane     `json:"flows"`
	Concurrency Concurrency    `json:"concurrency"`
	Incomplete  []string       `json:"incomplete,omitempty"`
}

// ActorLane is one actor's runs. Rows are the IDs of its runs in sub-rows,
// packed so that no two runs in a row overlap: an actor may run several
// batch runs at once.
type ActorLane struct {
	Actor string     `json:"actor"`
	Rows  [][]string `json:"rows"`
	Runs  int        `json:"runs"`
	// Busy is the seconds of the window that at least one of its runs,
	// interactive ones included, was live.
	Busy float64 `json:"busy"`
}

// Segment kinds in a flow lane.
const (
	SegmentRun       = "run"
	SegmentE2E       = "e2e"
	SegmentOwnerWait = "owner_wait"
	SegmentOverhead  = "overhead"
)

// Segment is one interval of a flow lane.
type Segment struct {
	Kind  string    `json:"kind"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// Live: it has not ended; End is Now, or the window's end.
	Live  bool   `json:"live,omitempty"`
	RunID string `json:"run_id,omitempty"` // run
	// A check's state (success, failure, or running for a live one), its
	// commit and description. Derived: its start was derived from the
	// description; a check whose description gives no duration has
	// Start equal to End.
	State       string `json:"state,omitempty"`
	SHA         string `json:"sha,omitempty"`
	Description string `json:"description,omitempty"`
	Derived     bool   `json:"derived,omitempty"`
	// An owner wait's stop reason, and what ended it.
	Reason  string `json:"reason,omitempty"`
	EndedBy string `json:"ended_by,omitempty"`
}

// FlowLane is one flow thread: its runs, checks, owner waits and the gaps
// between them, in time order.
type FlowLane struct {
	Key      string    `json:"key"`
	Issue    int64     `json:"issue,omitempty"`
	PR       int64     `json:"pr,omitempty"`
	Title    string    `json:"title,omitempty"`
	Segments []Segment `json:"segments"`
	Runs     int       `json:"runs"`
	// Seconds of the window spent on each: its runs (Agent), its checks,
	// waiting on the owner, and the flow's other gaps between them.
	Agent     float64 `json:"agent"`
	E2E       float64 `json:"e2e"`
	OwnerWait float64 `json:"owner_wait"`
	Overhead  float64 `json:"overhead"`
}

// Concurrency is how many batch runs (not interactive ones) were live at
// each moment of the window: Steps[i].N from Steps[i].T until the next
// step, or the window's end; Mean is over the window.
type Concurrency struct {
	Steps []ConcurrencyStep `json:"steps"`
	Peak  int               `json:"peak"`
	Mean  float64           `json:"mean"`
}

type ConcurrencyStep struct {
	T time.Time `json:"t"`
	N int       `json:"n"`
}

// interval is [start, end), clipped to a window.
type interval struct{ start, end time.Time }

func (iv interval) seconds() float64 { return iv.end.Sub(iv.start).Seconds() }

// clip is [start, end) within w, end nil meaning now, and whether any of
// it is in w; live is whether it is still going.
func clip(w activity.Window, now time.Time, start time.Time, end *time.Time) (iv interval, live, ok bool) {
	e := now
	if end != nil {
		e = *end
	} else {
		live = true
	}
	if start.Before(w.Since) {
		start = w.Since
	}
	if e.After(w.Until) {
		e = w.Until
	}
	if e.Before(start) {
		return interval{}, false, false
	}
	return interval{start, e}, live, true
}

// union is the seconds covered by ivs.
func union(ivs []interval) float64 {
	var total float64
	for _, iv := range merged(ivs) {
		total += iv.seconds()
	}
	return total
}

// merged are ivs sorted and with overlapping ones joined.
func merged(ivs []interval) []interval {
	ivs = slices.Clone(ivs)
	slices.SortFunc(ivs, func(a, b interval) int { return a.start.Compare(b.start) })
	var out []interval
	for _, iv := range ivs {
		if n := len(out); n > 0 && !iv.start.After(out[n-1].end) {
			if iv.end.After(out[n-1].end) {
				out[n-1].end = iv.end
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

// BuildTimeline lays out a's runs and threads, with checks, the flows'
// checks still running, at now.
func BuildTimeline(a *activity.Activity, checks []runs.CheckRecord, now time.Time) *Timeline {
	w := a.Window
	if now.After(w.Until) {
		now = w.Until
	}
	t := &Timeline{Window: w, Now: now, Runs: a.Runs, Actors: []ActorLane{}, Flows: []FlowLane{}, Incomplete: a.Incomplete}

	runIv := map[string]interval{}
	runLive := map[string]bool{}
	for _, r := range a.Runs {
		if iv, live, ok := clip(w, now, r.Created, r.Ended); ok {
			runIv[r.ID], runLive[r.ID] = iv, live
		}
	}

	// Lanes by actor, each run in the first sub-row it fits.
	byActor := map[string][]activity.Run{}
	for _, r := range a.Runs {
		if _, ok := runIv[r.ID]; ok {
			byActor[r.Actor] = append(byActor[r.Actor], r)
		}
	}
	for actor, rs := range byActor {
		lane := ActorLane{Actor: actor, Rows: [][]string{}, Runs: len(rs)}
		var rowEnds []time.Time
		var ivs []interval
		for _, r := range rs { // oldest first, as activity orders them
			iv := runIv[r.ID]
			ivs = append(ivs, iv)
			row := slices.IndexFunc(rowEnds, func(end time.Time) bool { return !iv.start.Before(end) })
			if row < 0 {
				row = len(rowEnds)
				rowEnds = append(rowEnds, iv.end)
				lane.Rows = append(lane.Rows, nil)
			}
			rowEnds[row] = iv.end
			lane.Rows[row] = append(lane.Rows[row], r.ID)
		}
		lane.Busy = union(ivs)
		t.Actors = append(t.Actors, lane)
	}
	slices.SortFunc(t.Actors, func(x, y ActorLane) int { return strings.Compare(x.Actor, y.Actor) })

	// Lanes by flow thread.
	liveChecks := map[string][]runs.CheckRecord{}
	for _, c := range checks {
		key := activity.ThreadKey(c.Issue, c.PR, "")
		liveChecks[key] = append(liveChecks[key], c)
	}
	threads := slices.Clone(a.Threads)
	for key, cs := range liveChecks {
		if !slices.ContainsFunc(threads, func(th activity.Thread) bool { return th.Key == key }) {
			threads = append(threads, activity.Thread{Key: key, Issue: cs[0].Issue, PR: cs[0].PR})
		}
	}
	for _, th := range threads {
		if lane, ok := flowLane(w, now, th, runIv, runLive, liveChecks[th.Key]); ok {
			t.Flows = append(t.Flows, lane)
		}
	}
	slices.SortFunc(t.Flows, func(x, y FlowLane) int {
		return cmp.Or(x.Segments[0].Start.Compare(y.Segments[0].Start), strings.Compare(x.Key, y.Key))
	})

	t.Concurrency = concurrency(w, a.Runs, runIv)
	return t
}

// flowLane is th's lane, and whether it has anything in w. Its gaps are
// the time between its first and last segment that none covers.
func flowLane(w activity.Window, now time.Time, th activity.Thread, runIv map[string]interval, runLive map[string]bool, live []runs.CheckRecord) (FlowLane, bool) {
	lane := FlowLane{Key: th.Key, Issue: th.Issue, PR: th.PR, Title: th.Title, Segments: []Segment{}}
	var agent, e2e, owner []interval
	for _, id := range th.Runs {
		iv, ok := runIv[id]
		if !ok {
			continue
		}
		lane.Runs++
		agent = append(agent, iv)
		lane.Segments = append(lane.Segments, Segment{Kind: SegmentRun, Start: iv.start, End: iv.end, Live: runLive[id], RunID: id})
	}
	for _, c := range th.Checks {
		start := c.End
		if c.Start != nil {
			start = *c.Start
		}
		iv, _, ok := clip(w, now, start, &c.End)
		if !ok {
			continue
		}
		e2e = append(e2e, iv)
		lane.Segments = append(lane.Segments, Segment{Kind: SegmentE2E, Start: iv.start, End: iv.end, State: c.State, SHA: c.SHA, Description: c.Description, Derived: c.Derived})
	}
	for _, c := range live {
		iv, isLive, ok := clip(w, now, c.Started, nil)
		if !ok {
			continue
		}
		e2e = append(e2e, iv)
		lane.Segments = append(lane.Segments, Segment{Kind: SegmentE2E, Start: iv.start, End: iv.end, Live: isLive, State: "running", SHA: c.SHA})
	}
	for _, ow := range th.OwnerWaits {
		iv, isLive, ok := clip(w, now, ow.Start, ow.End)
		if !ok {
			continue
		}
		owner = append(owner, iv)
		lane.Segments = append(lane.Segments, Segment{Kind: SegmentOwnerWait, Start: iv.start, End: iv.end, Live: isLive, Reason: string(ow.Reason), EndedBy: ow.EndedBy})
	}
	if len(lane.Segments) == 0 {
		return lane, false
	}
	covered := merged(slices.Concat(agent, e2e, owner))
	for i := 1; i < len(covered); i++ {
		gap := interval{covered[i-1].end, covered[i].start}
		lane.Overhead += gap.seconds()
		lane.Segments = append(lane.Segments, Segment{Kind: SegmentOverhead, Start: gap.start, End: gap.end})
	}
	lane.Agent, lane.E2E, lane.OwnerWait = union(agent), union(e2e), union(owner)
	slices.SortStableFunc(lane.Segments, func(x, y Segment) int { return x.Start.Compare(y.Start) })
	return lane, true
}

// concurrency counts the batch runs live at each moment of w.
func concurrency(w activity.Window, rs []activity.Run, runIv map[string]interval) Concurrency {
	type edge struct {
		t time.Time
		d int
	}
	var edges []edge
	for _, r := range rs {
		iv, ok := runIv[r.ID]
		if !ok || r.Interactive {
			continue
		}
		edges = append(edges, edge{iv.start, 1}, edge{iv.end, -1})
	}
	slices.SortFunc(edges, func(x, y edge) int { return cmp.Or(x.t.Compare(y.t), cmp.Compare(x.d, y.d)) })
	c := Concurrency{Steps: []ConcurrencyStep{{T: w.Since, N: 0}}}
	n := 0
	var area float64
	for _, e := range edges {
		last := &c.Steps[len(c.Steps)-1]
		area += float64(last.N) * e.t.Sub(last.T).Seconds()
		n += e.d
		if e.t.Equal(last.T) {
			last.N = n
		} else {
			c.Steps = append(c.Steps, ConcurrencyStep{T: e.t, N: n})
		}
		c.Peak = max(c.Peak, n)
	}
	last := c.Steps[len(c.Steps)-1]
	area += float64(last.N) * w.Until.Sub(last.T).Seconds()
	if d := w.Until.Sub(w.Since).Seconds(); d > 0 {
		c.Mean = area / d
	}
	return c
}

// handleGetTimeline serves GET /api/timeline?since=&until= (RFC 3339; since
// defaults to six hours before until, and until to now).
func (s *Server) handleGetTimeline(w http.ResponseWriter, r *http.Request) {
	win, err := timelineWindow(r, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a, err := s.ComputeActivity(r.Context(), win)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	tl := BuildTimeline(a, s.liveCheckRecords(), time.Now())
	tl.Open = r.URL.Query().Get("until") == ""
	writeJSON(w, http.StatusOK, tl)
}

// timelineWindow is the window a timeline request asks for: six hours to
// now, unless the request gives since or until.
func timelineWindow(r *http.Request, now time.Time) (activity.Window, error) {
	return window(r, now, 6*time.Hour)
}
