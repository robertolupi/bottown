// Package gitmerge tests and creates merges without a worktree.
package gitmerge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/robertolupi/bottown/internal/runs"
)

// Result is a trial merge of head and base.
type Result struct {
	Tree      string
	Conflicts []string
}

// MergeTree tests merging base into head without touching a worktree.
// A conflict is a result, not an error; its tree must not be committed.
func MergeTree(ctx context.Context, gitDir, head, base string) (Result, error) {
	cmd := runs.GitCommand(ctx, "", []string{"--git-dir", gitDir}, "merge-tree", "--write-tree", "--name-only", head, base)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return Result{}, fmt.Errorf("git merge-tree: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return Result{}, fmt.Errorf("git merge-tree returned no tree: %s", strings.TrimSpace(stderr.String()))
	}
	result := Result{Tree: lines[0]}
	for _, line := range lines[1:] {
		if line == "" {
			break
		}
		result.Conflicts = append(result.Conflicts, line)
	}
	return result, nil
}

// IsAncestor reports whether ancestor is already contained in commit.
func IsAncestor(ctx context.Context, gitDir, ancestor, commit string) (bool, error) {
	cmd := runs.GitCommand(ctx, "", []string{"--git-dir", gitDir}, "merge-base", "--is-ancestor", ancestor, commit)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git merge-base: %w", err)
	}
	return true, nil
}
