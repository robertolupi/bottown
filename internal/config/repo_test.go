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

	for _, bad := range []string{
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
