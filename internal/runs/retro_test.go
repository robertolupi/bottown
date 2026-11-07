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

// A windowed retro (#383) looks across a window's flows and writes its
// wiki page, not a comment on an issue.
func TestRetroPromptWindow(t *testing.T) {
	d := PromptData{
		RunID: "r-1", Actor: "codex", Family: "openai", Repo: "o/r",
		Title: "what landed on main after aaa, up to bbb", SHA: "bbbbbbb", Base: "main",
		Bottown: "/b", Remote: "bottown", Evidence: "/tmp/x/retro", EvidenceRuns: 9,
		Window: true, WindowSince: "2w", WindowBase: "aaaaaaa", WindowPage: "20261107-bbbbbbb-retrospective",
		WindowMerged: 4, Flows: 3,
	}
	p, err := Prompt("retro", d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a window of `o/r`\n(`--since 2w`): every commit on `main` after `aaaaaaa`,\nfirst-parent, up to `bbbbbbb`. 4 merged pull request(s) landed\nin it, which make 3 flow(s)",
		"window's flows, 9 run(s), in\n`/tmp/x/retro`", "/b issue show N", "/b wiki list",
		"**Repeated root causes**", "If nothing repeated, say so", "**What is no longer true**", "never binding",
		"`/b wiki put 20261107-bbbbbbb-retrospective --file F`", "Do not\n  comment on the window's issues",
		"written to the wiki page `20261107-bbbbbbb-retrospective`",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("windowed retro prompt lacks %q:\n%s", want, p)
		}
	}
	for _, not := range []string{"#0", "comment --body-file", "flow of #"} {
		if strings.Contains(p, not) {
			t.Errorf("windowed retro prompt has %q:\n%s", not, p)
		}
	}
}
