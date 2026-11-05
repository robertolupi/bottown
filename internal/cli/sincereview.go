package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/robertolupi/bottown/internal/server"
)

// pr show --since-review: what changed on a PR since its latest review, so a
// verifier of a moved head reads only that. The comparison runs in the
// local checkout, which fetches the reviewed commit, the head and the base
// branch from its bottown remote.

// prSinceJSON is pr show --since-review --json: the PR, the review compared
// with (none without an earlier review, when Diff has the full diff) and
// what changed since.
type prSinceJSON struct {
	server.PROut
	SinceReview *server.ReviewOut `json:"since_review,omitempty"`
	SinceText   string            `json:"since_review_text,omitempty"`
}

// prReviewed says whether PR n has a review --since-review can compare with,
// for the verify prompt. An error reads as no: the hint is only a hint.
func (a *agentCtx) prReviewed(ctx context.Context, n int64) bool {
	var p server.PROut
	if err := a.call(ctx, "GET", fmt.Sprintf("/v1/forge/pr?number=%d", n), nil, &p); err != nil {
		return false
	}
	_, ok := sinceReviewOf(p.Reviews, "")
	return ok
}

// sinceReviewOf picks the review to compare against: the latest by reviewer
// if one is given, else the latest official one (a verdict from someone who
// may approve), else the latest at all. Reviews without a commit (pending
// or requested) do not count. ok is false when none qualifies.
func sinceReviewOf(reviews []server.ReviewOut, reviewer string) (rev server.ReviewOut, ok bool) {
	var official, latest server.ReviewOut
	for _, r := range reviews {
		if r.CommitID == "" || r.State == "PENDING" || r.State == "REQUEST_REVIEW" {
			continue
		}
		if reviewer != "" && r.Reviewer != reviewer {
			continue
		}
		if r.ID >= latest.ID {
			latest = r
		}
		if r.Official && r.ID >= official.ID {
			official = r
		}
	}
	switch {
	case reviewer == "" && official.CommitID != "":
		return official, true
	case latest.CommitID != "":
		return latest, true
	}
	return server.ReviewOut{}, false
}

// sinceReview describes the change from the reviewed commit old to the head
// new of a PR onto baseBranch, in the checkout dir with remote. It is one of:
// no change, the same patch rebased, or a range-diff of the two series.
// mergeBase is the forge's merge base of new, if it knows one: once the PR
// has merged, new is on the base branch and only the forge remembers where
// it branched.
func sinceReview(dir, remote, baseBranch, mergeBase, old, new string) (string, error) {
	if old == new {
		return fmt.Sprintf("no change: the head is still the reviewed commit %s\n", short(new)), nil
	}
	if _, err := git(dir, "fetch", "-q", remote, baseBranch); err != nil {
		return "", err
	}
	tip, err := git(dir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	for _, sha := range []string{old, new} {
		if _, err := git(dir, "cat-file", "-e", sha+"^{commit}"); err == nil {
			continue
		}
		if _, err := git(dir, "fetch", "-q", remote, sha); err != nil {
			return "", fmt.Errorf("cannot fetch %s from %s: %w", short(sha), remote, err)
		}
	}
	oldBase, err := git(dir, "merge-base", old, tip)
	if err != nil {
		return "", err
	}
	newBase, err := git(dir, "merge-base", new, tip)
	if err != nil {
		return "", err
	}
	// Once merged, the head is on the base branch, and so is the reviewed
	// commit unless a rebase replaced it: each branched where it meets the
	// forge's merge base, a point of the base branch before the merge.
	if mergeBase != "" {
		if newBase == new {
			if _, err := git(dir, "merge-base", "--is-ancestor", mergeBase, new); err == nil {
				newBase = mergeBase
			}
		}
		if oldBase == old {
			if mb, err := git(dir, "merge-base", old, mergeBase); err == nil {
				oldBase = mb
			}
		}
	}
	for _, c := range [][2]string{{oldBase, old}, {newBase, new}} {
		if c[0] == c[1] {
			return "", fmt.Errorf("cannot tell where %s branched from %s: it is already on it", short(c[1]), baseBranch)
		}
	}
	oldID, err := patchID(dir, oldBase, old)
	if err != nil {
		return "", err
	}
	newID, err := patchID(dir, newBase, new)
	if err != nil {
		return "", err
	}
	if oldID == newID {
		if oldBase == newBase {
			return fmt.Sprintf("same patch on the same base %s: only the commits differ (messages or how the change is split)\n", short(newBase)), nil
		}
		return fmt.Sprintf("same patch, rebased onto %s (was on %s)\n", short(newBase), short(oldBase)), nil
	}
	rd, err := git(dir, "range-diff", "--no-color", oldBase+".."+old, newBase+".."+new)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the patch changed: git range-diff %s..%s %s..%s\n", short(oldBase), short(old), short(newBase), short(new))
	if oldBase != newBase {
		fmt.Fprintf(&b, "(the base also moved, from %s to %s)\n", short(oldBase), short(newBase))
	}
	fmt.Fprintf(&b, "\n%s\n", rd)
	// range-diff names a commit with no counterpart in the reviewed series
	// ("-:  ------- > 2:  abc1234 subject") without its patch: add it.
	differs := false
	for _, line := range strings.Split(rd, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || strings.HasPrefix(line, " ") || !strings.HasSuffix(f[0], ":") {
			continue // not a commit pair
		}
		differs = differs || f[2] != "="
		if f[0] != "-:" || f[2] != ">" {
			continue
		}
		show, err := git(dir, "show", "--no-color", "--no-ext-diff", "--format=commit %H%n%n    %s", f[4])
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n--- new since the review: %s\n%s\n", f[4], show)
	}
	// Every commit matches, yet the whole change differs: the difference is
	// in what range-diff leaves out, such as an edit in a merge commit.
	if !differs {
		full, err := git(dir, "diff", "--no-color", "--no-ext-diff", newBase, new)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\nThe commits match, but the whole change differs: the full diff %s..%s follows.\n\n%s\n", short(newBase), short(new), full)
	}
	fmt.Fprintf(&b, "\nMerge commits are left out; `git diff %s %s` shows everything between the two heads.\n", short(old), short(new))
	return b.String(), nil
}

// patchID is the patch ID of the whole change from base to head: equal for
// the same change on any base, however it is split into commits. It is
// --verbatim, as whitespace can matter (indentation, string literals): the
// default ID ignores it.
func patchID(dir, base, head string) (string, error) {
	diff := exec.Command("git", "-C", dir, "diff", "--no-color", "--no-ext-diff", base, head)
	id := exec.Command("git", "-C", dir, "patch-id", "--verbatim")
	var out, errb bytes.Buffer
	pipe, err := diff.StdoutPipe()
	if err != nil {
		return "", err
	}
	id.Stdin, id.Stdout, diff.Stderr, id.Stderr = pipe, &out, &errb, &errb
	if err := diff.Start(); err != nil {
		return "", err
	}
	if err := id.Run(); err != nil {
		_ = diff.Wait()
		return "", fmt.Errorf("git patch-id: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	if err := diff.Wait(); err != nil {
		return "", fmt.Errorf("git diff %s %s: %w: %s", short(base), short(head), err, strings.TrimSpace(errb.String()))
	}
	// "<patch-id> <commit-id>": the commit is all zeros for a diff.
	f := strings.Fields(out.String())
	if len(f) == 0 {
		return "", nil // no change at all
	}
	return f[0], nil
}

func short(sha string) string { return sha[:min(10, len(sha))] }

// printSinceReview prints the --since-review section of pr show.
func printSinceReview(out io.Writer, st *styles, rev server.ReviewOut, text string) {
	fmt.Fprintf(out, "\n--- since review %s by %s at %s\n%s",
		st.reviewVerdict(rev.State), st.Bold.Render(rev.Reviewer), st.Dim.Render(short(rev.CommitID)), text)
}
