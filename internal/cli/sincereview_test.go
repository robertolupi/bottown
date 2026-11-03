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

// sinceRepo is a PR's git setup: a bare remote, the author's clone work and
// a separate clone check that has to fetch the commits, as a verifier's
// worktree does. main starts with one commit.
type sinceRepo struct {
	t                 *testing.T
	bare, work, check string
}

func newSinceRepo(t *testing.T) *sinceRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	r := &sinceRepo{t: t, bare: filepath.Join(dir, "remote.git"), work: filepath.Join(dir, "work"), check: filepath.Join(dir, "check")}
	r.run(dir, "init", "-q", "--bare", "-b", "main", r.bare)
	// Forgejo serves commits by ID even once no branch has them.
	r.run(r.bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	r.run(dir, "clone", "-q", r.bare, r.work)
	r.commit("README", "demo\n", "init")
	r.git("push", "-q", "origin", "main")
	r.run(dir, "clone", "-q", r.bare, r.check)
	return r
}

func (r *sinceRepo) run(d string, args ...string) string {
	r.t.Helper()
	c := exec.Command("git", append([]string{"-C", d, "-c", "user.name=t", "-c", "user.email=t@x", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// git runs git in the author's clone.
func (r *sinceRepo) git(args ...string) string {
	r.t.Helper()
	return r.run(r.work, args...)
}

// commit writes file in the author's clone and commits it.
func (r *sinceRepo) commit(file, content, msg string) string {
	r.t.Helper()
	os.WriteFile(filepath.Join(r.work, file), []byte(content), 0o644)
	r.git("add", ".")
	r.git("commit", "-qm", msg)
	return r.git("rev-parse", "HEAD")
}

// since is sinceReview from the verifier's clone.
func (r *sinceRepo) since(mergeBase, old, new string) string {
	r.t.Helper()
	out, err := sinceReview(r.check, "origin", "main", mergeBase, old, new)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// TestSinceReview moves a PR branch as a rebase, a follow-up commit and a
// merge would.
func TestSinceReview(t *testing.T) {
	r := newSinceRepo(t)
	r.git("switch", "-q", "-c", "feature")
	reviewed := r.commit("f.txt", "one\n", "add f")
	r.git("push", "-q", "origin", "feature")
	r.git("switch", "-q", "main")
	m1 := r.commit("other.txt", "other\n", "main moves")
	r.git("push", "-q", "origin", "main")

	if out := r.since("", reviewed, reviewed); !strings.HasPrefix(out, "no change: the head is still the reviewed commit "+short(reviewed)) {
		t.Errorf("same head: %s", out)
	}

	// A rebase onto the new main: the same patch.
	r.git("switch", "-q", "feature")
	r.git("rebase", "-q", "main")
	rebased := r.git("rev-parse", "HEAD")
	r.git("push", "-q", "-f", "origin", "feature")
	if out := r.since("", reviewed, rebased); out != "same patch, rebased onto "+short(m1)+" (was on "+short(r.git("rev-parse", "main~1"))+")\n" {
		t.Errorf("rebase: %s", out)
	}

	// A follow-up that changes the patch: a range-diff.
	fixed := r.commit("f.txt", "one\ntwo\n", "fix f")
	r.git("push", "-q", "origin", "feature")
	out := r.since("", reviewed, fixed)
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
	r.git("push", "-q", "origin", "feature:main")
	if _, err := sinceReview(r.check, "origin", "main", "", reviewed, fixed); err == nil || !strings.Contains(err.Error(), "already on it") {
		t.Errorf("merged without a merge base: %v", err)
	}
	if out := r.since(m1, reviewed, fixed); !strings.Contains(out, "fix f") {
		t.Errorf("merged with the merge base:\n%s", out)
	}
}

// TestSinceReviewMergedFollowUp merges a PR whose reviewed commit is still
// in its history, under a follow-up: both are on main once it merges.
func TestSinceReviewMergedFollowUp(t *testing.T) {
	r := newSinceRepo(t)
	base := r.git("rev-parse", "HEAD")
	r.git("switch", "-q", "-c", "feature")
	reviewed := r.commit("f.txt", "one\n", "add f")
	fixed := r.commit("f.txt", "one\ntwo\n", "fix f")
	r.git("push", "-q", "origin", "feature:main")

	if _, err := sinceReview(r.check, "origin", "main", "", reviewed, fixed); err == nil || !strings.Contains(err.Error(), "already on it") {
		t.Errorf("without a merge base: %v", err)
	}
	out := r.since(base, reviewed, fixed)
	for _, want := range []string{"the patch changed: git range-diff " + short(base) + ".." + short(reviewed) + " " + short(base) + ".." + short(fixed), "fix f", "+two"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "base also moved") {
		t.Errorf("the base did not move:\n%s", out)
	}
}

// TestSinceReviewWhitespace changes only indentation, which changes what
// Python code does: it is not the same patch, as a follow-up commit or
// amended into the reviewed one.
func TestSinceReviewWhitespace(t *testing.T) {
	r := newSinceRepo(t)
	r.commit("a.py", "def f(x):\n    pass\n", "add f")
	r.git("push", "-q", "origin", "main")
	r.git("switch", "-q", "-c", "feature")
	reviewed := r.commit("a.py", "def f(x):\n    pass\n    if x:\n        print(1)\n        print(2)\n", "print")
	dedent := "def f(x):\n    pass\n    if x:\n        print(1)\n    print(2)\n"
	followUp := r.commit("a.py", dedent, "dedent")
	r.git("push", "-q", "origin", "feature")
	out := r.since("", reviewed, followUp)
	for _, want := range []string{"the patch changed: ", "dedent", "-        print(2)", "+    print(2)"} {
		if !strings.Contains(out, want) {
			t.Errorf("follow-up lacks %q:\n%s", want, out)
		}
	}

	// The same, amended into the reviewed commit on the same base.
	r.git("reset", "-q", "--hard", reviewed)
	os.WriteFile(filepath.Join(r.work, "a.py"), []byte(dedent), 0o644)
	r.git("commit", "-qa", "--amend", "--no-edit")
	amended := r.git("rev-parse", "HEAD")
	r.git("push", "-q", "-f", "origin", "feature")
	out = r.since("", reviewed, amended)
	if !strings.Contains(out, "the patch changed: ") || !strings.Contains(out, "++    print(2)") {
		t.Errorf("amended:\n%s", out)
	}
}

// TestSinceReviewMergeEdit changes the patch inside a merge of main, which
// range-diff leaves out: every commit matches, so the full diff is shown.
func TestSinceReviewMergeEdit(t *testing.T) {
	r := newSinceRepo(t)
	r.git("switch", "-q", "-c", "feature")
	reviewed := r.commit("f.txt", "one\n", "add f")
	r.git("switch", "-q", "main")
	r.commit("other.txt", "other\n", "main moves")
	r.git("push", "-q", "origin", "main")
	r.git("switch", "-q", "feature")
	r.git("merge", "-q", "--no-commit", "main")
	os.WriteFile(filepath.Join(r.work, "f.txt"), []byte("one\nhidden\n"), 0o644)
	r.git("add", ".")
	r.git("commit", "-qm", "merge main")
	merged := r.git("rev-parse", "HEAD")
	r.git("push", "-q", "origin", "feature")

	out := r.since("", reviewed, merged)
	for _, want := range []string{"the patch changed: ", "1:  " + reviewed[:7] + " = 1:", "The commits match, but the whole change differs", "+hidden"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
}
