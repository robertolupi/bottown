package webui

import (
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/runs"
)

var tl0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// th is tl0 plus h hours.
func th(h float64) time.Time { return tl0.Add(time.Duration(h * float64(time.Hour))) }

func thp(h float64) *time.Time { t := th(h); return &t }

func tlRun(id, actor, key string, created float64, ended *time.Time) activity.Run {
	return activity.Run{ID: id, Kind: "implement", Actor: actor, Created: th(created), Ended: ended, ThreadKey: key}
}

func TestTimelineActorLanes(t *testing.T) {
	a := &activity.Activity{
		Window: activity.Window{Since: th(0), Until: th(10)},
		Runs: []activity.Run{
			tlRun("r1", "claude", "k1", -1, thp(2)), // starts before the window
			tlRun("r2", "claude", "k2", 1, thp(3)),  // overlaps r1: a second row
			tlRun("r3", "claude", "k3", 4, thp(5)),  // fits the first row again
			tlRun("r4", "codex", "k4", 8, nil),      // live
		},
	}
	tl := BuildTimeline(a, nil, th(9))
	if len(tl.Actors) != 2 || tl.Actors[0].Actor != "claude" || tl.Actors[1].Actor != "codex" {
		t.Fatalf("actors = %+v", tl.Actors)
	}
	c := tl.Actors[0]
	if want := [][]string{{"r1", "r3"}, {"r2"}}; !slices.EqualFunc(c.Rows, want, slices.Equal) {
		t.Errorf("rows = %v, want %v", c.Rows, want)
	}
	// [0,3) and [4,5): the window clips r1, and r1 and r2 count once.
	if c.Busy != 4*3600 || c.Runs != 3 {
		t.Errorf("claude busy = %v runs = %d", c.Busy, c.Runs)
	}
	// A live run ends at now.
	if x := tl.Actors[1]; x.Busy != 3600 {
		t.Errorf("codex busy = %v", x.Busy)
	}
}

func TestTimelineFlowSegments(t *testing.T) {
	key := activity.ThreadKey(7, 9, "")
	a := &activity.Activity{
		Window: activity.Window{Since: th(0), Until: th(24)},
		Runs: []activity.Run{
			tlRun("impl", "claude", key, 1, thp(2)),
			tlRun("ver", "codex", key, 4, thp(5)),
		},
		Threads: []activity.Thread{{
			Key: key, Issue: 7, PR: 9, Title: "a thing",
			Runs: []string{"impl", "ver"},
			Checks: []activity.Check{
				{SHA: "abc", State: "success", Start: thp(2.5), End: th(3.5), Derived: true},
				// No duration in its description: a point at its end.
				{SHA: "def", State: "failure", End: th(6)},
			},
			OwnerWaits: []activity.OwnerWait{{Start: th(5), End: thp(8), EndedBy: activity.EndedByMerge, Reason: "ready to merge"}},
		}},
	}
	tl := BuildTimeline(a, nil, th(20))
	if len(tl.Flows) != 1 {
		t.Fatalf("flows = %+v", tl.Flows)
	}
	f := tl.Flows[0]
	var kinds []string
	for _, s := range f.Segments {
		kinds = append(kinds, s.Kind)
	}
	want := []string{SegmentRun, SegmentOverhead, SegmentE2E, SegmentOverhead, SegmentRun, SegmentOwnerWait, SegmentE2E}
	if !slices.Equal(kinds, want) {
		t.Fatalf("segments = %v, want %v", kinds, want)
	}
	if f.Agent != 2*3600 || f.E2E != 3600 || f.OwnerWait != 3*3600 || f.Overhead != 3600 || f.Runs != 2 || f.Title != "a thing" {
		t.Errorf("lane = %+v", f)
	}
	if s := f.Segments[6]; !s.Start.Equal(s.End) || s.Derived {
		t.Errorf("check without a duration = %+v", s)
	}
	if s := f.Segments[5]; s.Reason != "ready to merge" || s.EndedBy != activity.EndedByMerge || s.Live {
		t.Errorf("owner wait = %+v", s)
	}
}

func TestTimelineLiveSegmentsAndChecks(t *testing.T) {
	key := activity.ThreadKey(7, 9, "")
	a := &activity.Activity{
		Window: activity.Window{Since: th(0), Until: th(10)},
		Runs:   []activity.Run{tlRun("impl", "claude", key, 1, thp(2))},
		Threads: []activity.Thread{{
			Key: key, Issue: 7, PR: 9, Runs: []string{"impl"},
			OwnerWaits: []activity.OwnerWait{{Start: th(-5), Reason: "blocked"}},
		}},
	}
	checks := []runs.CheckRecord{
		{ID: "flow-7-abc.e2e", Issue: 7, PR: 9, SHA: "abc", Started: th(3)},
		// A check on a thread with nothing else in the window gets a lane.
		{ID: "flow-8-def.e2e", Issue: 8, PR: 11, SHA: "def", Started: th(4)},
	}
	tl := BuildTimeline(a, checks, th(6))
	if len(tl.Flows) != 2 || tl.Flows[0].Key != key || tl.Flows[1].Key != activity.ThreadKey(8, 11, "") {
		t.Fatalf("flows = %+v", tl.Flows)
	}
	f := tl.Flows[0]
	// The wait began before the window and goes on: clipped, live.
	ow := f.Segments[0]
	if ow.Kind != SegmentOwnerWait || !ow.Start.Equal(th(0)) || !ow.End.Equal(th(6)) || !ow.Live {
		t.Errorf("wait = %+v", ow)
	}
	var check *Segment
	for i := range f.Segments {
		if f.Segments[i].Kind == SegmentE2E {
			check = &f.Segments[i]
		}
	}
	if check == nil || !check.Live || check.State != "running" || !check.End.Equal(th(6)) {
		t.Errorf("live check = %+v", check)
	}
	// A window that has ended stops live intervals at its end.
	a.Window = activity.Window{Since: th(0), Until: th(5)}
	tl = BuildTimeline(a, nil, th(6))
	if s := tl.Flows[0].Segments[0]; !s.End.Equal(th(5)) || !s.Live {
		t.Errorf("wait in an ended window = %+v", s)
	}
}

func TestTimelineConcurrency(t *testing.T) {
	inter := tlRun("i", "claude", "k", 0, thp(10))
	inter.Interactive = true
	a := &activity.Activity{
		Window: activity.Window{Since: th(0), Until: th(10)},
		Runs: []activity.Run{
			tlRun("a", "claude", "k", 1, thp(3)),
			tlRun("b", "codex", "k", 2, thp(4)),
			tlRun("c", "agy", "k", 4, thp(5)), // starts as b ends
			inter,                             // not counted
		},
	}
	c := BuildTimeline(a, nil, th(10)).Concurrency
	want := []ConcurrencyStep{{th(0), 0}, {th(1), 1}, {th(2), 2}, {th(3), 1}, {th(4), 1}, {th(5), 0}}
	if !slices.EqualFunc(c.Steps, want, func(x, y ConcurrencyStep) bool { return x.T.Equal(y.T) && x.N == y.N }) {
		t.Errorf("steps = %v, want %v", c.Steps, want)
	}
	// 1+2+1+1 run-hours over 10 hours.
	if c.Peak != 2 || c.Mean != 0.5 {
		t.Errorf("peak = %d mean = %v", c.Peak, c.Mean)
	}
}

func TestTimelineWindow(t *testing.T) {
	now := th(100)
	req := func(q string) (activity.Window, error) {
		return timelineWindow(httptest.NewRequest("GET", "/api/timeline"+q, nil), now)
	}
	w, err := req("")
	if err != nil || !w.Until.Equal(now) || !w.Since.Equal(th(94)) {
		t.Errorf("default = %+v, %v", w, err)
	}
	w, err = req("?since=" + th(1).Format(time.RFC3339) + "&until=" + th(2).Format(time.RFC3339))
	if err != nil || !w.Since.Equal(th(1)) || !w.Until.Equal(th(2)) {
		t.Errorf("explicit = %+v, %v", w, err)
	}
	for _, q := range []string{"?since=yesterday", "?until=x", "?since=" + th(101).Format(time.RFC3339)} {
		if _, err := req(q); err == nil {
			t.Errorf("%s: no error", q)
		}
	}
}
