// Package stats derives the owner-facing statistics view (issue #319) from
// the shared activity derivation (internal/activity) and the forge: a
// per-issue/PR metric vector, that vector grouped into breakdowns by
// implementer actor, family, verifier family, pairing, size and milestone,
// the size labels' estimate against the measured diff, and each
// milestone's burndown and burnup.
//
// It follows the ground rules of #309: every number carries its coverage,
// missing data stays missing rather than becoming zero, tokens are never
// added up across model families, and a group reports its n. Nothing here
// is a store of its own: it reads what it is given and keeps nothing.
package stats

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/diffstat"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// Facts is what Compute needs beyond the shared activity derivation: the
// repository's issues (for their size label, milestone and created time)
// and the merged PRs' diffs (for the measured size).
type Facts struct {
	// Issues are the repository's issues (not PRs) in every state
	// (GET /v1/forge/issues?state=all).
	Issues []*server.IssueItem
	// Diffs are merged PRs' unified diffs by PR number; a missing one
	// leaves that PR's measured size unknown.
	Diffs map[int64]string
}

// Stats is what Compute derives for a window.
type Stats struct {
	Window activity.Window `json:"window"`
	Now    time.Time       `json:"now"`
	// Issues is the per-issue/PR vector, one entry per flow thread with
	// something in the window, ordered by issue then PR.
	Issues []Issue `json:"issues"`
	// Breakdowns group Issues by each dimension.
	Breakdowns []Breakdown `json:"breakdowns"`
	// Sizes is the estimate-vs-measured confusion table, one row per size
	// label (and one for unlabelled issues).
	Sizes []SizeRow `json:"sizes"`
	// Milestones is each milestone's burndown and burnup over time.
	Milestones []Milestone `json:"milestones"`
	// Incomplete names the reads that failed, whose facts are missing.
	Incomplete []string `json:"incomplete,omitempty"`
}

// Issue is the metric vector of one issue/PR: its size and milestone, its
// lead and cycle times, the time its agents spent by kind, its review and
// check rounds, its verifier rejections and failed runs, the owner's
// observable touches, its usage by family and its measured size.
type Issue struct {
	Issue     int64  `json:"issue,omitempty"`
	PR        int64  `json:"pr,omitempty"`
	Title     string `json:"title,omitempty"`
	Size      string `json:"size,omitempty"`      // S, M, L or XL; empty when unlabelled
	Milestone string `json:"milestone,omitempty"` // the milestone's title
	// Implementer is the actor of the thread's last implement run, and
	// Verifier the actor of its last verify run; their families are for
	// the breakdowns. Empty when the thread had no such run.
	Implementer       string `json:"implementer,omitempty"`
	ImplementerFamily string `json:"implementer_family,omitempty"`
	Verifier          string `json:"verifier,omitempty"`
	VerifierFamily    string `json:"verifier_family,omitempty"`

	Created *time.Time `json:"created,omitempty"` // the issue's creation
	Merged  *time.Time `json:"merged,omitempty"`  // the PR's merge
	// LeadSeconds is Created to Merged, CycleSeconds the thread's first
	// run to Merged; both nil until the PR is merged.
	LeadSeconds  *float64 `json:"lead_seconds,omitempty"`
	CycleSeconds *float64 `json:"cycle_seconds,omitempty"`

	Runs         int                `json:"runs"`
	AgentSeconds float64            `json:"agent_seconds"`
	AgentByKind  map[string]float64 `json:"agent_by_kind,omitempty"`

	ImplementRounds    int            `json:"implement_rounds"`
	CheckRounds        int            `json:"check_rounds"`
	VerifierRejections int            `json:"verifier_rejections"`
	RejectionsByFamily map[string]int `json:"rejections_by_family,omitempty"`
	FailedRuns         int            `json:"failed_runs"`
	FailuresByCause    map[string]int `json:"failures_by_cause,omitempty"`

	// OwnerTouches are what the records show of the owner's own work:
	// interactive runs, and the flow's stops (each a decision or resume).
	// Owner comments and commits are not counted yet.
	InteractiveRuns int `json:"interactive_runs"`
	Stops           int `json:"stops"`

	Usage    []FamilyUsage `json:"usage,omitempty"`
	Measured *Measured     `json:"measured,omitempty"`
}

// FamilyUsage is a thread's reported usage, by model family. Tokens are
// summed only over the runs of one family that reported them; a field is
// nil when none did. Runs and Reported are the coverage ("Reported of Runs
// reported").
type FamilyUsage struct {
	Family     string   `json:"family,omitempty"`
	Runs       int      `json:"runs"`
	Reported   int      `json:"reported"`
	Input      *int64   `json:"input_tokens,omitempty"`
	Output     *int64   `json:"output_tokens,omitempty"`
	CacheRead  *int64   `json:"cache_read_tokens,omitempty"`
	CacheWrite *int64   `json:"cache_write_tokens,omitempty"`
	CostUSD    *float64 `json:"cost_usd,omitempty"`
}

// Measured is a merged PR's measured size: the added and deleted lines and
// the files touched, lock files excluded (internal/diffstat).
type Measured struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
	Files   int `json:"files"`
}

// Size labels and their rough weights, used only to weigh a milestone's
// burn chart when every issue has one; sizes are never converted into
// points for the vector.
const (
	SizeS  = "S"
	SizeM  = "M"
	SizeL  = "L"
	SizeXL = "XL"
)

var sizeWeights = map[string]float64{SizeS: 1, SizeM: 2, SizeL: 4, SizeXL: 8}

// SizeLabel is the size a label names, or empty: "size/S" gives S.
func SizeLabel(label string) string {
	name, ok := strings.CutPrefix(label, "size/")
	if !ok {
		return ""
	}
	switch name {
	case SizeS, SizeM, SizeL, SizeXL:
		return name
	}
	return ""
}

// issueMeta is an issue's facts for the vector and the burn charts.
type issueMeta struct {
	size      string
	milestone string
	created   *time.Time
	closed    *time.Time
}

// Compute derives the statistics for a's window from a and f, at now.
func Compute(a *activity.Activity, f Facts, now time.Time) *Stats {
	w := a.Window
	st := &Stats{Window: w, Now: now, Issues: []Issue{}, Breakdowns: []Breakdown{}, Sizes: []SizeRow{}, Milestones: []Milestone{}, Incomplete: slices.Clone(a.Incomplete)}

	meta := make(map[int64]issueMeta, len(f.Issues))
	for _, is := range f.Issues {
		if is == nil || is.Number <= 0 || is.PullRequest {
			continue
		}
		var created *time.Time
		if !is.Created.IsZero() {
			created = &is.Created
		}
		m := issueMeta{milestone: milestoneTitle(is), created: created, closed: is.Closed}
		for _, l := range is.Labels {
			if s := SizeLabel(l); s != "" {
				m.size = s
			}
		}
		meta[is.Number] = m
	}

	runs := map[string][]activity.Run{}
	for _, r := range a.Runs {
		runs[r.ThreadKey] = append(runs[r.ThreadKey], r)
	}

	for _, th := range a.Threads {
		st.Issues = append(st.Issues, issueVector(th, runs[th.Key], meta[th.Issue], f.Diffs[th.PR]))
	}
	slices.SortFunc(st.Issues, func(x, y Issue) int {
		return cmp.Or(cmp.Compare(x.Issue, y.Issue), cmp.Compare(x.PR, y.PR))
	})

	st.Breakdowns = breakdowns(st.Issues)
	st.Sizes = sizeTable(st.Issues)
	st.Milestones = burn(f.Issues, w)
	return st
}

// milestoneTitle is is's milestone title, or empty.
func milestoneTitle(is *server.IssueItem) string {
	if is.Milestone == nil {
		return ""
	}
	return is.Milestone.Title
}

// issueVector is the metric vector of one thread and its runs.
func issueVector(th activity.Thread, runs []activity.Run, m issueMeta, diff string) Issue {
	v := Issue{
		Issue: th.Issue, PR: th.PR, Title: th.Title,
		Size: m.size, Milestone: m.milestone,
		Created: m.created, Merged: th.PRMerged,
		Runs: len(runs), ImplementRounds: th.ImplementRounds, CheckRounds: th.CheckRounds,
		Stops: len(th.Stops),
	}
	if v.Created == nil {
		v.Created = m.created
	}

	var first *time.Time
	for _, r := range runs {
		if first == nil || r.Created.Before(*first) {
			t := r.Created
			first = &t
		}
		switch r.Kind {
		case "implement":
			v.Implementer, v.ImplementerFamily = r.Actor, r.Family
		case "verify":
			v.Verifier, v.VerifierFamily = r.Actor, r.Family
			if r.Outcome == secrets.OutcomeChangesRequested {
				v.VerifierRejections++
				v.RejectionsByFamily = incr(v.RejectionsByFamily, r.Family)
			}
		}
		if r.Interactive {
			v.InteractiveRuns++
		}
		if r.Ended != nil {
			secs := r.Ended.Sub(r.Created).Seconds()
			if secs > 0 {
				v.AgentSeconds += secs
				if v.AgentByKind == nil {
					v.AgentByKind = map[string]float64{}
				}
				v.AgentByKind[r.Kind] += secs
			}
		}
		if r.Status == "failed" {
			v.FailedRuns++
			v.FailuresByCause = incr(v.FailuresByCause, cmp.Or(r.Failure, "unknown"))
		}
	}
	v.Usage = usageByFamily(runs)

	if v.Merged != nil {
		if v.Created != nil {
			v.LeadSeconds = secondsBetween(*v.Created, *v.Merged)
		}
		if first != nil {
			v.CycleSeconds = secondsBetween(*first, *v.Merged)
		}
	}
	if v.PR > 0 && diff != "" {
		s := diffstat.Sum(diffstat.Parse(diff))
		v.Measured = &Measured{Added: s.Added, Deleted: s.Deleted, Files: s.Files}
	}
	return v
}

// usageByFamily sums runs' reported usage per model family, oldest family
// first.
func usageByFamily(runs []activity.Run) []FamilyUsage {
	order := []string{}
	byFamily := map[string]*FamilyUsage{}
	for _, r := range runs {
		fam := r.Family
		u := byFamily[fam]
		if u == nil {
			u = &FamilyUsage{Family: fam}
			byFamily[fam] = u
			order = append(order, fam)
		}
		u.Runs++
		if r.UsageReported {
			u.Reported++
		}
		sumUsage(u, r.Usage)
	}
	out := make([]FamilyUsage, 0, len(order))
	for _, fam := range order {
		out = append(out, *byFamily[fam])
	}
	return out
}

// sumUsage adds u's reported figures to fu; a figure u did not report is
// left alone, never treated as zero.
func sumUsage(fu *FamilyUsage, u *secrets.Usage) {
	if u == nil {
		return
	}
	fu.Input = addInt(fu.Input, u.InputTokens)
	fu.Output = addInt(fu.Output, u.OutputTokens)
	fu.CacheRead = addInt(fu.CacheRead, u.CacheReadTokens)
	fu.CacheWrite = addInt(fu.CacheWrite, u.CacheWriteTokens)
	fu.CostUSD = addFloat(fu.CostUSD, u.CostUSD)
}

func addInt(sum *int64, v *int64) *int64 {
	if v == nil {
		return sum
	}
	n := *v
	if sum != nil {
		n += *sum
	}
	return &n
}

func addFloat(sum *float64, v *float64) *float64 {
	if v == nil {
		return sum
	}
	n := *v
	if sum != nil {
		n += *sum
	}
	return &n
}

func secondsBetween(a, b time.Time) *float64 {
	s := b.Sub(a).Seconds()
	return &s
}

func incr(m map[string]int, k string) map[string]int {
	if m == nil {
		m = map[string]int{}
	}
	m[k]++
	return m
}

// medianMinMax is the median, minimum and maximum of xs, and how many
// values there were. Empty input gives a zero Measure.
func medianMinMax(xs []float64) Measure {
	m := Measure{N: len(xs)}
	if len(xs) == 0 {
		return m
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	median := s[len(s)/2]
	if len(s)%2 == 0 {
		median = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	lo, hi := s[0], s[len(s)-1]
	m.Median, m.Min, m.Max = &median, &lo, &hi
	return m
}
