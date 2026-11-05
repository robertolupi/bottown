package server

import (
	"context"
	"fmt"
	"net/http"
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
// The assessment counts it (internal/review). The flow's reconcile step also
// undismisses it when Forgejo dismissed it automatically for the new commits,
// so Forgejo's branch protection counts it too. Forgejo counts an official,
// undismissed approval at any commit, since bottown leaves "ignore stale
// approvals" off. A dismissal with a person's message is left alone.

type carryKey struct {
	gitDir    string
	base, old string
	head      string
}

type carryResult struct {
	onto string
	same bool
}

func (s *Server) samePatch(ctx context.Context, gitDir, base, old, head string) (string, bool, error) {
	key := carryKey{gitDir: gitDir, base: base, old: old, head: head}
	s.carryMu.Lock()
	result, ok := s.carryCache[key]
	s.carryMu.Unlock()
	if ok {
		return result.onto, result.same, nil
	}
	onto, same, err := review.SamePatch(ctx, gitDir, base, old, head)
	if err != nil {
		return "", false, err
	}
	s.carryMu.Lock()
	if s.carryCache == nil {
		s.carryCache = map[carryKey]carryResult{}
	}
	s.carryCache[key] = carryResult{onto: onto, same: same}
	s.carryMu.Unlock()
	return onto, same, nil
}

// carryOver returns the older heads of open PR pr whose approvals carry over
// to its head. It only reads: undismissCarried is the separate reconcile step.
func (s *Server) carryOver(ctx context.Context, org, repo string, pr *sdk.PullRequest, rs []*sdk.PullReview) []review.Carry {
	if s.GitRoot == "" || pr == nil || pr.Head == nil || pr.Base == nil || pr.State != sdk.StateOpen || pr.HasMerged {
		return nil
	}
	head := pr.Head.Sha
	gitDir := filepath.Join(s.GitRoot, strings.ToLower(org), strings.ToLower(repo)+".git")
	base := "refs/heads/" + pr.Base.Ref
	var carries []review.Carry
	tried := map[string]bool{}
	for _, r := range rs {
		if r == nil || string(r.State) != review.StateApproved || !r.Official || r.CommitID == "" || r.CommitID == head || tried[r.CommitID] {
			continue
		}
		tried[r.CommitID] = true
		onto, same, err := s.samePatch(ctx, gitDir, base, r.CommitID, head)
		if err != nil {
			s.Log.Debug("review carry-over", "repo", repo, "pr", pr.Index, "from", r.CommitID, "err", err)
			continue
		}
		if same {
			carries = append(carries, review.Carry{From: r.CommitID, Onto: onto})
		}
	}
	return carries
}

// opReconcileCarries undismisses automatically dismissed approvals whose
// patches carry over. Unlike the carry-over reads, this owner-only operation
// is allowed to write to Forgejo.
func (s *Server) opReconcileCarries(ctx context.Context, p secrets.Principal, in Target) ([]review.Carry, error) {
	org, repo, n, err := s.resolve(p, in, 0)
	if err != nil {
		return nil, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return nil, err
	}
	pr, resp, err := c.GetPullRequest(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return nil, ferr(http.StatusNotFound, "pull request #%d not found in %s", n, repo)
	} else if err != nil {
		return nil, err
	}
	rs, _, err := c.AllReviews(org, repo, n, sdk.ListPullReviewsOptions{})
	if err != nil {
		return nil, err
	}
	carries := s.carryOver(ctx, org, repo, pr, rs)
	head := ""
	if pr.Head != nil {
		head = pr.Head.Sha
	}
	s.undismissCarried(ctx, p, org, repo, n, head, carries, rs)
	if carries == nil {
		carries = []review.Carry{}
	}
	return carries, nil
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
