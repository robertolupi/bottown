package runs

import (
	"strings"
	"testing"
)

func TestRetroPrompt(t *testing.T) {
	d := PromptData{
		RunID: "r-1", Actor: "codex", Family: "openai", Repo: "o/r", Issue: 7,
		Title: "Done thing", Body: "Do the thing.", SHA: "abc", Base: "main",
		Bottown: "/b", Remote: "bottown", Evidence: "/tmp/x/retro", EvidenceRuns: 3,
		Message: "focus on the second round",
	}
	p, err := Prompt("retro", d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**retrospective**", "flow of #7 in `o/r`", "**Done thing**", "Do the thing.",
		"## Owner's note\n\nfocus on the second round",
		"flow's 3 run(s) in\n`/tmp/x/retro`", "`runs.json`", "`transcripts/<run>.jsonl`", "`prompts/<run>.md`",
		"/b issue show 7", "/b pr show N", "blameless", "**Root causes**", "**Follow-ups**",
		"detached at `abc`", "cannot push, open pull requests or\n  review", "/b comment --body-file -",
		"/b issue create", "discuss the findings\n  with them first",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("retro prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "run end") {
		t.Errorf("a launched retro run's prompt asks it to end itself:\n%s", p)
	}

	d.Evidence, d.EvidenceError = "", "list runs: boom"
	p, err = Prompt("retro", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "could not gather its records of the flow: list runs: boom.") || strings.Contains(p, "`runs.json`") {
		t.Errorf("retro prompt without evidence:\n%s", p)
	}
}
