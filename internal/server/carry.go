package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/secrets"
)

// Review carry-over. An official approval at an older head counts at the
// current head when the head only rebased, or merged main into, the same
// patch (review.SamePatch, run on Forgejo's own copy of the repository).
// The assessment counts it (internal/review); for Forgejo's branch
// protection to count it too, carryOver also undismisses it when Forgejo
// dismissed it automatically for the new commits. Forgejo counts an
// official, undismissed approval at any commit, since bottown leaves
// "ignore stale approvals" off. A dismissal with a person's message is
// left alone.

// carryOver returns the older heads of open PR pr whose approvals carry
// over to its head, and undismisses the carried approvals Forgejo
// dismissed by itself, updating rs to match. p is the caller, for the
// audit.
func (s *Server) carryOver(ctx context.Context, p secrets.Principal, org, repo string, pr *sdk.PullRequest, rs []*sdk.PullReview) []review.Carry {
	if s.GitRoot == "" || pr == nil || pr.Head == nil || pr.Base == nil || pr.State != sdk.StateOpen || pr.HasMerged {
		return nil
	}
	head := pr.Head.Sha
	gitDir := filepath.Join(s.GitRoot, strings.ToLower(org), strings.ToLower(repo)+".git")
	var carries []review.Carry
	tried := map[string]bool{}
	for _, r := range rs {
		if r == nil || string(r.State) != review.StateApproved || !r.Official || r.CommitID == "" || r.CommitID == head || tried[r.CommitID] {
			continue
		}
		tried[r.CommitID] = true
		onto, same, err := review.SamePatch(ctx, gitDir, "refs/heads/"+pr.Base.Ref, r.CommitID, head)
		if err != nil {
			s.Log.Debug("review carry-over", "repo", repo, "pr", pr.Index, "from", r.CommitID, "err", err)
			continue
		}
		if same {
			carries = append(carries, review.Carry{From: r.CommitID, Onto: onto})
		}
	}
	if len(carries) > 0 {
		s.undismissCarried(ctx, p, org, repo, pr.Index, head, carries, rs)
	}
	return carries
}

func (s *Server) undismissCarried(ctx context.Context, p secrets.Principal, org, repo string, index int64, head string, carries []review.Carry, rs []*sdk.PullReview) {
	carried := map[string]bool{}
	for _, c := range carries {
		carried[c.From] = true
	}
	var auto map[int64]bool
	var c *forgejo.Client
	for _, r := range rs {
		if r == nil || !r.Dismissed || !r.Official || string(r.State) != review.StateApproved || !carried[r.CommitID] {
			continue
		}
		if auto == nil {
			var err error
			if c, err = s.actorClient(ctx, s.Owner); err != nil {
				s.Log.Warn("review carry-over: no owner client", "err", err)
				return
			}
			if auto, err = c.AutoDismissedReviews(org, repo, index); err != nil {
				s.Log.Warn("review carry-over: read timeline", "repo", repo, "pr", index, "err", err)
				return
			}
		}
		if !auto[r.ID] {
			continue
		}
		if _, err := c.UnDismissPullReview(org, repo, index, r.ID); err != nil {
			s.Log.Warn("review carry-over: undismiss", "repo", repo, "pr", index, "review", r.ID, "err", err)
			continue
		}
		r.Dismissed = false
		s.toolAudit(p, repo, "review_carry_over", "allow", fmt.Sprintf("!%d review %d at %s undismissed: same patch at %s", index, r.ID, shortCommit(r.CommitID), shortCommit(head)))
	}
}

func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
