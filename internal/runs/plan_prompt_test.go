package runs_test

import (
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/runs"
)

// An external test: milestone imports agents, which imports runs.
func TestPlanPrompt(t *testing.T) {
	p, err := runs.Prompt("plan", runs.PromptData{
		RunID: "r-1", Actor: "claude", Family: "anthropic", Repo: "o/r", Issue: 7,
		Title: "Kickoff", Body: "Plan the thing.", Branch: "issue/7", Base: "main",
		Bottown: "/b", Remote: "bottown", SelfEnd: true, MilestonesDir: "plans",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**planner**", "kickoff issue #7 in `o/r`", "**Kickoff**", "Plan the thing.",
		"`plans/<slug>.md`", "creates\nno issues", "/b issue show", "/b milestone check <slug>",
		"repository's checks", "AGENTS.md (or README)", "`/b check`", "Commit only the plan file on `issue/7`",
		"git push bottown HEAD", "pull request into `main`", "/b pr create", "kickoff = 7",
		"/b run end --summary-file -",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plan prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "issue create") {
		t.Errorf("plan prompt suggests filing issues:\n%s", p)
	}

	// The prompt's example must be a valid plan.
	_, rest, ok := strings.Cut(p, "````markdown\n")
	example, _, ok2 := strings.Cut(rest, "````")
	if !ok || !ok2 {
		t.Fatalf("plan prompt has no example plan:\n%s", p)
	}
	var lines []string
	for _, l := range strings.Split(example, "\n") {
		lines = append(lines, strings.TrimPrefix(l, "   "))
	}
	if _, err := milestone.Check("example.md", []byte(strings.Join(lines, "\n"))); err != nil {
		t.Errorf("the plan prompt's example does not check: %v\n%s", err, example)
	}
}
