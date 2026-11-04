package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/gitmerge"
	"github.com/robertolupi/bottown/internal/secrets"
)

// PRMergeStatus is the result of merging the base into a PR head without a
// worktree. HeadSHA and BaseSHA identify the forge refs that were tested.
type PRMergeStatus struct {
	Behind    bool     `json:"behind"`
	Clean     bool     `json:"clean"`
	HeadSHA   string   `json:"head_sha"`
	BaseSHA   string   `json:"base_sha"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// PRMergeIn asks to merge the current base into the PR head the owner saw.
type PRMergeIn struct {
	Target
	HeadSHA string `json:"head_sha"`
}

// PRMergeOut identifies the automatic merge commit pushed to the PR branch.
type PRMergeOut struct {
	HeadSHA string `json:"head_sha"`
	BaseSHA string `json:"base_sha"`
}

type mergeState struct {
	PRMergeStatus
	org        string
	repo       string
	gitDir     string
	tree       string
	headBranch string
	baseBranch string
}

func (s *Server) opPRMergeStatus(ctx context.Context, p secrets.Principal, in Target) (PRMergeStatus, error) {
	state, err := s.prMergeState(ctx, p, in)
	return state.PRMergeStatus, err
}

func (s *Server) prMergeState(ctx context.Context, p secrets.Principal, in Target) (mergeState, error) {
	if s.GitRoot == "" {
		return mergeState{}, ferr(http.StatusNotImplemented, "forge git repositories are not available")
	}
	org, repo, n, err := s.resolve(p, in, 0)
	if err != nil {
		return mergeState{}, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return mergeState{}, err
	}
	pr, resp, err := c.GetPullRequest(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return mergeState{}, ferr(http.StatusNotFound, "pull request #%d not found in %s", n, repo)
	} else if err != nil {
		return mergeState{}, err
	}
	if pr.State != sdk.StateOpen || pr.HasMerged || pr.Head == nil || pr.Base == nil {
		return mergeState{}, ferr(http.StatusConflict, "pull request !%d is not open", n)
	}
	gitDir := filepath.Join(s.GitRoot, strings.ToLower(org), strings.ToLower(repo)+".git")
	headRef, baseRef := "refs/heads/"+pr.Head.Ref, "refs/heads/"+pr.Base.Ref
	head, err := mergeGit(ctx, gitDir, nil, nil, "rev-parse", "--verify", headRef+"^{commit}")
	if err != nil {
		return mergeState{}, err
	}
	base, err := mergeGit(ctx, gitDir, nil, nil, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return mergeState{}, err
	}
	state := mergeState{PRMergeStatus: PRMergeStatus{
		Clean: true, HeadSHA: strings.TrimSpace(head), BaseSHA: strings.TrimSpace(base),
	}, org: org, repo: repo, gitDir: gitDir, headBranch: pr.Head.Ref, baseBranch: pr.Base.Ref}
	contains, err := gitmerge.IsAncestor(ctx, gitDir, state.BaseSHA, state.HeadSHA)
	if err != nil {
		return mergeState{}, err
	}
	if contains {
		return state, nil
	}
	state.Behind = true
	merged, err := gitmerge.MergeTree(ctx, gitDir, state.HeadSHA, state.BaseSHA)
	if err != nil {
		return mergeState{}, err
	}
	state.tree = merged.Tree
	state.Clean = len(merged.Conflicts) == 0
	state.Conflicts = merged.Conflicts
	return state, nil
}

func (s *Server) opPRMerge(ctx context.Context, p secrets.Principal, in PRMergeIn) (PRMergeOut, error) {
	if in.HeadSHA == "" {
		return PRMergeOut{}, ferr(http.StatusBadRequest, "head_sha is required")
	}
	state, err := s.prMergeState(ctx, p, in.Target)
	if err != nil {
		return PRMergeOut{}, err
	}
	if state.HeadSHA != in.HeadSHA {
		return PRMergeOut{}, ferr(http.StatusConflict, "PR head moved from %s to %s; reload and try again", shortCommit(in.HeadSHA), shortCommit(state.HeadSHA))
	}
	if !state.Behind {
		return PRMergeOut{}, ferr(http.StatusConflict, "PR is not behind %s", state.baseBranch)
	}
	if !state.Clean {
		return PRMergeOut{}, ferr(http.StatusConflict, "merge conflicts in %s", strings.Join(state.Conflicts, ", "))
	}
	message := fmt.Sprintf("Merge %s into %s for !%d\n\nAutomatic clean merge by bottown at the owner's request.\n", state.baseBranch, state.headBranch, in.Number)
	commit, err := mergeGit(ctx, state.gitDir, strings.NewReader(message), []string{
		"GIT_AUTHOR_NAME=bottown", "GIT_AUTHOR_EMAIL=bottown@localhost",
		"GIT_COMMITTER_NAME=bottown", "GIT_COMMITTER_EMAIL=bottown@localhost",
	}, "commit-tree", state.tree, "-p", state.HeadSHA, "-p", state.BaseSHA)
	if err != nil {
		return PRMergeOut{}, err
	}
	commit = strings.TrimSpace(commit)
	if err := s.pushMerge(ctx, state, commit); err != nil {
		return PRMergeOut{}, err
	}
	s.toolAudit(p, state.org+"/"+state.repo, "merge_base", "allow", fmt.Sprintf("!%d %s merged into %s at %s", in.Number, state.baseBranch, state.headBranch, shortCommit(commit)))
	return PRMergeOut{HeadSHA: commit, BaseSHA: state.BaseSHA}, nil
}

func (s *Server) pushMerge(ctx context.Context, state mergeState, commit string) error {
	token, ok := s.actorToken(s.Owner)
	if !ok {
		return ferr(http.StatusForbidden, "no forge token for %s", s.Owner)
	}
	remote := strings.TrimRight(s.Rec.BaseURL, "/") + "/" + state.org + "/" + state.repo + ".git"
	auth := base64.StdEncoding.EncodeToString([]byte(s.Owner + ":" + token))
	_, err := mergeGit(ctx, state.gitDir, nil, []string{
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + auth,
	}, "push", remote, commit+":refs/heads/"+state.headBranch)
	if err != nil {
		return fmt.Errorf("push automatic merge: %w", err)
	}
	return nil
}

func mergeGit(ctx context.Context, gitDir string, stdin *strings.Reader, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir", gitDir}, args...)...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
