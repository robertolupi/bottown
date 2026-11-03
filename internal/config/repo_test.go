package config

import (
	"slices"
	"strings"
	"testing"
)

func TestParseRepoSettings(t *testing.T) {
	s, err := ParseRepoSettings([]byte("# comment\nafter_pull = [\"make install\", \"echo done\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.AfterPull, []string{"make install", "echo done"}) {
		t.Fatalf("after_pull = %q", s.AfterPull)
	}

	s, err = ParseRepoSettings(nil)
	if err != nil || len(s.AfterPull) != 0 || s.MilestonesDir != "docs/milestones" {
		t.Fatalf("empty file: %+v, %v", s, err)
	}
	if d := DefaultRepoSettings().MilestonesDir; d != "docs/milestones" {
		t.Fatalf("default milestones_dir = %q", d)
	}

	s, err = ParseRepoSettings([]byte(`milestones_dir = "plans/milestones"` + "\n"))
	if err != nil || s.MilestonesDir != "plans/milestones" {
		t.Fatalf("custom milestones_dir: %+v, %v", s, err)
	}

	s, err = ParseRepoSettings(nil)
	if err != nil || !slices.Equal(s.Check, []string{"make check"}) || !slices.Equal(s.E2E, []string{"make e2e"}) {
		t.Fatalf("default checks: %+v, %v", s, err)
	}
	s, err = ParseRepoSettings([]byte("check = [\"go vet ./...\", \"go test ./...\"]\ne2e = []\n"))
	if err != nil || !slices.Equal(s.Check, []string{"go vet ./...", "go test ./..."}) || len(s.E2E) != 0 {
		t.Fatalf("custom checks: %+v, %v", s, err)
	}
	if !slices.Equal(DefaultRepoSettings().Check, []string{"make check"}) {
		t.Fatalf("parsing changed the defaults: %q", DefaultCheck)
	}

	for _, bad := range []string{
		"check = [\"\"]\n",
		"e2e = \"make e2e\"\n",
		"after-pull = [\"make install\"]\n", // unknown key
		"after_pull = \"make install\"\n",   // not a list
		"after_pull = [\"  \"]\n",           // empty command
		"after_pull = [\n",                  // syntax
		`milestones-dir = "plans"`,          // unknown key
		`milestones_dir = ""`,
		`milestones_dir = "."`,
		`milestones_dir = "/docs/milestones"`,
		`milestones_dir = "../milestones"`,
		`milestones_dir = "docs/../.."`,
		`milestones_dir = "docs/milestones/"`,
		`milestones_dir = "docs//milestones"`,
		`milestones_dir = 'docs\milestones'`,
		`milestones_dir = 3`,
	} {
		if _, err := ParseRepoSettings([]byte(bad)); err == nil || !strings.Contains(err.Error(), RepoSettingsFile) {
			t.Errorf("%q: err = %v, want an error naming %s", bad, err, RepoSettingsFile)
		}
	}
}
