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
	if strings.Contains(p, "partial") {
		t.Errorf("whole evidence is called partial:\n%s", p)
	}

	d.EvidenceGaps = []string{"The evidence has 2 verify run(s) but no implement or plan run.", "Pull request !9's head is by kimi."}
	if p, err = Prompt("retro", d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "`prompts/<run>.md`: the goal prompt the run was given.\n\n**The evidence looks partial:**\n\n"+
		"- The evidence has 2 verify run(s) but no implement or plan run.\n- Pull request !9's head is by kimi.\n\nSay so in the retro.") {
		t.Errorf("retro prompt with partial evidence:\n%s", p)
	}
	d.EvidenceGaps = nil

	d.Evidence, d.EvidenceError = "", "list runs: boom"
	p, err = Prompt("retro", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "could not gather its records of the flow: list runs: boom.") || strings.Contains(p, "`runs.json`") {
		t.Errorf("retro prompt without evidence:\n%s", p)
	}
}

// A retro on a pull request says which issue's flow it covers, or that it
// covers none.
func TestRetroPromptOnPR(t *testing.T) {
	d := PromptData{
		RunID: "r-1", Actor: "codex", Family: "openai", Repo: "o/r", Issue: 9,
		Title: "Fix it", Bottown: "/b", Evidence: "/tmp/x/retro",
		RetroPR: 9, RetroIssue: 7, RetroBranch: "issue/7",
	}
	p, err := Prompt("retro", d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Goal\n\nWrite a blameless retrospective of PR !9 of issue #7\nin `o/r`: **Fix it**",
		"from `issue/7`, so the\nretro covers the flow of #7",
		"stops. Read the discussion of #7 as well as the PR's. Build it from",
		"/b comment --body-file -`, on #9",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("retro prompt on a PR lacks %q:\n%s", want, p)
		}
	}

	d.RetroIssue, d.RetroBranch = 0, "hotfix"
	if p, err = Prompt("retro", d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "## Goal\n\nWrite a blameless retrospective of PR !9 in `o/r`:\n**Fix it**. It is from `hotfix`, not an `issue/N` branch") {
		t.Errorf("retro prompt on a lone PR:\n%s", p)
	}

	d.RetroPR = 0
	if p, err = Prompt("retro", d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "## Goal\n\nWrite a blameless retrospective of the flow of #9 in `o/r`") {
		t.Errorf("retro prompt on an issue:\n%s", p)
	}
}
