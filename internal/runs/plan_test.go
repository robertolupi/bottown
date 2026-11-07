package runs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
