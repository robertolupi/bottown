package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// After a push to a PR's head branch, Forgejo reports the new head at once
// (the API's head.sha is the branch), but processes the push in the
// background: it updates refs/pull/N/head, rechecks mergeability, marks
// reviews stale and dismisses approvals for the new commits. Until then the
// PR's diff, merge base and mergeability are from the older head, and an
// approval submitted at the new head can be dismissed as if it predated it.
//
// So bottown reads an open PR's diff and merge base from git at the head,
// and waits until refs/pull/N/head is the head before it pins a verify run
// or submits its review (#305).

// pushSyncWait bounds the wait for Forgejo to process a push.
const pushSyncWait = 30 * time.Second

// pushSyncPoll is how often the wait looks at the PR ref.
var pushSyncPoll = 50 * time.Millisecond

func (s *Server) repoGitDir(org, repo string) string {
	return filepath.Join(s.GitRoot, strings.ToLower(org), strings.ToLower(repo)+".git")
}

// prGit is an open PR's state in its git repository, at a given head.
type prGit struct {
	gitDir    string
	head      string
	mergeBase string
	synced    bool // refs/pull/N/head is head
}

// openPRGit reads PR n's merge base with baseBranch and whether Forgejo has
// processed the push of head. It returns nil when the repositories are not
// available.
func (s *Server) openPRGit(ctx context.Context, org, repo string, n int64, baseBranch, head string) (*prGit, error) {
	if s.GitRoot == "" {
		return nil, nil
	}
	gitDir := s.repoGitDir(org, repo)
	if _, err := os.Stat(gitDir); err != nil {
		return nil, err
	}
	mb, err := mergeGit(ctx, gitDir, nil, nil, "merge-base", "refs/heads/"+baseBranch, head)
	if err != nil {
		return nil, err
	}
	return &prGit{gitDir: gitDir, head: head, mergeBase: strings.TrimSpace(mb), synced: prRef(ctx, gitDir, n) == head}, nil
}

// diff is the PR's change, from the merge base to the head, as Forgejo's
// .diff endpoint shows it.
func (g *prGit) diff(ctx context.Context) ([]byte, error) {
	out, err := mergeGit(ctx, g.gitDir, nil, nil, "diff", "-p", "-M", "--no-color", "--no-ext-diff", g.mergeBase, g.head, "--")
	return []byte(out), err
}

// prRef is the commit refs/pull/N/head points at, or "" if none.
func prRef(ctx context.Context, gitDir string, n int64) string {
	out, err := mergeGit(ctx, gitDir, nil, nil, "rev-parse", "--verify", "-q", fmt.Sprintf("refs/pull/%d/head^{commit}", n))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// awaitPushSynced waits, up to wait, until Forgejo has moved PR n's ref to
// head. It reports whether it has; without the repositories it cannot tell,
// and reports true.
func (s *Server) awaitPushSynced(ctx context.Context, org, repo string, n int64, head string, wait time.Duration) (bool, error) {
	if s.GitRoot == "" {
		return true, nil
	}
	gitDir := s.repoGitDir(org, repo)
	if _, err := os.Stat(gitDir); err != nil {
		return true, nil
	}
	deadline := time.Now().Add(wait)
	for {
		if prRef(ctx, gitDir, n) == head {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(pushSyncPoll):
		}
	}
}

// waitPushSynced is awaitPushSynced with the server's bound, logging when
// Forgejo did not catch up in time: the caller goes on, as it did before
// it waited.
func (s *Server) waitPushSynced(ctx context.Context, org, repo string, n int64, head, why string) error {
	ok, err := s.awaitPushSynced(ctx, org, repo, n, head, pushSyncWait)
	if err != nil {
		return err
	}
	if !ok {
		s.Log.Warn("Forgejo has not processed the push to the PR", "repo", repo, "pr", n, "head", head, "before", why, "waited", pushSyncWait)
	}
	return nil
}
