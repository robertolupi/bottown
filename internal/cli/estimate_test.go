package cli

import (
	"bytes"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/agents"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/estimate"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

var estimateActors = []config.Actor{{Name: "claude", Family: "anthropic"}, {Name: "opus", Family: "anthropic"}, {Name: "codex", Family: "openai"}}

// --actors is milestone run's grammar with N defaulting to 1, and no
// family rule.
func TestEstimatePool(t *testing.T) {
	got, err := estimatePool("claude,opus:sonnet=*,codex=3", estimateActors)
	if err != nil {
		t.Fatal(err)
	}
	want := []agents.Entry{
		{Actor: "claude", Family: "anthropic", Limit: 1},
		{Actor: "opus", Model: "sonnet", Family: "anthropic", Limit: 0},
		{Actor: "codex", Family: "openai", Limit: 3},
	}
	if !slices.Equal(got, want) {
		t.Errorf("entries = %+v, want %+v", got, want)
	}
	if _, err := estimatePool("claude,opus", estimateActors); err != nil {
		t.Errorf("single-family pool: %v", err)
	}
	for _, spec := range []string{"", "nobody", "claude=0", "claude,claude=2"} {
		if _, err := estimatePool(spec, estimateActors); err == nil || !strings.Contains(err.Error(), "--actors") {
			t.Errorf("estimatePool(%q) err = %v", spec, err)
		}
	}
}

func TestUnsizedIssues(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }
	epic := "The epic.\n\n" + milestone.Marker{Plan: "m", Epic: true, SHA: "0123456789abcdef"}.String()
	items := []server.IssueItem{
		{Number: 9, State: "open", Created: at(3)},
		{Number: 2, State: "open", Created: at(1), Labels: []string{"size/M"}},
		{Number: 3, State: "open", Created: at(2), Labels: []string{"needs/clarification"}},
		{Number: 4, State: "open", Created: at(0), PullRequest: true},
		{Number: 5, State: "closed", Created: at(0)},
		{Number: 6, State: "open", Created: at(0), Body: epic},
		{Number: 7, State: "open", Created: at(2), Labels: []string{"mode/ask"}},
		{Number: 8, State: "open", Created: at(2)},
	}
	if got := unsizedIssues(items, 10); !slices.Equal(got, []int64{7, 8, 9}) {
		t.Errorf("unsized = %v, want [7 8 9] (oldest first)", got)
	}
	if got := unsizedIssues(items, 2); !slices.Equal(got, []int64{7, 8}) {
		t.Errorf("unsized, max 2 = %v, want [7 8]", got)
	}
}

func TestNamedIssues(t *testing.T) {
	issues := []server.IssueOut{
		{Number: 1}, {Number: 2, Labels: []string{"size/L"}}, {Number: 3, Labels: []string{"needs/clarification"}},
	}
	todo, skipped := namedIssues(issues, false)
	if !slices.Equal(todo, []int64{1, 3}) || len(skipped) != 1 || !strings.Contains(skipped[0], "#2 is already size/L") {
		t.Errorf("todo = %v, skipped = %q", todo, skipped)
	}
	todo, skipped = namedIssues(issues, true)
	if !slices.Equal(todo, []int64{1, 2, 3}) || len(skipped) != 0 {
		t.Errorf("--again: todo = %v, skipped = %q", todo, skipped)
	}
}

// Issues go to the next entry with a free slot, and wait for one when
// every entry is busy.
func TestEstimateSlots(t *testing.T) {
	s := newSlots([]agents.Entry{{Actor: "claude", Limit: 1}, {Actor: "codex", Limit: 2}})
	var got []string
	for range 3 {
		got = append(got, s.take().Actor)
	}
	if !slices.Equal(got, []string{"claude", "codex", "codex"}) {
		t.Fatalf("takes = %v", got)
	}
	var wg sync.WaitGroup
	taken := make(chan string, 1)
	wg.Go(func() { taken <- s.take().Actor })
	select {
	case a := <-taken:
		t.Fatalf("took %s with every slot busy", a)
	case <-time.After(50 * time.Millisecond):
	}
	s.give(agents.Entry{Actor: "claude"})
	wg.Wait()
	if a := <-taken; a != "claude" {
		t.Errorf("took %s, want the freed claude", a)
	}
}

func TestEstimateFromComments(t *testing.T) {
	in := estimate.Input{Size: "L", DecidingFactor: "three contracts", Confidence: "medium", Scope: "as filed"}
	mine := estimate.Record{Version: estimate.Version, Run: "r-1", Actor: "claude", Role: policy.Estimate, Size: "L", Applied: true}
	other := mine
	other.Run, other.Size = "r-0", "S"
	comments := []server.CommentOut{
		{Body: estimate.Comment(in, mine, "rlupi")},
		{Body: estimate.Comment(estimate.Input{Size: "S", DecidingFactor: "tiny", Confidence: "high", Scope: "x"}, other, "rlupi")},
		{Body: "an ordinary comment"},
	}
	r := estimateResult{Issue: 4, Run: "r-1"}
	estimateFromComments(&r, comments, "size/L")
	if r.Outcome != secrets.OutcomeSized || r.Size != "L" || r.Factor != "three contracts" {
		t.Errorf("result = %+v", r)
	}

	second := mine
	second.Applied, second.Existing = false, "size/M"
	r = estimateResult{Issue: 4, Run: "r-1"}
	estimateFromComments(&r, []server.CommentOut{{Body: estimate.Comment(in, second, "rlupi")}}, "size/M")
	if r.Outcome != secrets.OutcomeAlreadySized || r.Label != "size/M" {
		t.Errorf("second opinion = %+v", r)
	}

	clar := mine
	clar.Size = estimate.NeedsClarification
	r = estimateResult{Issue: 4, Run: "r-1"}
	estimateFromComments(&r, []server.CommentOut{{Body: estimate.Comment(estimate.Input{Size: estimate.NeedsClarification, Questions: []string{"what?"}}, clar, "rlupi")}}, "")
	if r.Outcome != secrets.OutcomeNeedsClarification {
		t.Errorf("clarification = %+v", r)
	}

	r = estimateResult{Issue: 4, Run: "r-9"}
	estimateFromComments(&r, comments, "")
	if r.Outcome != "" {
		t.Errorf("another run's estimate was taken: %+v", r)
	}
}

func TestPrintEstimates(t *testing.T) {
	var b bytes.Buffer
	printEstimates(&b, []estimateResult{
		{Issue: 1, Actor: "claude", Run: "r-1", Outcome: secrets.OutcomeSized, Size: "M", Factor: "one boundary"},
		{Issue: 2, Actor: "codex", Run: "r-2", Outcome: secrets.OutcomeAlreadySized, Size: "S", Factor: "tiny", Label: "size/L"},
		{Issue: 3, Actor: "claude", Run: "r-3", Outcome: secrets.OutcomeNeedsClarification, Size: estimate.NeedsClarification},
		{Issue: 4, Actor: "codex"},
	})
	out := b.String()
	for _, want := range []string{"#1", "one boundary", "kept size/L; tiny", "questions posted on the issue", "no estimate recorded", "failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

// An estimate run that exits cleanly without an estimate has failed; other
// runs are not checked.
func TestEstimateFailure(t *testing.T) {
	run := secrets.Run{ID: "r-1", Kind: policy.Estimate}
	for outcome, fails := range map[string]bool{
		"": true, secrets.OutcomeCommented: true,
		secrets.OutcomeSized: false, secrets.OutcomeAlreadySized: false, secrets.OutcomeNeedsClarification: false,
	} {
		f := estimateFailure(run, secrets.Run{Outcome: outcome})
		if (f != nil) != fails {
			t.Errorf("outcome %q: failure = %+v, want failed %v", outcome, f, fails)
		}
		if f != nil && f.Cause != secrets.FailureNoOutput {
			t.Errorf("cause = %q", f.Cause)
		}
	}
	if f := estimateFailure(secrets.Run{Kind: policy.Discuss}, secrets.Run{}); f != nil {
		t.Errorf("a discuss run was checked: %+v", f)
	}
}

func TestEstimateHandoverText(t *testing.T) {
	rec := runs.Record{ID: "r-1", Kind: policy.Estimate, Actor: "codex", Family: "openai", SHA: "abc123"}
	body := handoverText(rec, runs.StatusFailed, 1, "")
	if !strings.Contains(body, "Estimated against `abc123`, the default branch's head.") {
		t.Errorf("handover:\n%s", body)
	}
}
