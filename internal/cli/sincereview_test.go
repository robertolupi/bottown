package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/server"
)

func TestSinceReviewOf(t *testing.T) {
	rs := []server.ReviewOut{
		{ID: 1, Reviewer: "agy", State: "REQUEST_CHANGES", CommitID: "a1", Official: true},
		{ID: 2, Reviewer: "kimi", State: "COMMENT", CommitID: "b2"},
		{ID: 3, Reviewer: "agy", State: "APPROVED", CommitID: "c3", Official: true, Stale: true},
		{ID: 4, Reviewer: "codex", State: "COMMENT", CommitID: "d4"},
		{ID: 5, Reviewer: "codex", State: "REQUEST_REVIEW"},
		{ID: 6, Reviewer: "kimi", State: "PENDING", CommitID: "f6"},
	}
	for _, c := range []struct {
		name     string
		reviews  []server.ReviewOut
		reviewer string
		want     int64 // 0: none
	}{
		{"latest official over a later comment", rs, "", 3},
		{"by reviewer", rs, "kimi", 2},
		{"by reviewer, not official", rs, "codex", 4},
		{"by reviewer, none", rs, "claude", 0},
		{"no official: latest at all", rs[1:2], "", 2},
		{"pending and requested do not count", rs[4:], "", 0},
		{"no reviews", nil, "", 0},
		// Listed out of order, the highest ID is still the latest.
		{"order", []server.ReviewOut{rs[2], rs[0]}, "", 3},
	} {
		got, ok := sinceReviewOf(c.reviews, c.reviewer)
		if ok != (c.want != 0) || got.ID != c.want {
			t.Errorf("%s: got review %d (%v), want %d", c.name, got.ID, ok, c.want)
		}
	}
}

// TestSinceReview pushes a PR branch to a bare remote, moves it as a
// rebase, a follow-up commit and a merge would, and compares from a separate
// clone that has to fetch the commits, as a verifier's worktree does.
func TestSinceReview(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(d string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", d, "-c", "user.name=t", "-c", "user.email=t@x", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(d, file, content, msg string) string {
		t.Helper()
		os.WriteFile(filepath.Join(d, file), []byte(content), 0o644)
		run(d, "add", ".")
		run(d, "commit", "-qm", msg)
		return run(d, "rev-parse", "HEAD")
	}
	bare, work, check := filepath.Join(dir, "remote.git"), filepath.Join(dir, "work"), filepath.Join(dir, "check")
	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	// Forgejo serves commits by ID even once no branch has them.
	run(bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	run(dir, "clone", "-q", bare, work)
	commit(work, "README", "demo\n", "init")
	run(work, "push", "-q", "origin", "main")
	run(dir, "clone", "-q", bare, check)

	run(work, "switch", "-q", "-c", "feature")
	reviewed := commit(work, "f.txt", "one\n", "add f")
	run(work, "push", "-q", "origin", "feature")
	run(work, "switch", "-q", "main")
	m1 := commit(work, "other.txt", "other\n", "main moves")
	run(work, "push", "-q", "origin", "main")

	since := func(mergeBase, old, new string) string {
		t.Helper()
		out, err := sinceReview(check, "origin", "main", mergeBase, old, new)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	if out := since("", reviewed, reviewed); !strings.HasPrefix(out, "no change: the head is still the reviewed commit "+short(reviewed)) {
		t.Errorf("same head: %s", out)
	}

	// A rebase onto the new main: the same patch.
	run(work, "switch", "-q", "feature")
	run(work, "rebase", "-q", "main")
	rebased := run(work, "rev-parse", "HEAD")
	run(work, "push", "-q", "-f", "origin", "feature")
	if out := since("", reviewed, rebased); out != "same patch, rebased onto "+short(m1)+" (was on "+short(run(work, "rev-parse", "main~1"))+")\n" {
		t.Errorf("rebase: %s", out)
	}

	// A follow-up that changes the patch: a range-diff.
	fixed := commit(work, "f.txt", "one\ntwo\n", "fix f")
	run(work, "push", "-q", "origin", "feature")
	out := since("", reviewed, fixed)
	for _, want := range []string{
		"the patch changed: git range-diff ",
		"(the base also moved, from ",
		"add f",            // the reviewed commit, matched as unchanged
		"-:  ------- > 2:", // the new commit
		"fix f",
		"--- new since the review: ",
		"+two",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("follow-up lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "same patch") {
		t.Errorf("follow-up reported as the same patch:\n%s", out)
	}

	// Merged: the head is on main, and only the forge's merge base says
	// where it branched.
	run(work, "push", "-q", "origin", "feature:main")
	if _, err := sinceReview(check, "origin", "main", "", reviewed, fixed); err == nil || !strings.Contains(err.Error(), "already on it") {
		t.Errorf("merged without a merge base: %v", err)
	}
	if out := since(m1, reviewed, fixed); !strings.Contains(out, "fix f") {
		t.Errorf("merged with the merge base:\n%s", out)
	}
}
