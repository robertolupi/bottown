package stats

import (
	"slices"
)

// Measure is a distribution over a group: how many values it had (n), and
// their median, minimum and maximum. It is empty (all nil) when n is zero,
// so missing data is never a zero.
type Measure struct {
	N      int      `json:"n"`
	Median *float64 `json:"median,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
}

// Cell is one group of a breakdown: its key, its n, and the same measures
// for each metric.
type Cell struct {
	Key             string  `json:"key"`
	N               int     `json:"n"`
	LeadSeconds     Measure `json:"lead_seconds"`
	CycleSeconds    Measure `json:"cycle_seconds"`
	AgentSeconds    Measure `json:"agent_seconds"`
	ImplementRounds Measure `json:"implement_rounds"`
	CheckRounds     Measure `json:"check_rounds"`
	Rejections      Measure `json:"verifier_rejections"`
}

// Breakdown is the vector grouped by one dimension.
type Breakdown struct {
	By    string `json:"by"`
	Cells []Cell `json:"cells"`
}

// breakdowns groups issues by each dimension. A cell's key is empty for
// the issues that dimension does not cover (no implementer, no size, no
// milestone), which the view names; it is never dropped.
func breakdowns(issues []Issue) []Breakdown {
	dims := []struct {
		by  string
		key func(Issue) string
	}{
		{"actor", func(v Issue) string { return v.Implementer }},
		{"family", func(v Issue) string { return v.ImplementerFamily }},
		{"verifier_family", func(v Issue) string { return v.VerifierFamily }},
		{"pair", pair},
		{"size", func(v Issue) string { return v.Size }},
		{"milestone", func(v Issue) string { return v.Milestone }},
	}
	out := make([]Breakdown, 0, len(dims))
	for _, d := range dims {
		order := []string{}
		groups := map[string][]Issue{}
		for _, v := range issues {
			k := d.key(v)
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], v)
		}
		slices.Sort(order)
		b := Breakdown{By: d.by, Cells: make([]Cell, 0, len(order))}
		for _, k := range order {
			b.Cells = append(b.Cells, cell(k, groups[k]))
		}
		out = append(out, b)
	}
	return out
}

// pair is an issue's implementer and verifier families, "impl/ver".
func pair(v Issue) string {
	if v.ImplementerFamily == "" || v.VerifierFamily == "" {
		return ""
	}
	return v.ImplementerFamily + "/" + v.VerifierFamily
}

// cell is the measures of one group of issues.
func cell(key string, vs []Issue) Cell {
	c := Cell{Key: key, N: len(vs)}
	var lead, cycle, agent, impl, check, rej []float64
	for _, v := range vs {
		if v.LeadSeconds != nil {
			lead = append(lead, *v.LeadSeconds)
		}
		if v.CycleSeconds != nil {
			cycle = append(cycle, *v.CycleSeconds)
		}
		agent = append(agent, v.AgentSeconds)
		impl = append(impl, float64(v.ImplementRounds))
		check = append(check, float64(v.CheckRounds))
		rej = append(rej, float64(v.VerifierRejections))
	}
	c.LeadSeconds = medianMinMax(lead)
	c.CycleSeconds = medianMinMax(cycle)
	c.AgentSeconds = medianMinMax(agent)
	c.ImplementRounds = medianMinMax(impl)
	c.CheckRounds = medianMinMax(check)
	c.Rejections = medianMinMax(rej)
	return c
}

// Measured-bucket boundaries, by changed lines (added + deleted, lock files
// excluded). Approximate, and only for the estimate-vs-measured table;
// sizes are never converted into points.
const (
	measuredS = 20
	measuredM = 100
	measuredL = 400
)

// measuredBucket is the bucket a measured line count falls in, one of S, M,
// L, XL.
func measuredBucket(lines int) string {
	switch {
	case lines <= measuredS:
		return SizeS
	case lines <= measuredM:
		return SizeM
	case lines <= measuredL:
		return SizeL
	default:
		return SizeXL
	}
}

// SizeRow is one size label's measured outcomes: how many issues carried
// the label, how many had a measured size, and how those fell into the
// measured buckets.
type SizeRow struct {
	Size     string       `json:"size"` // S, M, L, XL, or empty for unlabelled
	N        int          `json:"n"`
	Measured int          `json:"measured"` // issues with a measured size
	Buckets  []SizeBucket `json:"buckets"`
}

// SizeBucket is one measured bucket's count.
type SizeBucket struct {
	Bucket string `json:"bucket"`
	N      int    `json:"n"`
}

// sizeTable is the estimate-vs-measured confusion table: a row per size
// label (S, M, L, XL, and unlabelled), and a column per measured bucket.
func sizeTable(issues []Issue) []SizeRow {
	rows := []SizeRow{}
	for _, size := range []string{SizeS, SizeM, SizeL, SizeXL, ""} {
		row := SizeRow{Size: size, Buckets: make([]SizeBucket, 0, 4)}
		counts := map[string]int{SizeS: 0, SizeM: 0, SizeL: 0, SizeXL: 0}
		for _, v := range issues {
			if v.Size != size {
				continue
			}
			row.N++
			if v.Measured == nil {
				continue
			}
			row.Measured++
			counts[measuredBucket(v.Measured.Added+v.Measured.Deleted)]++
		}
		for _, b := range []string{SizeS, SizeM, SizeL, SizeXL} {
			row.Buckets = append(row.Buckets, SizeBucket{Bucket: b, N: counts[b]})
		}
		rows = append(rows, row)
	}
	return rows
}

// sizeWeight is a size label's rough weight for the burn chart, and whether
// the label is known.
func sizeWeight(size string) (float64, bool) {
	w, ok := sizeWeights[size]
	return w, ok
}
