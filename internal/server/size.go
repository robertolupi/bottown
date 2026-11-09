package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/estimate"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

// issue_size (docs/reference/sizing.md): an agent's size estimate of an
// issue, recorded as a comment the server renders, with a parseable
// bottown-estimate block, and as the issue's size label when it has none.
// It is the only way an agent sets a label: the proxy denies the labels
// endpoints (internal/policy).

// IssueSizeIn is the input of issue size: the issue (the run's by default)
// and the estimate.
type IssueSizeIn struct {
	Target
	estimate.Input
}

// IssueSizeOut says what an estimate did.
type IssueSizeOut struct {
	Number int64 `json:"number"`
	// Outcome is sized, already_sized or needs_clarification
	// (secrets.Outcome*).
	Outcome string `json:"outcome"`
	// Size is the estimate's size, or needs-clarification.
	Size string `json:"size"`
	// Label is the size label the issue has after the call, if any.
	Label string `json:"label,omitempty"`
	// Applied says the estimate set the issue's label.
	Applied bool  `json:"applied"`
	Comment int64 `json:"comment_id"`
	// Reused says this run had already posted its estimate, which the call
	// kept (adding the label if it was missing) rather than posting another.
	Reused bool   `json:"reused,omitempty"`
	URL    string `json:"url,omitempty"`
}

// sizingRoles are the runs that may size their issue; the owner may size
// any issue outside a run.
var sizingRoles = []string{policy.Implement, policy.Estimate}

func (s *Server) opIssueSize(ctx context.Context, p secrets.Principal, in IssueSizeIn) (IssueSizeOut, error) {
	deny := func(err error) (IssueSizeOut, error) {
		s.toolAudit(p, "", "issue_size", "deny", err.Error())
		return IssueSizeOut{}, err
	}
	if p.Run != nil {
		if err := requireRole(p, sizingRoles...); err != nil {
			return deny(err)
		}
		if in.Number != 0 && in.Number != p.Run.Issue {
			return deny(ferr(http.StatusForbidden, "this run may size only its issue, #%d", p.Run.Issue))
		}
	} else if p.Actor != s.Owner || p.ReadOnly {
		return deny(ferr(http.StatusForbidden, "needs an implement run (start one with `bottown run start implement N`)"))
	}
	in.Normalize()
	if err := in.Validate(); err != nil {
		return IssueSizeOut{}, ferr(http.StatusBadRequest, "%v", err)
	}
	org, repo, n, err := s.resolve(p, in.Target, runDefault(p))
	if err != nil {
		if errStatus(err) == http.StatusForbidden {
			return deny(err)
		}
		return IssueSizeOut{}, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return IssueSizeOut{}, err
	}

	// One estimate is written at a time, so two runs sizing an issue at
	// once read each other's label and apply one size.
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()

	is, resp, err := c.GetIssue(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return IssueSizeOut{}, ferr(http.StatusNotFound, "issue #%d not found in %s", n, repo)
	} else if err != nil {
		return IssueSizeOut{}, err
	}
	if is.PullRequest != nil {
		return IssueSizeOut{}, ferr(http.StatusBadRequest, "#%d is a pull request, not an issue", n)
	}
	existing := sizeLabel(is.Labels)
	out := IssueSizeOut{Number: n, Size: in.Size, Label: existing}

	// A retry finds this run's earlier estimate and keeps it.
	if p.Run != nil {
		cs, err := c.IssueComments(org, repo, n)
		if err != nil {
			return IssueSizeOut{}, err
		}
		for i := len(cs) - 1; i >= 0; i-- {
			rec, ok := estimate.Parse(cs[i].Body)
			if !ok || rec.Run != p.Run.ID || login(cs[i].Poster) != p.Actor {
				continue
			}
			out.Reused, out.Comment, out.Size, out.Applied = true, cs[i].ID, rec.Size, rec.Applied
			out.URL = fmt.Sprintf("%s#issuecomment-%d", is.HTMLURL, cs[i].ID)
			prior := estimate.Input{Size: rec.Size}
			if rec.Applied && existing == "" {
				if err := c.AddIssueLabelNames(org, repo, n, prior.Label()); err != nil {
					return IssueSizeOut{}, fmt.Errorf("estimate posted as comment %d, but adding %s: %w", cs[i].ID, prior.Label(), err)
				}
				if prior.Size != estimate.NeedsClarification {
					out.Label = prior.Label()
				}
			}
			out.Outcome = sizeOutcome(rec.Size, rec.Applied)
			s.toolAudit(p, org+"/"+repo, "issue_size", "allow", fmt.Sprintf("%s#%d: %s (reused comment %d)", repo, n, out.Outcome, cs[i].ID))
			s.recordSizeOutcome(p, out.Outcome)
			return out, nil
		}
	}

	rec := estimate.Record{
		Version: estimate.Version, Guide: runs.SizingGuideVersion, Actor: p.Actor, Role: "owner",
		Size: in.Size, Applied: existing == "", Confidence: in.Confidence, Existing: existing,
	}
	if run := p.Run; run != nil {
		rec.Run, rec.Family, rec.Role, rec.SHA = run.ID, run.Family, run.Kind, run.SHA
	} else {
		rec.Family = s.family(p.Actor)
	}
	owner := s.Owner
	if p.Actor == s.Owner {
		owner = ""
	}
	cm, _, err := c.CreateIssueComment(org, repo, n, sdk.CreateIssueCommentOption{Body: estimate.Comment(in.Input, rec, owner)})
	if err != nil {
		return IssueSizeOut{}, err
	}
	out.Comment, out.Applied, out.URL = cm.ID, rec.Applied, cm.HTMLURL
	if rec.Applied {
		if err := c.AddIssueLabelNames(org, repo, n, in.Label()); err != nil {
			return IssueSizeOut{}, fmt.Errorf("estimate posted as comment %d, but adding %s (call issue size again to retry): %w", cm.ID, in.Label(), err)
		}
		if in.Size != estimate.NeedsClarification {
			out.Label = in.Label()
		}
	}
	out.Outcome = sizeOutcome(in.Size, rec.Applied)
	s.toolAudit(p, org+"/"+repo, "issue_size", "allow", fmt.Sprintf("%s#%d: %s %s", repo, n, out.Outcome, in.Size))
	s.recordSizeOutcome(p, out.Outcome)
	return out, nil
}

// sizeLabel is the issue's size label, or "".
func sizeLabel(ls []*sdk.Label) string {
	for _, l := range ls {
		if l != nil && strings.HasPrefix(l.Name, estimate.SizeLabelPrefix) {
			return l.Name
		}
	}
	return ""
}

func sizeOutcome(size string, applied bool) string {
	switch {
	case !applied:
		return secrets.OutcomeAlreadySized
	case size == estimate.NeedsClarification:
		return secrets.OutcomeNeedsClarification
	default:
		return secrets.OutcomeSized
	}
}

// recordSizeOutcome records an estimate's outcome on its run. An implement
// run's outcome is its pull request, so there the estimate is recorded only
// until one opens, and never replaces it.
func (s *Server) recordSizeOutcome(p secrets.Principal, outcome string) {
	if p.Run == nil {
		return
	}
	if p.Run.Kind == policy.Implement {
		if _, err := s.Store.SetOutcomeIfUnset(p.Run.ID, outcome); err != nil {
			s.Log.Error("record run outcome", "run", p.Run.ID, "outcome", outcome, "err", err)
		}
		return
	}
	s.recordOutcome(p.Run.ID, outcome)
}

// family is actor's model family in the desired state, or "".
func (s *Server) family(actor string) string {
	for _, a := range s.desired().Actors {
		if a.Name == actor {
			return a.Family
		}
	}
	return ""
}
