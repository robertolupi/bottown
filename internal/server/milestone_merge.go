package server

import (
	"context"
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
	"github.com/robertolupi/bottown/internal/secrets"
)

// mergeReadyRetries bounds the retries of a fast-forward-only merge Forgejo
// refuses with 405 while it recomputes the pull request's mergeability
// after the landing push, and mergeReadyPoll is how long to wait between
// them.
const (
	mergeReadyRetries = 120
	mergeReadyPoll    = 250 * time.Millisecond
)

// LandIn asks to land a PR on its base, the default branch or a
// milestone/* branch, at the head the caller expects.
type LandIn struct {
	Target
	HeadSHA string `json:"head_sha"`
}

// LandOut is a PR landed on its base, now or by an earlier call
// (AlreadyMerged).
type LandOut struct {
	Number int64 `json:"number"`
	// Issue is the milestone child the merge closes: set only for a
	// milestone/* base and an issue/<n> head.
	Issue         int64  `json:"issue,omitempty"`
	Base          string `json:"base"`
	HeadSHA       string `json:"head_sha"`
	MergeCommit   string `json:"merge_commit,omitempty"`
	AlreadyMerged bool   `json:"already_merged,omitempty"`
	// IssueClosed says this call closed the child issue; false when it was
	// closed already, or there is none.
	IssueClosed bool `json:"issue_closed,omitempty"`
}

// opLand is the landing merge (design §4 step 6, §9), the only operation
// through which bottown merges a PR: into the default branch or a
// milestone/* branch, as the manager account, fast-forward-only at a head
// the review assessment approves. Fast-forward-only with HeadCommitId is
// Forgejo's compare-and-swap on the base: it lands only when the base's tip
// is an ancestor of that exact head, so the tree that lands is the tree
// that was checked (design §4.1). On a milestone/* base from an issue/<n>
// head it then closes the child issue and comments on it; on the default
// branch Forgejo applies the PR's closing keywords, and nothing is closed
// from a branch name.
func (s *Server) opLand(ctx context.Context, p secrets.Principal, in LandIn) (LandOut, error) {
	deny := func(repo string, err error) (LandOut, error) {
		s.toolAudit(p, repo, "land", "deny", err.Error())
		return LandOut{}, err
	}
	if p.Actor != s.Owner || p.Run != nil {
		return deny("", ferr(http.StatusForbidden, "owner only"))
	}
	org, repo, n, err := s.resolve(p, in.Target, 0)
	if err != nil {
		return LandOut{}, err
	}
	full := org + "/" + repo
	if in.HeadSHA == "" {
		return LandOut{}, ferr(http.StatusBadRequest, "head_sha is required")
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
		return LandOut{}, err
	}
	pr, resp, err := c.GetPullRequest(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return LandOut{}, ferr(http.StatusNotFound, "pull request #%d not found in %s", n, repo)
	} else if err != nil {
		return LandOut{}, err
	}
	if pr.Head == nil || pr.Base == nil {
		return LandOut{}, fmt.Errorf("pull request !%d has no head or base", n)
	}
	base, head := pr.Base.Ref, pr.Head.Sha
	milestone := strings.HasPrefix(base, "milestone/") && base != "milestone/"
	if !milestone {
		r, _, err := c.GetRepo(org, repo)
		if err != nil {
			return LandOut{}, fmt.Errorf("read %s: %w", full, err)
		}
		if base != r.DefaultBranch {
			return deny(full, ferr(http.StatusConflict, "!%d's base is %s, not %s or a milestone/* branch", n, base, r.DefaultBranch))
		}
	}
	out := LandOut{Number: n, Base: base, HeadSHA: head}
	if milestone {
		if n, ok := childIssue(pr.Head.Ref); ok {
			out.Issue = n
		}
	}
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
		return LandOut{}, err
	}
	runs, err := s.runRecords()
	if err != nil {
		return LandOut{}, err
	}
	// Undismiss the approvals Forgejo dismissed automatically whose patch
	// carries over, as the landing step does, so that both the assessment
	// and Forgejo's branch protection count them.
	carries := s.carryOver(ctx, org, repo, pr, rs)
	s.undismissCarried(ctx, p, org, repo, n, head, carries, rs)
	a := assessReviews(s.desired(), s.Owner, runs, repo, pr, rs, carries)
	if a.Decision != review.Approved {
		return deny(full, ferr(http.StatusConflict, "!%d is not approved at %s: %s", n, shortCommit(head), a.Decision))
	}
	// Forgejo recomputes a pull request's mergeability asynchronously after
	// the landing push, so a fast-forward-only merge can be refused with
	// 405 while the new mergeability is not computed yet (the spike's e2e
	// retries it). Retry a bounded number of times.
	var (
		ok     bool
		status int
	)
	for attempt := 0; ; attempt++ {
		ok, resp, err = mc.MergePullRequest(org, repo, n, sdk.MergePullRequestOption{
			Style:        sdk.MergeStyleFastForwardOnly,
			HeadCommitId: head,
		})
		status = 0
		if resp != nil {
			status = resp.StatusCode
		}
		if status == http.StatusMethodNotAllowed && attempt < mergeReadyRetries {
			select {
			case <-ctx.Done():
				return LandOut{}, ctx.Err()
			case <-time.After(mergeReadyPoll):
			}
			continue
		}
		break
	}
	// The SDK reports a refusal as ok false with no error.
	if err == nil && !ok {
		err = fmt.Errorf("forge answered %d", status)
	}
	if err != nil {
		// Forgejo 15 refuses a fast-forward over a moved base with a 500
		// and no message (design §4.1): read the base after the refusal to
		// tell that from any other failure.
		if status == http.StatusConflict || status == http.StatusInternalServerError {
			if tip, moved := s.baseMoved(ctx, org, repo, base, head); moved {
				return deny(full, ferrCode(http.StatusConflict, ErrorCodeBaseMoved,
					"%s moved to %s, which !%d's head %s does not contain: build the landing commit again", base, shortCommit(tip), n, shortCommit(head)))
			}
		}
		if status == http.StatusConflict {
			return deny(full, ferr(http.StatusConflict, "forge refused to merge !%d: %v", n, err))
		}
		s.toolAudit(p, full, "land", "deny", fmt.Sprintf("merge !%d: %v", n, err))
		return LandOut{}, fmt.Errorf("merge !%d: %w", n, err)
	}
	if merged, _, err := c.GetPullRequest(org, repo, n); err == nil {
		out.MergeCommit = deref(merged.MergedCommitID)
	}
	s.toolAudit(p, full, "land", "allow", fmt.Sprintf("!%d at %s into %s as %s", n, shortCommit(head), base, manager))
	if out.IssueClosed, err = s.closeChild(mc, org, repo, out); err != nil {
		return out, err
	}
	return out, nil
}

// baseMoved reads base's tip in the forge's repository and reports whether
// it is no longer an ancestor of head. It reports false when it cannot
// tell, so an unexplained refusal stays an error.
func (s *Server) baseMoved(ctx context.Context, org, repo, base, head string) (string, bool) {
	if s.GitRoot == "" {
		return "", false
	}
	gitDir := filepath.Join(s.GitRoot, strings.ToLower(org), strings.ToLower(repo)+".git")
	tip, err := mergeGit(ctx, gitDir, nil, nil, "rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if err != nil {
		s.Log.Warn("land: read base", "repo", repo, "base", base, "err", err)
		return "", false
	}
	tip = strings.TrimSpace(tip)
	contains, err := gitmerge.IsAncestor(ctx, gitDir, tip, head)
	if err != nil {
		s.Log.Warn("land: base ancestry", "repo", repo, "base", base, "err", err)
		return tip, false
	}
	return tip, !contains
}

// closeChild closes the merged PR's child issue as the manager, because
// Forgejo applies closing keywords only on merges into the default branch,
// and comments on it with what was merged. An issue closed already is left
// alone, so repeated calls comment once; a merge without a child issue
// (out.Issue 0) closes nothing.
func (s *Server) closeChild(mc *forgejo.Client, org, repo string, out LandOut) (bool, error) {
	if out.Issue == 0 {
		return false, nil
	}
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

// childIssue is the issue number of an issue/<n> branch, n positive; any
// other branch, issue/-7 or an overflowing number included, has none.
func childIssue(branch string) (int64, bool) {
	v, ok := strings.CutPrefix(branch, "issue/")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
