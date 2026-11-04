package gitmerge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMergeTree(t *testing.T) {
	for _, tc := range []struct {
		name      string
		conflict  bool
		conflicts []string
	}{
		{name: "clean"},
		{name: "conflicting", conflict: true, conflicts: []string{"a.txt", "b.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitTest(t, dir, "init", "-q", "-b", "main")
			writeTestFile(t, dir, "base.txt", "base\n")
			if tc.conflict {
				writeTestFile(t, dir, "a.txt", "base a\n")
				writeTestFile(t, dir, "b.txt", "base b\n")
			}
			gitTest(t, dir, "add", ".")
			gitTest(t, dir, "commit", "-qm", "base")
			gitTest(t, dir, "checkout", "-qb", "issue/1")
			writeTestFile(t, dir, "pr.txt", "PR\n")
			if tc.conflict {
				writeTestFile(t, dir, "a.txt", "PR a\n")
				writeTestFile(t, dir, "b.txt", "PR b\n")
			}
			gitTest(t, dir, "add", ".")
			gitTest(t, dir, "commit", "-qm", "PR")
			head := gitTest(t, dir, "rev-parse", "HEAD")
			gitTest(t, dir, "checkout", "-q", "main")
			writeTestFile(t, dir, "main.txt", "main\n")
			if tc.conflict {
				writeTestFile(t, dir, "a.txt", "main a\n")
				writeTestFile(t, dir, "b.txt", "main b\n")
			}
			gitTest(t, dir, "add", ".")
			gitTest(t, dir, "commit", "-qm", "main moves")
			base := gitTest(t, dir, "rev-parse", "HEAD")
			gitDir := filepath.Join(dir, ".git")

			behind, err := IsAncestor(context.Background(), gitDir, base, head)
			if err != nil || behind {
				t.Fatalf("IsAncestor = %v, %v; want false, nil", behind, err)
			}
			before := gitTest(t, dir, "status", "--porcelain")
			result, err := MergeTree(context.Background(), gitDir, head, base)
			if err != nil {
				t.Fatal(err)
			}
			if result.Tree == "" || !reflect.DeepEqual(result.Conflicts, tc.conflicts) {
				t.Fatalf("MergeTree = %+v, want conflicts %v", result, tc.conflicts)
			}
			if after := gitTest(t, dir, "status", "--porcelain"); after != before {
				t.Fatalf("worktree changed: before %q, after %q", before, after)
			}
			if got := gitTest(t, dir, "rev-parse", "HEAD"); got != base {
				t.Fatalf("HEAD moved from %s to %s", base, got)
			}
		})
	}
}

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTestFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
