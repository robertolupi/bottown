package runs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/milestone"
)

func TestPlanPrompt(t *testing.T) {
	p, err := Prompt("plan", PromptData{
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

func TestRepoSettingsAt(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "empty")
	if s, err := RepoSettingsAt(dir, "main"); err != nil || s.MilestonesDir != "docs/milestones" {
		t.Errorf("without the file: %+v, %v; want the defaults", s, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bottown.toml"), []byte("milestones_dir = \"plans\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".bottown.toml")
	git("commit", "-q", "-m", "settings")
	if s, err := RepoSettingsAt(dir, "main"); err != nil || s.MilestonesDir != "plans" {
		t.Errorf("with the file: %+v, %v; want plans", s, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bottown.toml"), []byte("milestones_dir = \"../x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-q", "-am", "bad")
	if _, err := RepoSettingsAt(dir, "main"); err == nil {
		t.Error("an invalid milestones_dir was accepted")
	}
	// Only the ref counts, not the working tree.
	if s, err := RepoSettingsAt(dir, "main~1"); err != nil || s.MilestonesDir != "plans" {
		t.Errorf("at main~1: %+v, %v; want plans", s, err)
	}
}
