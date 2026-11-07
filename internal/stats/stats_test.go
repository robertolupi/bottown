package stats

import (
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

var t0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func at(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }

func ptr[T any](v T) *T { return &v }

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

func run(id, kind, actor, family string, created, ended float64) activity.Run {
	r := activity.Run{ID: id, Kind: kind, Actor: actor, Family: family, Created: at(created), Status: "succeeded", ThreadKey: "flow:issue:7:pr:9"}
	r.Ended = ptr(at(ended))
	return r
}

func merged(h float64) *time.Time { return ptr(at(h)) }

// sampleActivity is one merged thread: five runs, a rejection, a failure,
// an interactive run, usage on one run, and rounds.
func sampleActivity() *activity.Activity {
	impl := run("r1", "implement", "claude", "anthropic", 10, 12)
	impl.Outcome = secrets.OutcomePROpened
	impl.Usage = &secrets.Usage{InputTokens: i64(100), OutputTokens: i64(50), CostUSD: f64(0.5)}
	impl.UsageReported = true

	reject := run("r2", "verify", "codex", "openai", 20, 20.5)
	reject.Outcome = secrets.OutcomeChangesRequested

	approve := run("r3", "verify", "claude", "anthropic", 30, 31)
	approve.Outcome = secrets.OutcomeApproved

	failed := run("r4", "implement", "claude", "anthropic", 40, 41)
	failed.Status, failed.Failure = "failed", "timeout"

	interactive := run("r5", "implement", "claude", "anthropic", 50, 51)
	interactive.Outcome = secrets.OutcomePROpened
	interactive.Interactive = true

	th := activity.Thread{
		Key: "flow:issue:7:pr:9", Issue: 7, PR: 9, Title: "the issue",
		PRMerged: merged(100), ImplementRounds: 1, CheckRounds: 2,
		Runs:  []string{"r1", "r2", "r3", "r4", "r5"},
		Stops: []activity.Stop{{Time: at(60), Reason: "checks_failed"}},
	}
	return &activity.Activity{
		Window:  activity.Window{Since: t0, Until: at(200)},
		Runs:    []activity.Run{impl, reject, approve, failed, interactive},
		Threads: []activity.Thread{th},
	}
}

func sampleFacts() Facts {
	return Facts{
		Issues: []*server.IssueItem{
			{Number: 7, State: "closed", Labels: []string{"size/L", "mode/ask"}, Milestone: &server.MilestoneItem{Title: "M1"}, Created: at(0), Closed: ptr(at(100))},
			{Number: 8, State: "open", Milestone: &server.MilestoneItem{Title: "M1"}, Created: at(50)},
		},
		Diffs: map[int64]string{9: "diff --git a/go.sum b/go.sum\n@@ -1 +1,2 @@\n x\n+y\ndiff --git a/a.go b/a.go\n@@ -1 +1,4 @@\n-a\n-b\n+c\n+d\n+e\n"},
	}
}

func TestIssueVector(t *testing.T) {
	st := Compute(sampleActivity(), sampleFacts(), at(200))
	if len(st.Issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(st.Issues))
	}
	v := st.Issues[0]
	if v.Size != "L" || v.Milestone != "M1" {
		t.Errorf("size/milestone = %q/%q", v.Size, v.Milestone)
	}
	if v.Implementer != "claude" || v.ImplementerFamily != "anthropic" {
		t.Errorf("implementer = %q/%q", v.Implementer, v.ImplementerFamily)
	}
	if v.Verifier != "claude" || v.VerifierFamily != "anthropic" {
		t.Errorf("verifier = %q/%q", v.Verifier, v.VerifierFamily)
	}
	if v.VerifierRejections != 1 || v.RejectionsByFamily["openai"] != 1 {
		t.Errorf("rejections = %d %v", v.VerifierRejections, v.RejectionsByFamily)
	}
	if v.FailedRuns != 1 || v.FailuresByCause["timeout"] != 1 {
		t.Errorf("failures = %d %v", v.FailedRuns, v.FailuresByCause)
	}
	if v.InteractiveRuns != 1 || v.Stops != 1 {
		t.Errorf("owner touches = %d/%d", v.InteractiveRuns, v.Stops)
	}
	if v.ImplementRounds != 1 || v.CheckRounds != 2 {
		t.Errorf("rounds = %d/%d", v.ImplementRounds, v.CheckRounds)
	}
	// 2 + 0.5 + 1 + 1 + 1 = 5.5h.
	if v.AgentSeconds != 5.5*3600 {
		t.Errorf("agent seconds = %v", v.AgentSeconds)
	}
	if v.AgentByKind["implement"] != 4*3600 || v.AgentByKind["verify"] != 1.5*3600 {
		t.Errorf("agent by kind = %v", v.AgentByKind)
	}
	if v.LeadSeconds == nil || *v.LeadSeconds != 100*3600 {
		t.Errorf("lead = %v", v.LeadSeconds)
	}
	if v.CycleSeconds == nil || *v.CycleSeconds != 90*3600 {
		t.Errorf("cycle = %v", v.CycleSeconds)
	}
	if len(v.Usage) != 2 {
		t.Fatalf("usage families = %+v", v.Usage)
	}
	if v.Usage[0].Family != "anthropic" || v.Usage[0].Runs != 4 || v.Usage[0].Reported != 1 {
		t.Errorf("anthropic usage coverage = %+v", v.Usage[0])
	}
	if v.Usage[0].Input == nil || *v.Usage[0].Input != 100 || v.Usage[0].CostUSD == nil || *v.Usage[0].CostUSD != 0.5 {
		t.Errorf("anthropic usage sums = %+v", v.Usage[0])
	}
	if v.Usage[1].Family != "openai" || v.Usage[1].Reported != 0 || v.Usage[1].Input != nil {
		t.Errorf("openai usage = %+v", v.Usage[1])
	}
	if v.Measured == nil || v.Measured.Added != 3 || v.Measured.Deleted != 2 || v.Measured.Files != 1 {
		t.Errorf("measured = %+v", v.Measured)
	}
}

func TestBreakdowns(t *testing.T) {
	st := Compute(sampleActivity(), sampleFacts(), at(200))
	bySize := findBreakdown(t, st, "size")
	if len(bySize.Cells) != 1 || bySize.Cells[0].Key != "L" || bySize.Cells[0].N != 1 {
		t.Errorf("size breakdown = %+v", bySize.Cells)
	}
	if bySize.Cells[0].AgentSeconds.Median == nil || *bySize.Cells[0].AgentSeconds.Median != 5.5*3600 {
		t.Errorf("size agent median = %+v", bySize.Cells[0].AgentSeconds)
	}
	byFamily := findBreakdown(t, st, "family")
	if len(byFamily.Cells) != 1 || byFamily.Cells[0].Key != "anthropic" {
		t.Errorf("family breakdown = %+v", byFamily.Cells)
	}
	byPair := findBreakdown(t, st, "pair")
	if len(byPair.Cells) != 1 || byPair.Cells[0].Key != "anthropic/anthropic" {
		t.Errorf("pair breakdown = %+v", byPair.Cells)
	}
}

func findBreakdown(t *testing.T, st *Stats, by string) Breakdown {
	t.Helper()
	for _, b := range st.Breakdowns {
		if b.By == by {
			return b
		}
	}
	t.Fatalf("no %s breakdown", by)
	return Breakdown{}
}

func TestSizeTable(t *testing.T) {
	st := Compute(sampleActivity(), sampleFacts(), at(200))
	rows := map[string]SizeRow{}
	for _, r := range st.Sizes {
		rows[r.Size] = r
	}
	l := rows["L"]
	if l.N != 1 || l.Measured != 1 {
		t.Errorf("L row = %+v", l)
	}
	// Measured 5 lines: the S bucket.
	if bucket := bucketN(l, "S"); bucket != 1 {
		t.Errorf("L -> S bucket = %d, want 1 (%+v)", bucket, l.Buckets)
	}
	if rows[""].N != 0 {
		t.Errorf("unlabelled row = %+v", rows[""])
	}
}

func bucketN(r SizeRow, b string) int {
	for _, x := range r.Buckets {
		if x.Bucket == b {
			return x.N
		}
	}
	return -1
}

func TestBurn(t *testing.T) {
	st := Compute(sampleActivity(), sampleFacts(), at(200))
	if len(st.Milestones) != 1 {
		t.Fatalf("milestones = %+v", st.Milestones)
	}
	m := st.Milestones[0]
	if m.Title != "M1" || m.Total != 2 || m.Closed != 1 {
		t.Errorf("milestone = %+v", m)
	}
	if m.Weighted {
		t.Errorf("milestone weighted although #8 has no size")
	}
	if len(m.Points) != 4 {
		t.Fatalf("points = %+v", m.Points)
	}
	if p := m.Points[0]; p.Total != 1 || p.Open != 1 || p.Closed != 0 {
		t.Errorf("point 0 = %+v", p)
	}
	if p := m.Points[2]; p.Total != 2 || p.Closed != 1 || p.Open != 1 {
		t.Errorf("point 2 = %+v", p)
	}
	if p := m.Points[3]; p.Total != 2 || p.Open != 1 {
		t.Errorf("final point = %+v", p)
	}
}

func TestSizeLabel(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"size/S", "S"}, {"size/XL", "XL"}, {"size/xl", ""}, {"mode/ask", ""}, {"size/", ""}, {"", ""},
	} {
		if got := SizeLabel(tc.in); got != tc.want {
			t.Errorf("SizeLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
