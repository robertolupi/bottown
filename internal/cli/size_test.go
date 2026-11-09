package cli

import (
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

func TestParseEstimate(t *testing.T) {
	for name, text := range map[string]string{
		"toml": "deciding_factor = \"one boundary\"\nboundaries = [\"CLI and server\"]\nconfidence = \"high\"\nscope = \"as filed\"\n",
		"json": ` {"deciding_factor": "one boundary", "boundaries": ["CLI and server"], "confidence": "high", "scope": "as filed"}`,
	} {
		got, err := parseEstimate(text)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.DecidingFactor != "one boundary" || strings.Join(got.Boundaries, ",") != "CLI and server" || got.Confidence != "high" || got.Scope != "as filed" {
			t.Errorf("%s: %+v", name, got)
		}
	}
	for name, text := range map[string]string{
		"misspelt toml key": "behaviors = [\"x\"]\n",
		"misspelt json key": `{"behaviors": ["x"]}`,
		"not toml":          "deciding_factor: x",
	} {
		if _, err := parseEstimate(text); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// An implement run sizes its issue only when the issue has no size label;
// other runs never do.
func TestGoalPromptSizeIssue(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		labels []string
		want   bool
	}{
		{policy.Implement, []string{"mode/ask"}, true},
		{policy.Implement, []string{"needs/clarification"}, true},
		{policy.Implement, []string{"size/L"}, false},
		{policy.Discuss, nil, false},
	} {
		run := secrets.Run{ID: "r-1", Kind: tc.kind, Actor: "claude", Org: "bottown", Repo: "demo", Issue: 4}
		item := server.IssueOut{Number: 4, Labels: tc.labels}
		p, err := goalPrompt(t.Context(), &agentCtx{}, run, runs.PromptData{Bottown: "/b"}, t.TempDir(), "main", t.TempDir(), &item, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(p, "## Size the issue"); got != tc.want {
			t.Errorf("%s run, labels %v: sizes = %v, want %v", tc.kind, tc.labels, got, tc.want)
		}
	}
}
