package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/secrets"
)

// MilestoneMergeIn asks to merge a milestone child's PR into its
// milestone branch at the head the caller expects.
type MilestoneMergeIn struct {
	Target
	HeadSHA string `json:"head_sha"`
}

// MilestoneMergeOut is a child PR merged into its milestone branch, now or
// by an earlier call (AlreadyMerged).
type MilestoneMergeOut struct {
	Number        int64  `json:"number"`
	Issue         int64  `json:"issue"`
	Base          string `json:"base"`
	HeadSHA       string `json:"head_sha"`
	MergeCommit   string `json:"merge_commit,omitempty"`
	AlreadyMerged bool   `json:"already_merged,omitempty"`
	// IssueClosed says this call closed the child issue; false when it was
	// closed already.
	IssueClosed bool `json:"issue_closed,omitempty"`
}

// opMilestoneMerge merges a child PR into its milestone/* base as the
// manager account, closes the child issue and comments on it. It is the
// only operation through which bottown merges: only into a milestone
// branch, and only at a head the review assessment approves.
func (s *Server) opMilestoneMerge(ctx context.Context, p secrets.Principal, in MilestoneMergeIn) (MilestoneMergeOut, error) {
	deny := func(repo string, err error) (MilestoneMergeOut, error) {
		s.toolAudit(p, repo, "milestone_merge", "deny", err.Error())
		return MilestoneMergeOut{}, err
	}
	if p.Actor != s.Owner || p.Run != nil {
		return deny("", ferr(http.StatusForbidden, "owner only"))
	}
	org, repo, n, err := s.resolve(p, in.Target, 0)
	if err != nil {
		return MilestoneMergeOut{}, err
	}
	full := org + "/" + repo
	if in.HeadSHA == "" {
		return MilestoneMergeOut{}, ferr(http.StatusBadRequest, "head_sha is required")
	}
	manager := s.desired().Manager.Name
	if manager == "" {
		return deny(full, ferr(http.StatusNotImplemented, "no manager account is configured"))
	}
	mc, err := s.actorClient(ctx, manager)
	if err != nil {
		return deny(full, err)
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return MilestoneMergeOut{}, err
	}
	pr, resp, err := c.GetPullRequest(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return MilestoneMergeOut{}, ferr(http.StatusNotFound, "pull request #%d not found in %s", n, repo)
	} else if err != nil {
		return MilestoneMergeOut{}, err
	}
	if pr.Head == nil || pr.Base == nil {
		return MilestoneMergeOut{}, fmt.Errorf("pull request !%d has no head or base", n)
	}
	base, head := pr.Base.Ref, pr.Head.Sha
	if !strings.HasPrefix(base, "milestone/") || base == "milestone/" {
		return deny(full, ferr(http.StatusConflict, "!%d's base is %s, not a milestone/* branch", n, base))
	}
	issue, ok := childIssue(pr.Head.Ref)
	if !ok {
		return deny(full, ferr(http.StatusConflict, "!%d's head branch is %s, not issue/<n>: no child issue to close", n, pr.Head.Ref))
	}
	out := MilestoneMergeOut{Number: n, Issue: issue, Base: base, HeadSHA: head}
	if pr.HasMerged {
		// A repeated call: finish closing the child if an earlier one
		// stopped after the merge.
		out.AlreadyMerged, out.MergeCommit = true, deref(pr.MergedCommitID)
		if out.IssueClosed, err = s.closeChild(mc, org, repo, out); err != nil {
			return out, err
		}
		return out, nil
	}
	if pr.State != sdk.StateOpen {
		return deny(full, ferr(http.StatusConflict, "!%d is closed without being merged", n))
	}
	if head != in.HeadSHA {
		return deny(full, ferr(http.StatusConflict, "!%d's head is %s, not %s", n, shortCommit(head), shortCommit(in.HeadSHA)))
	}
	rs, _, err := c.AllReviews(org, repo, n, sdk.ListPullReviewsOptions{})
	if err != nil {
		return MilestoneMergeOut{}, err
	}
	runs, err := s.runRecords()
	if err != nil {
		return MilestoneMergeOut{}, err
	}
	a := assessReviews(s.desired(), s.Owner, runs, repo, pr, rs, s.carryOver(ctx, org, repo, pr, rs))
	if a.Decision != review.Approved {
		return deny(full, ferr(http.StatusConflict, "!%d is not approved at %s: %s", n, shortCommit(head), a.Decision))
	}
	_, resp, err = mc.MergePullRequest(org, repo, n, sdk.MergePullRequestOption{
		Style:        sdk.MergeStyleMerge,
		HeadCommitId: head,
		Title:        fmt.Sprintf("Merge !%d (%s) into %s", n, pr.Title, base),
		Message:      fmt.Sprintf("Approved at %s; merged by bottown as %s for #%d.", head, manager, issue),
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusConflict {
			return deny(full, ferr(http.StatusConflict, "forge refused to merge !%d: %v", n, err))
		}
		return MilestoneMergeOut{}, fmt.Errorf("merge !%d: %w", n, err)
	}
	if merged, _, err := c.GetPullRequest(org, repo, n); err == nil {
		out.MergeCommit = deref(merged.MergedCommitID)
	}
	s.toolAudit(p, full, "milestone_merge", "allow", fmt.Sprintf("!%d at %s into %s as %s", n, shortCommit(head), base, manager))
	if out.IssueClosed, err = s.closeChild(mc, org, repo, out); err != nil {
		return out, err
	}
	return out, nil
}

// closeChild closes the merged PR's child issue as the manager, because
// Forgejo applies closing keywords only on merges into the default branch,
// and comments on it with what was merged. An issue closed already is left
// alone, so repeated calls comment once.
func (s *Server) closeChild(mc *forgejo.Client, org, repo string, out MilestoneMergeOut) (bool, error) {
	is, _, err := mc.GetIssue(org, repo, out.Issue)
	if err != nil {
		return false, fmt.Errorf("read child issue #%d: %w", out.Issue, err)
	}
	if is.State == sdk.StateClosed {
		return false, nil
	}
	closed := sdk.StateClosed
	if _, _, err := mc.EditIssue(org, repo, out.Issue, sdk.EditIssueOption{State: &closed}); err != nil {
		return false, fmt.Errorf("close child issue #%d: %w", out.Issue, err)
	}
	body := fmt.Sprintf("**bottown: merged !%d into `%s` at %s.**", out.Number, out.Base, out.HeadSHA)
	if out.MergeCommit != "" {
		body += fmt.Sprintf(" Merge commit %s.", out.MergeCommit)
	}
	if _, _, err := mc.CreateIssueComment(org, repo, out.Issue, sdk.CreateIssueCommentOption{Body: body}); err != nil {
		return true, fmt.Errorf("comment on child issue #%d: %w", out.Issue, err)
	}
	return true, nil
}

// childIssue is the issue number of an issue/<n> branch.
func childIssue(branch string) (int64, bool) {
	v, ok := strings.CutPrefix(branch, "issue/")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n > 0
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
