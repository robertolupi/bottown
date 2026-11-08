package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/gitmerge"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/runs"
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
	// Files are the paths the PR's own delta touches, from the merge base
	// to the head, a rename counted as both paths. merge-all orders the
	// batch by their overlap (design §5.2).
	Files []string `json:"files,omitempty"`
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

// PRLandingIn asks to build and push the landing commit for the PR head the
// owner saw.
type PRLandingIn struct {
	Target
	HeadSHA string `json:"head_sha"`
}

// PRLandingOut is the landing commit pushed to the PR branch, the base it
// contains, and the assessment at it (design §4 step 3).
type PRLandingOut struct {
	HeadSHA    string            `json:"head_sha"`
	BaseSHA    string            `json:"base_sha"`
	Assessment review.Assessment `json:"assessment"`
}

type mergeState struct {
	PRMergeStatus
	org        string
	repo       string
	gitDir     string
	tree       string
	title      string
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
	}, org: org, repo: repo, gitDir: gitDir, title: pr.Title, headBranch: pr.Head.Ref, baseBranch: pr.Base.Ref}
	files, err := mergeGit(ctx, gitDir, nil, nil, "diff", "--name-only", "--no-renames", state.BaseSHA+"..."+state.HeadSHA, "--")
	if err != nil {
		return mergeState{}, err
	}
	state.Files = splitLines(files)
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
	// The offer may predate a run that started since: never move a branch
	// a live run works on.
	st, err := s.Store.Read()
	if err != nil {
		return PRMergeOut{}, err
	}
	if run, ok := liveRunOnPR(st.Runs, state.org, state.repo, in.Number, state.headBranch, time.Now()); ok {
		return PRMergeOut{}, ferr(http.StatusConflict, "run %s (%s by %s) is live on !%d; merge after it ends", run.ID, run.Kind, run.Actor, in.Number)
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

// opPRLanding builds the landing commit for a PR (design §4 step 3, §9): a
// merge commit on the PR branch whose first parent is the base's tip and
// whose second is the head, the tree of the trial merge (or the head's tree
// when it already contains the base's tip). It pushes the commit as the
// owner, waits for Forgejo to process the push, reconciles the approvals the
// push dismissed, and returns the new head, the base it contains and the
// assessment pr show would give at the new head.
func (s *Server) opPRLanding(ctx context.Context, p secrets.Principal, in PRLandingIn) (PRLandingOut, error) {
	if p.Actor != s.Owner || p.Run != nil {
		s.toolAudit(p, "", "landing", "deny", "owner only")
		return PRLandingOut{}, ferr(http.StatusForbidden, "owner only")
	}
	if in.HeadSHA == "" {
		return PRLandingOut{}, ferr(http.StatusBadRequest, "head_sha is required")
	}
	state, err := s.prMergeState(ctx, p, in.Target)
	if err != nil {
		return PRLandingOut{}, err
	}
	if state.HeadSHA != in.HeadSHA {
		return PRLandingOut{}, ferr(http.StatusConflict, "PR head moved from %s to %s; reload and try again", shortCommit(in.HeadSHA), shortCommit(state.HeadSHA))
	}
	if !state.Clean {
		return PRLandingOut{}, ferr(http.StatusConflict, "merge conflicts in %s", strings.Join(state.Conflicts, ", "))
	}
	// The offer may predate a run that started since: never move a branch
	// a live run works on.
	st, err := s.Store.Read()
	if err != nil {
		return PRLandingOut{}, err
	}
	if run, ok := liveRunOnPR(st.Runs, state.org, state.repo, in.Number, state.headBranch, time.Now()); ok {
		return PRLandingOut{}, ferr(http.StatusConflict, "run %s (%s by %s) is live on !%d; land after it ends", run.ID, run.Kind, run.Actor, in.Number)
	}
	// A PR that already contains the base's tip lands on its own tree; the
	// parents are the same, so main still gets one merge commit per PR.
	tree := state.tree
	if !state.Behind {
		headTree, err := mergeGit(ctx, state.gitDir, nil, nil, "rev-parse", "--verify", state.HeadSHA+"^{tree}")
		if err != nil {
			return PRLandingOut{}, err
		}
		tree = strings.TrimSpace(headTree)
	}
	message := fmt.Sprintf("Merge !%d (%s) into %s\n\nbottown landing commit: head %s, base %s.\n",
		in.Number, state.title, state.baseBranch, state.HeadSHA, state.BaseSHA)
	commit, err := mergeGit(ctx, state.gitDir, strings.NewReader(message), []string{
		"GIT_AUTHOR_NAME=bottown", "GIT_AUTHOR_EMAIL=bottown@localhost",
		"GIT_COMMITTER_NAME=bottown", "GIT_COMMITTER_EMAIL=bottown@localhost",
	}, "commit-tree", tree, "-p", state.BaseSHA, "-p", state.HeadSHA)
	if err != nil {
		return PRLandingOut{}, err
	}
	commit = strings.TrimSpace(commit)
	if err := s.pushMerge(ctx, state, commit); err != nil {
		return PRLandingOut{}, err
	}
	if err := s.waitPushSynced(ctx, state.org, state.repo, in.Number, commit, "landing"); err != nil {
		return PRLandingOut{}, err
	}
	out, err := s.landingAssessment(ctx, p, state, in.Number, commit)
	if err != nil {
		return PRLandingOut{}, err
	}
	s.toolAudit(p, state.org+"/"+state.repo, "landing", "allow", fmt.Sprintf("!%d landed at %s on base %s", in.Number, shortCommit(commit), shortCommit(state.BaseSHA)))
	return out, nil
}

// landingAssessment re-reads the PR and its reviews at the landing commit,
// undismisses the approvals the push dismissed when their patch carried
// over (opReconcileCarries' reconcile step), and assesses the new head.
func (s *Server) landingAssessment(ctx context.Context, p secrets.Principal, state mergeState, n int64, commit string) (PRLandingOut, error) {
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return PRLandingOut{}, err
	}
	pr, _, err := c.GetPullRequest(state.org, state.repo, n)
	if err != nil {
		return PRLandingOut{}, err
	}
	// The push is the head; pin it so the assessment is at the landing
	// commit even if Forgejo's read lags the branch (waitPushSynced warns
	// and goes on).
	if pr.Head != nil {
		pr.Head.Sha = commit
	}
	rs, _, err := c.AllReviews(state.org, state.repo, n, sdk.ListPullReviewsOptions{})
	if err != nil {
		return PRLandingOut{}, err
	}
	carries := s.carryOver(ctx, state.org, state.repo, pr, rs)
	s.undismissCarried(ctx, p, state.org, state.repo, n, commit, carries, rs)
	runs, err := s.runRecords()
	if err != nil {
		return PRLandingOut{}, err
	}
	return PRLandingOut{
		HeadSHA:    commit,
		BaseSHA:    state.BaseSHA,
		Assessment: assessReviews(s.desired(), s.Owner, runs, state.repo, pr, rs, carries),
	}, nil
}

// liveRunOnPR finds a live run on PR n of org/repo or its head branch:
// verify runs record the PR, implement and plan runs the branch and its
// issue.
func liveRunOnPR(all map[string]secrets.Run, org, repo string, n int64, branch string, now time.Time) (secrets.Run, bool) {
	var issue int64
	if v, ok := strings.CutPrefix(branch, "issue/"); ok {
		issue, _ = strconv.ParseInt(v, 10, 64)
	}
	for _, run := range all {
		if run.Ended != nil || (!run.Expires.IsZero() && now.After(run.Expires)) {
			continue
		}
		if !strings.EqualFold(run.Org, org) || !strings.EqualFold(run.Repo, repo) {
			continue
		}
		if run.PR == n || (branch != "" && run.Branch == branch) || (issue > 0 && run.Issue == issue) {
			return run, true
		}
	}
	return secrets.Run{}, false
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
	cmd := runs.GitCommand(ctx, "", []string{"--git-dir", gitDir}, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// splitLines splits git's line-per-item output, dropping the trailing empty
// line and any blank ones.
func splitLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
