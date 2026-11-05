package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuideRendersTheFam(t *testing.T) {
	g, err := Guide(GuideData{
		Repo: "bottown/demo", Main: "/f/demo/main", Actor: "claude", Remote: "bottown-dev", Bottown: "/bin/bottown",
		Actors: []GuideActor{{"claude", "/f/demo/claude"}, {"codex", "/f/demo/codex"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<!-- " + GuideMarker + " -->\n",
		"`bottown/demo` is developed in a bottown fam",
		"This worktree is actor **claude**'s.",
		"/f/demo/main  the owner's checkout\n/f/demo/claude  actor worktree for claude\n/f/demo/codex  actor worktree for codex\n```",
		"The `bottown-dev` remote is the forge",
		"/bin/bottown run start implement 12",
		"`bottown` here is `/bin/bottown`.",
		"Anyone can file issues",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("guide lacks %q:\n%s", want, g)
		}
	}
	// The owner's checkout: no actor sentence.
	g, err = Guide(GuideData{Main: "/f/demo/main", Remote: "bottown", Bottown: "bottown"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(g, "This worktree is actor") || !strings.Contains(g, "This repository is developed") {
		t.Errorf("owner's guide:\n%s", g)
	}
}

// guideRepo is a checkout with one commit and a linked worktree, like a fam's
// main and an actor's worktree.
func guideRepo(t *testing.T) (main, wt string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", tmp)
	main, wt = filepath.Join(tmp, "main"), filepath.Join(tmp, "claude")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-q", "--detach", wt},
	} {
		if _, err := Git(main, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	return main, wt
}

func TestWriteGuide(t *testing.T) {
	main, wt := guideRepo(t)
	const guide = "<!-- " + GuideMarker + " -->\nthe guide\n"

	path, skipped, err := WriteGuide(wt, "claude", guide)
	if err != nil || skipped != "" || path != "CLAUDE.local.md" {
		t.Fatalf("WriteGuide: %q %q %v", path, skipped, err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "CLAUDE.local.md")); string(b) != guide {
		t.Errorf("guide file: %q", b)
	}
	// Excluded in the shared info/exclude: no worktree shows it.
	for _, dir := range []string{wt, main} {
		os.WriteFile(filepath.Join(dir, "CLAUDE.local.md"), []byte(guide), 0o644)
		if st, err := Git(dir, nil, "status", "--porcelain"); err != nil || st != "" {
			t.Errorf("%s status: %q %v", dir, st, err)
		}
	}
	os.Remove(filepath.Join(main, "CLAUDE.local.md"))
	if s := GuideStatus(wt, "claude", guide); s != GuideCurrent {
		t.Errorf("status %s", s)
	}
	if s := GuideStatus(wt, "claude", guide+"more\n"); s != GuideOutdated {
		t.Errorf("status after a guide change: %s", s)
	}
	if s := GuideStatus(wt, "kimi", guide); s != GuideMissing {
		t.Errorf("kimi status: %s", s)
	}
	if s := GuideStatus(wt, "codex", guide); s != GuideNone {
		t.Errorf("codex status: %s", s)
	}

	// The actor's harness changed: the old slot's file goes.
	if path, _, err := WriteGuide(wt, "kimi", guide); err != nil || path != ".kimi-code/AGENTS.md" {
		t.Fatalf("kimi: %q %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "CLAUDE.local.md")); err == nil {
		t.Error("claude's guide file left behind")
	}
	if st, _ := Git(wt, nil, "status", "--porcelain"); st != "" {
		t.Errorf("kimi guide not excluded: %q", st)
	}

	// A file bottown did not write is left alone.
	mine := filepath.Join(wt, "CRUSH.local.md")
	os.WriteFile(mine, []byte("my notes\n"), 0o644)
	if path, skipped, err := WriteGuide(wt, "crush", guide); err != nil || path != "" || !strings.Contains(skipped, "not written by bottown") {
		t.Errorf("foreign file: %q %q %v", path, skipped, err)
	}
	if b, _ := os.ReadFile(mine); string(b) != "my notes\n" {
		t.Errorf("foreign file changed: %q", b)
	}
	if s := GuideStatus(wt, "crush", guide); s != GuideForeign {
		t.Errorf("foreign status: %s", s)
	}

	// No slot: nothing written, and the reason names the skill.
	if path, skipped, err := WriteGuide(wt, "codex", guide); err != nil || path != "" || !strings.Contains(skipped, "skill") {
		t.Errorf("codex: %q %q %v", path, skipped, err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".kimi-code", "AGENTS.md")); err == nil {
		t.Error("kimi's guide file left behind for codex")
	}
}

// A guide slot the repository tracks is the project's file: bottown neither
// writes nor removes it.
func TestWriteGuideTrackedSlot(t *testing.T) {
	main, wt := guideRepo(t)
	const guide = "<!-- " + GuideMarker + " -->\nthe guide\n"
	// Tracked, and even carrying the marker (say, committed by mistake).
	if err := os.WriteFile(filepath.Join(main, "CLAUDE.local.md"), []byte(guide), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-f", "CLAUDE.local.md"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "local"},
	} {
		if _, err := Git(main, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Git(wt, nil, "checkout", "-q", "--detach", "main"); err != nil {
		t.Fatal(err)
	}
	if s := GuideStatus(wt, "claude", guide); s != GuideForeign {
		t.Errorf("tracked slot status: %s", s)
	}
	if path, skipped, err := WriteGuide(wt, "claude", guide); err != nil || path != "" || skipped == "" {
		t.Errorf("tracked slot: %q %q %v", path, skipped, err)
	}
	if _, _, err := WriteGuide(wt, "kimi", guide); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "CLAUDE.local.md")); err != nil {
		t.Errorf("tracked file removed: %v", err)
	}
}
