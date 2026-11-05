package review

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/robertolupi/bottown/internal/runs"
)

// SamePatch reports whether old and head carry the same change: the diff
// each makes against its merge base with base (a branch ref) has the same
// `git patch-id --stable`, so the change was only rebased onto, or merged
// with, a newer base. Like an empty `git range-diff`, it counts a change to
// the diff's context lines as a change. It returns head's merge base, the
// base the patch now sits on. gitDir is the repository, bare or not.
func SamePatch(ctx context.Context, gitDir, base, old, head string) (string, bool, error) {
	for _, c := range []string{old, head} {
		if !isSHA(c) {
			return "", false, fmt.Errorf("not a commit id: %q", c)
		}
	}
	if strings.HasPrefix(base, "-") {
		return "", false, fmt.Errorf("not a branch: %q", base)
	}
	oldID, _, err := patchID(ctx, gitDir, base, old)
	if err != nil {
		return "", false, err
	}
	headID, onto, err := patchID(ctx, gitDir, base, head)
	if err != nil {
		return "", false, err
	}
	return onto, oldID != "" && oldID == headID, nil
}

// patchID is commit's patch id against its merge base with base, and that
// merge base; "" for an empty diff.
func patchID(ctx context.Context, gitDir, base, commit string) (string, string, error) {
	mb, err := git(ctx, gitDir, nil, "merge-base", base, commit)
	if err != nil {
		return "", "", err
	}
	mb = strings.TrimSpace(mb)
	diff, err := git(ctx, gitDir, nil, "diff", "--no-ext-diff", "--no-textconv", mb, commit, "--")
	if err != nil {
		return "", "", err
	}
	out, err := git(ctx, gitDir, strings.NewReader(diff), "patch-id", "--stable")
	if err != nil {
		return "", "", err
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out), " ")
	return id, mb, nil
}

func isSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}

func git(ctx context.Context, gitDir string, stdin *strings.Reader, args ...string) (string, error) {
	cmd := runs.GitCommand(ctx, "", []string{"--git-dir", gitDir}, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
