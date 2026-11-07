package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/gitmerge"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/secrets"
)

// The owner's operations on a milestone's integration branch,
// milestone/<slug>, behind `bottown milestone branch` and `finish`. They
// write as the manager account: the branch's protection lets only it and
// the owner push, and the final PR must be someone else's than the owner's
// for the owner to approve it (docs/design/milestones.md §3.6).
//
//	POST /v1/forge/milestone/branch      {repo, slug}
//	POST /v1/forge/milestone/merge-base  {repo, slug}
//	POST /v1/forge/milestone/pr          {repo, slug, sha, checks}
//	POST /v1/forge/milestone/restart     {repo, slug}

// MilestoneIn names a milestone plan in a repository.
type MilestoneIn struct {
	Repo string `json:"repo"`
	Slug string `json:"slug"`
}

// MilestoneBranchOut is the milestone branch, and whether this call
// created it.
type MilestoneBranchOut struct {
	Branch  string `json:"branch"`
	SHA     string `json:"sha"`
	Epic    int64  `json:"epic"`
	Created bool   `json:"created"`
}

// MilestoneMergeBaseOut is the milestone branch after the default branch was
// merged into it. Merged is false when the branch already contained it.
type MilestoneMergeBaseOut struct {
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	HeadSHA string `json:"head_sha"`
	BaseSHA string `json:"base_sha"`
	Epic    int64  `json:"epic"`
	Merged  bool   `json:"merged"`
}

// MilestoneRestartOut is the milestone branch after restart. Final is the
// open final PR, or else the newest merged one, 0 if there is neither;
// Merged is whether this call merged the default branch, Base, into it.
type MilestoneRestartOut struct {
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	HeadSHA string `json:"head_sha,omitempty"`
	Final   int64  `json:"final,omitempty"`
	Merged  bool   `json:"merged"`
}

// MilestonePRIn asks for the final PR at the head whose e2e gate passed;
// Checks is the gate's description, for the PR's.
type MilestonePRIn struct {
	MilestoneIn
	SHA    string `json:"sha"`
	Checks string `json:"checks,omitempty"`
}

// MilestonePROut is the final PR, and whether this call opened it.
type MilestonePROut struct {
	Number  int64  `json:"number"`
	URL     string `json:"url"`
	Created bool   `json:"created"`
}

// milestoneOp is what each operation starts from: the repository, the
// manager's client and the plan's epic with its active children.
type milestoneOp struct {
	org, repo, branch string
	manager           string
	c                 *forgejo.Client
	st                *milestone.EpicState
}

func (s *Server) milestoneOp(ctx context.Context, p secrets.Principal, in MilestoneIn) (*milestoneOp, error) {
	org, repo, err := s.resolveRepo(p, in.Repo)
	if err != nil {
		return nil, err
	}
	if !milestone.ValidSlug(in.Slug) {
		return nil, ferr(http.StatusBadRequest, "plan slug %q must be lowercase words joined by -", in.Slug)
	}
	manager := s.desired().Manager.Name
	if manager == "" {
		return nil, ferr(http.StatusNotImplemented, "no manager account is configured")
	}
	c, err := s.actorClient(ctx, manager)
	if err != nil {
		return nil, err
	}
	st, err := milestone.Epic(ctx, issueReader{c, org, repo}, s.Owner, in.Slug)
	if errors.Is(err, milestone.ErrNotFound) {
		return nil, ferr(http.StatusNotFound, "%s/%s: %v", org, repo, err)
	} else if err != nil {
		return nil, err
	}
	return &milestoneOp{org: org, repo: repo, branch: MilestoneBranchPrefix + in.Slug, manager: manager, c: c, st: st}, nil
}

// refuseOpen refuses while active children are still open.
func (m *milestoneOp) refuseOpen() error {
	open := m.st.Open()
	if len(open) == 0 {
		return nil
	}
	names := make([]string, len(open))
	for i, is := range open {
		names[i] = fmt.Sprintf("#%d %s", is.Number, is.Title)
	}
	return ferr(http.StatusConflict, "epic #%d has %d open active child issue(s): %s", m.st.Epic.Number, len(open), strings.Join(names, "; "))
}

func (s *Server) opMilestoneBranch(ctx context.Context, p secrets.Principal, in MilestoneIn) (MilestoneBranchOut, error) {
	m, err := s.milestoneOp(ctx, p, in)
	if err != nil {
		return MilestoneBranchOut{}, err
	}
	out := MilestoneBranchOut{Branch: m.branch, Epic: m.st.Epic.Number}
	switch b, err := m.c.Branch(m.org, m.repo, m.branch); {
	case err == nil:
		out.SHA = b.Commit
		return out, nil
	case !forgejo.IsRawNotFound(err):
		return out, fmt.Errorf("branch %s: %w", m.branch, err)
	}
	r, _, err := m.c.GetRepo(m.org, m.repo)
	if err != nil {
		return out, err
	}
	b, _, err := m.c.CreateBranch(m.org, m.repo, sdk.CreateBranchOption{BranchName: m.branch, OldBranchName: r.DefaultBranch})
	if err != nil {
		return out, fmt.Errorf("creating %s from %s as %s: %w", m.branch, r.DefaultBranch, m.manager, err)
	}
	if b.Commit != nil {
		out.SHA = b.Commit.ID
	}
	out.Created = true
	s.toolAudit(p, m.org+"/"+m.repo, "milestone_branch", "allow", fmt.Sprintf("created %s from %s at %s", m.branch, r.DefaultBranch, shortCommit(out.SHA)))
	return out, nil
}

// opMilestoneMergeBase merges the default branch into the milestone
// branch, as the manager, once every active child is closed. A conflict
// leaves the branch as it was.
func (s *Server) opMilestoneMergeBase(ctx context.Context, p secrets.Principal, in MilestoneIn) (MilestoneMergeBaseOut, error) {
	if s.GitRoot == "" {
		return MilestoneMergeBaseOut{}, ferr(http.StatusNotImplemented, "forge git repositories are not available")
	}
	m, err := s.milestoneOp(ctx, p, in)
	if err != nil {
		return MilestoneMergeBaseOut{}, err
	}
	if err := m.refuseOpen(); err != nil {
		return MilestoneMergeBaseOut{}, err
	}
	r, _, err := m.c.GetRepo(m.org, m.repo)
	if err != nil {
		return MilestoneMergeBaseOut{}, err
	}
	message := fmt.Sprintf("Merge %s into %s\n\nbottown milestone finish for epic #%d, at the owner's request.\n", r.DefaultBranch, m.branch, m.st.Epic.Number)
	return s.mergeDefault(ctx, p, m, r.DefaultBranch, in.Slug, message, "milestone_merge_base")
}

// opMilestoneRestart starts a new round of a milestone whose final PR was
// merged: it merges the default branch into the milestone branch, as the
// manager, so the round's children start from it. It does nothing while a
// final PR is open, when none was merged, or when the branch already
// contains the last one's merge. Unlike merge-base, open children do not
// refuse it: they are the new round's.
func (s *Server) opMilestoneRestart(ctx context.Context, p secrets.Principal, in MilestoneIn) (MilestoneRestartOut, error) {
	if s.GitRoot == "" {
		return MilestoneRestartOut{}, ferr(http.StatusNotImplemented, "forge git repositories are not available")
	}
	m, err := s.milestoneOp(ctx, p, in)
	if err != nil {
		return MilestoneRestartOut{}, err
	}
	r, _, err := m.c.GetRepo(m.org, m.repo)
	if err != nil {
		return MilestoneRestartOut{}, err
	}
	out := MilestoneRestartOut{Branch: m.branch, Base: r.DefaultBranch}
	prs, _, err := m.c.AllPullRequests(m.org, m.repo, sdk.ListPullRequestsOptions{State: sdk.StateAll})
	if err != nil {
		return out, fmt.Errorf("listing pull requests: %w", err)
	}
	var last *sdk.PullRequest // the newest merged final PR
	for _, pr := range prs {
		if pr.Head == nil || pr.Base == nil || pr.Head.Ref != m.branch || pr.Base.Ref != r.DefaultBranch {
			continue
		}
		if pr.State == sdk.StateOpen {
			out.Final = pr.Index
			return out, nil
		}
		if pr.HasMerged && pr.MergedCommitID != nil && (last == nil || pr.Index > last.Index) {
			last = pr
		}
	}
	if last == nil {
		return out, nil
	}
	out.Final = last.Index
	gitDir := m.gitDir(s.GitRoot)
	head, err := m.head(ctx, gitDir, in.Slug)
	if err != nil {
		return out, err
	}
	out.HeadSHA = head
	switch contains, err := gitmerge.IsAncestor(ctx, gitDir, *last.MergedCommitID, head); {
	case err != nil:
		return out, err
	case contains:
		return out, nil
	}
	message := fmt.Sprintf("Merge %s into %s\n\nbottown milestone run for epic #%d: the final PR !%d was merged, so the new round starts from %s.\n",
		r.DefaultBranch, m.branch, m.st.Epic.Number, last.Index, r.DefaultBranch)
	merged, err := s.mergeDefault(ctx, p, m, r.DefaultBranch, in.Slug, message, "milestone_restart")
	out.HeadSHA, out.Merged = merged.HeadSHA, merged.Merged
	return out, err
}

// mergeDefault merges the default branch, base, into the milestone branch
// as the manager, with message, unless the branch already contains it. A
// conflict leaves the branch as it was.
func (s *Server) mergeDefault(ctx context.Context, p secrets.Principal, m *milestoneOp, base, slug, message, audit string) (MilestoneMergeBaseOut, error) {
	out := MilestoneMergeBaseOut{Branch: m.branch, Base: base, Epic: m.st.Epic.Number}
	gitDir := m.gitDir(s.GitRoot)
	head, err := m.head(ctx, gitDir, slug)
	if err != nil {
		return out, err
	}
	baseSHA, err := mergeGit(ctx, gitDir, nil, nil, "rev-parse", "--verify", "refs/heads/"+out.Base+"^{commit}")
	if err != nil {
		return out, err
	}
	out.HeadSHA, out.BaseSHA = head, strings.TrimSpace(baseSHA)
	contains, err := gitmerge.IsAncestor(ctx, gitDir, out.BaseSHA, out.HeadSHA)
	if err != nil || contains {
		return out, err
	}
	merged, err := gitmerge.MergeTree(ctx, gitDir, out.HeadSHA, out.BaseSHA)
	if err != nil {
		return out, err
	}
	if len(merged.Conflicts) > 0 {
		return out, ferr(http.StatusConflict, "merging %s into %s conflicts in %s; %s is unchanged at %s: merge and resolve it by hand",
			out.Base, m.branch, strings.Join(merged.Conflicts, ", "), m.branch, shortCommit(out.HeadSHA))
	}
	commit, err := mergeGit(ctx, gitDir, strings.NewReader(message), []string{
		"GIT_AUTHOR_NAME=" + m.manager, "GIT_AUTHOR_EMAIL=" + m.manager + "@bottown.local",
		"GIT_COMMITTER_NAME=" + m.manager, "GIT_COMMITTER_EMAIL=" + m.manager + "@bottown.local",
	}, "commit-tree", merged.Tree, "-p", out.HeadSHA, "-p", out.BaseSHA)
	if err != nil {
		return out, err
	}
	commit = strings.TrimSpace(commit)
	if err := s.milestonePush(ctx, m, gitDir, commit); err != nil {
		return out, err
	}
	out.HeadSHA, out.Merged = commit, true
	s.toolAudit(p, m.org+"/"+m.repo, audit, "allow", fmt.Sprintf("%s merged into %s at %s", out.Base, m.branch, shortCommit(commit)))
	return out, nil
}

// gitDir is the repository's bare git directory under root.
func (m *milestoneOp) gitDir(root string) string {
	return filepath.Join(root, strings.ToLower(m.org), strings.ToLower(m.repo)+".git")
}

// head is the milestone branch's head commit in gitDir.
func (m *milestoneOp) head(ctx context.Context, gitDir, slug string) (string, error) {
	head, err := mergeGit(ctx, gitDir, nil, nil, "rev-parse", "--verify", "refs/heads/"+m.branch+"^{commit}")
	if err != nil {
		return "", ferr(http.StatusNotFound, "%s does not exist: create it with `bottown milestone branch %s`", m.branch, slug)
	}
	return strings.TrimSpace(head), nil
}

// milestonePush pushes commit to the milestone branch as the manager,
// without force: a branch that moved since is refused.
func (s *Server) milestonePush(ctx context.Context, m *milestoneOp, gitDir, commit string) error {
	if s.milestonePushFn != nil {
		return s.milestonePushFn(ctx, gitDir, m.branch, commit)
	}
	token, ok := s.actorToken(m.manager)
	if !ok {
		return ferr(http.StatusForbidden, "no forge token for %s", m.manager)
	}
	remote := strings.TrimRight(s.Rec.BaseURL, "/") + "/" + m.org + "/" + m.repo + ".git"
	auth := base64.StdEncoding.EncodeToString([]byte(m.manager + ":" + token))
	if _, err := mergeGit(ctx, gitDir, nil, []string{
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + auth,
	}, "push", remote, commit+":refs/heads/"+m.branch); err != nil {
		return fmt.Errorf("push the merge to %s: %w", m.branch, err)
	}
	return nil
}

// opMilestonePR opens the final PR from the milestone branch into the
// default branch as the manager, or updates the description of the one
// open, once the e2e gate passed at the branch's head.
func (s *Server) opMilestonePR(ctx context.Context, p secrets.Principal, in MilestonePRIn) (MilestonePROut, error) {
	if in.SHA == "" {
		return MilestonePROut{}, ferr(http.StatusBadRequest, "sha is required")
	}
	m, err := s.milestoneOp(ctx, p, in.MilestoneIn)
	if err != nil {
		return MilestonePROut{}, err
	}
	if err := m.refuseOpen(); err != nil {
		return MilestonePROut{}, err
	}
	head, err := m.c.BranchHead(m.org, m.repo, m.branch)
	if err != nil {
		return MilestonePROut{}, fmt.Errorf("branch %s: %w", m.branch, err)
	}
	if head != in.SHA {
		return MilestonePROut{}, ferr(http.StatusConflict, "%s moved from %s to %s since its checks; run finish again", m.branch, shortCommit(in.SHA), shortCommit(head))
	}
	r, _, err := m.c.GetRepo(m.org, m.repo)
	if err != nil {
		return MilestonePROut{}, err
	}
	prs, _, err := m.c.AllPullRequests(m.org, m.repo, sdk.ListPullRequestsOptions{State: sdk.StateAll})
	if err != nil {
		return MilestonePROut{}, fmt.Errorf("listing pull requests: %w", err)
	}
	body := finalPRBody(m, prs, in)
	for _, pr := range prs {
		if pr.State != sdk.StateOpen || pr.Head == nil || pr.Base == nil || pr.Head.Ref != m.branch || pr.Base.Ref != r.DefaultBranch {
			continue
		}
		if _, _, err := m.c.EditPullRequest(m.org, m.repo, pr.Index, sdk.EditPullRequestOption{Body: &body}); err != nil {
			return MilestonePROut{}, fmt.Errorf("updating !%d: %w", pr.Index, err)
		}
		s.toolAudit(p, m.org+"/"+m.repo, "milestone_pr", "allow", fmt.Sprintf("updated !%d at %s", pr.Index, shortCommit(in.SHA)))
		return MilestonePROut{Number: pr.Index, URL: pr.HTMLURL}, nil
	}
	pr, _, err := m.c.CreatePullRequest(m.org, m.repo, sdk.CreatePullRequestOption{
		Head: m.branch, Base: r.DefaultBranch, Title: m.st.Epic.Title, Body: body,
	})
	if err != nil {
		return MilestonePROut{}, fmt.Errorf("opening the PR from %s into %s as %s: %w", m.branch, r.DefaultBranch, m.manager, err)
	}
	s.toolAudit(p, m.org+"/"+m.repo, "milestone_pr", "allow", fmt.Sprintf("opened !%d at %s", pr.Index, shortCommit(in.SHA)))
	return MilestonePROut{Number: pr.Index, URL: pr.HTMLURL, Created: true}, nil
}

// finalPRBody describes the final PR: the merged active children with
// their PRs into the milestone branch, and the keyword that closes the
// epic when the owner merges it.
func finalPRBody(m *milestoneOp, prs []*sdk.PullRequest, in MilestonePRIn) string {
	merged := map[string][]int64{} // head branch → its PRs merged into the milestone branch
	for _, pr := range prs {
		if pr.HasMerged && pr.Head != nil && pr.Base != nil && pr.Base.Ref == m.branch {
			merged[pr.Head.Ref] = append(merged[pr.Head.Ref], pr.Index)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The integration branch `%s` of milestone plan `%s`, for the Implementation Epic #%d, opened by `bottown milestone finish`.\n\n",
		m.branch, in.Slug, m.st.Epic.Number)
	fmt.Fprintf(&b, "The e2e gate passed at `%s`", shortCommit(in.SHA))
	if in.Checks != "" {
		fmt.Fprintf(&b, ": %s", in.Checks)
	}
	b.WriteString(".\n\n## Merged child issues\n\n")
	if len(m.st.Children) == 0 {
		b.WriteString("None.\n")
	}
	for _, c := range m.st.Children {
		fmt.Fprintf(&b, "- #%d — %s: ", c.Number, c.Title)
		ns := merged[fmt.Sprintf("issue/%d", c.Number)]
		if len(ns) == 0 {
			fmt.Fprintf(&b, "closed with no PR merged into `%s`\n", m.branch)
			continue
		}
		refs := make([]string, len(ns))
		for i, n := range ns {
			refs[i] = fmt.Sprintf("!%d", n)
		}
		b.WriteString(strings.Join(refs, ", ") + "\n")
	}
	fmt.Fprintf(&b, "\nCloses #%d\n", m.st.Epic.Number)
	return b.String()
}

// issueReader is milestone.Reader over a Forgejo client.
type issueReader struct {
	c         *forgejo.Client
	org, repo string
}

func (r issueReader) Issues(context.Context) ([]*milestone.ForgeIssue, error) {
	list, _, err := r.c.AllIssues(r.org, r.repo, sdk.ListIssueOption{State: sdk.StateAll, Type: sdk.IssueTypeIssue})
	if err != nil {
		return nil, err
	}
	out := make([]*milestone.ForgeIssue, 0, len(list))
	for _, is := range list {
		if is.PullRequest != nil {
			continue
		}
		fi := &milestone.ForgeIssue{Number: is.Index, Title: is.Title, Body: is.Body, Closed: is.State == sdk.StateClosed}
		if is.Poster != nil {
			fi.Poster = is.Poster.UserName
		}
		out = append(out, fi)
	}
	return out, nil
}

func (r issueReader) BlockedBy(_ context.Context, n int64) ([]int64, error) {
	deps, err := r.c.AllIssueDependencies(r.org, r.repo, n)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(deps))
	for i, d := range deps {
		out[i] = d.Index
	}
	return out, nil
}
