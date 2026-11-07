package stats

import (
	"slices"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/server"
)

// Milestone is one milestone's burndown and burnup over the window: its
// scope, how much of it is closed, and the series the charts draw.
type Milestone struct {
	Title  string `json:"title"`
	Total  int    `json:"total"`
	Closed int    `json:"closed"`
	// Weighted: every issue in the milestone has a size label, so the
	// series carry size weights. Otherwise the series count issues.
	Weighted bool        `json:"weighted"`
	Points   []BurnPoint `json:"points"`
}

// BurnPoint is the milestone's state at t: its scope (Total), how much is
// closed (Closed) and open (Open), and the size-weighted equivalents when
// Weighted. Scope added late shows as a rising Total.
type BurnPoint struct {
	T           time.Time `json:"t"`
	Total       int       `json:"total"`
	Closed      int       `json:"closed"`
	Open        int       `json:"open"`
	TotalWeight float64   `json:"total_weight,omitempty"`
	OpenWeight  float64   `json:"open_weight,omitempty"`
}

// burnItem is one issue's contribution to a milestone's burn.
type burnItem struct {
	created *time.Time
	closed  *time.Time
	weight  float64
	sized   bool
}

// burn derives each milestone's burndown and burnup over w, by milestone
// title.
func burn(issues []*server.IssueItem, w activity.Window) []Milestone {
	groups := map[string][]burnItem{}
	order := []string{}
	for _, is := range issues {
		if is == nil || is.Number <= 0 || is.PullRequest || is.Milestone == nil || is.Milestone.Title == "" {
			continue
		}
		title := is.Milestone.Title
		if _, ok := groups[title]; !ok {
			order = append(order, title)
		}
		it := burnItem{closed: is.Closed}
		if !is.Created.IsZero() {
			c := is.Created
			it.created = &c
		}
		if sz := sizeOf(is.Labels); sz != "" {
			it.weight, it.sized = sizeWeight(sz)
		}
		groups[title] = append(groups[title], it)
	}
	slices.Sort(order)
	out := make([]Milestone, 0, len(order))
	for _, title := range order {
		out = append(out, milestoneBurn(title, groups[title], w))
	}
	return out
}

// milestoneBurn is title's burndown and burnup over w.
func milestoneBurn(title string, items []burnItem, w activity.Window) Milestone {
	m := Milestone{Title: title, Total: len(items), Weighted: true, Points: []BurnPoint{}}
	for _, it := range items {
		if it.closed != nil {
			m.Closed++
		}
		if !it.sized {
			m.Weighted = false
		}
	}
	var events []time.Time
	inWindow := func(t *time.Time) bool { return t != nil && !t.Before(w.Since) && t.Before(w.Until) }
	for _, it := range items {
		if inWindow(it.created) {
			events = append(events, *it.created)
		}
		if inWindow(it.closed) {
			events = append(events, *it.closed)
		}
	}
	slices.SortFunc(events, func(a, b time.Time) int { return a.Compare(b) })
	events = slices.Compact(events)
	times := append([]time.Time{w.Since}, events...)
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	times = slices.Compact(times)
	if last := times[len(times)-1]; last.Before(w.Until) {
		times = append(times, w.Until)
	}
	for _, t := range times {
		m.Points = append(m.Points, burnPoint(items, t, m.Weighted))
	}
	return m
}

// burnPoint is items' state at t.
func burnPoint(items []burnItem, t time.Time, weighted bool) BurnPoint {
	p := BurnPoint{T: t}
	for _, it := range items {
		if it.created == nil || it.created.After(t) {
			continue
		}
		p.Total++
		open := true
		if it.closed != nil && !it.closed.After(t) {
			p.Closed++
			open = false
		}
		if weighted {
			p.TotalWeight += it.weight
			if open {
				p.OpenWeight += it.weight
			}
		}
	}
	p.Open = p.Total - p.Closed
	return p
}

// sizeOf is the size a label set names, or empty.
func sizeOf(labels []string) string {
	for _, l := range labels {
		if s := SizeLabel(l); s != "" {
			return s
		}
	}
	return ""
}
