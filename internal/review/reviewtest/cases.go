// Package reviewtest holds the review fixtures shared by the flow, PR list
// and inbox tests, so every surface is checked against the same decisions.
package reviewtest

import (
	"time"

	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/secrets"
)

// The fixtures' world: claude (anthropic) wrote the PR; haiku is another
// anthropic actor; agy and kimi are other families; rlupi is the owner.
const (
	Repo   = "demo"
	PR     = 12
	Author = "claude"
	Owner  = "rlupi"
	Head   = "h2"
	Old    = "h1"
	// Base is the head's merge base when Old is carried over.
	Base = "b2"
)

// Families are the fixture actors' model families.
var Families = map[string]string{
	"claude": "anthropic",
	"haiku":  "anthropic",
	"agy":    "google",
	"kimi":   "moonshot",
}

// Case is one review history and the decision it must produce.
type Case struct {
	Name    string
	Reviews []review.Review
	// Runs are server run records for the PR (owner overrides).
	Runs []secrets.Run
	// Carries are older heads with the head's patch (SamePatch).
	Carries []review.Carry
	Want    review.Decision
	// WantID is the deciding review's ID; 0 for NeedsReview.
	WantID int64
	// WantAtHead reports whether the deciding review is at the head.
	WantAtHead bool
}

// Policy is the fixtures' policy for c.
func (c Case) Policy() review.Policy {
	return review.Policy{Author: Author, Owner: Owner, Families: Families, Grants: review.Grants(c.Runs, Repo, PR), Carries: c.Carries}
}

func rv(id int64, reviewer, state, commit string, official bool) review.Review {
	return review.Review{ID: id, Reviewer: reviewer, State: state, CommitID: commit, Official: official, Stale: commit != Head}
}

func dismissed(r review.Review) review.Review { r.Dismissed = true; return r }

// sameFamily is an ended verify run the owner allowed to be same-family.
func sameFamily(actor, sha string) secrets.Run {
	created := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ended := created.Add(time.Hour)
	return secrets.Run{
		ID: "r-" + actor + "-" + sha, Kind: "verify", Actor: actor, Family: Families[actor], Repo: Repo, PR: PR, SHA: sha,
		Author: Author, AllowSameFamily: true, Created: created, Expires: ended, Ended: &ended, Status: "succeeded",
	}
}

// rebased carries Old over to Head: the same patch, rebased onto Base.
var rebased = []review.Carry{{From: Old, Onto: Base}}

const (
	approved = review.StateApproved
	changes  = review.StateRequestChanges
	comment  = review.StateComment
)

// Cases are the shared review histories. Forgejo keeps only each
// reviewer's latest APPROVED or REQUEST_CHANGES official, and never marks a
// COMMENT official; the fixtures follow it.
var Cases = []Case{
	{Name: "no reviews", Want: review.NeedsReview},
	{Name: "cross-family approval", Reviews: []review.Review{rv(1, "agy", approved, Head, true)}, Want: review.Approved, WantID: 1, WantAtHead: true},
	{Name: "approval then another reviewer requests changes",
		Reviews: []review.Review{rv(1, "agy", approved, Head, true), rv(2, "kimi", changes, Head, true)},
		Want:    review.ChangesRequested, WantID: 2, WantAtHead: true},
	{Name: "request changes then another reviewer approves",
		Reviews: []review.Review{rv(1, "kimi", changes, Head, true), rv(2, "agy", approved, Head, true)},
		Want:    review.ChangesRequested, WantID: 1, WantAtHead: true},
	{Name: "same reviewer approves then requests changes",
		Reviews: []review.Review{rv(1, "agy", approved, Head, false), rv(2, "agy", changes, Head, true)},
		Want:    review.ChangesRequested, WantID: 2, WantAtHead: true},
	{Name: "same reviewer requests changes then approves",
		Reviews: []review.Review{rv(1, "agy", changes, Head, false), rv(2, "agy", approved, Head, true)},
		Want:    review.Approved, WantID: 2, WantAtHead: true},
	{Name: "dismissed request for changes",
		Reviews: []review.Review{dismissed(rv(1, "kimi", changes, Head, true)), rv(2, "agy", approved, Head, true)},
		Want:    review.Approved, WantID: 2, WantAtHead: true},
	{Name: "dismissed approval",
		Reviews: []review.Review{dismissed(rv(1, "agy", approved, Head, true))},
		Want:    review.NeedsReview},
	{Name: "stale approval at an old head",
		Reviews: []review.Review{rv(1, "agy", approved, Old, true)},
		Want:    review.NeedsReview},
	{Name: "stale request for changes awaits a new verdict",
		Reviews: []review.Review{rv(1, "agy", changes, Old, true)},
		Want:    review.NeedsReview},
	{Name: "stale request for changes by another reviewer holds back an approval",
		Reviews: []review.Review{rv(1, "kimi", changes, Old, true), rv(2, "agy", approved, Head, true)},
		Want:    review.ChangesRequested, WantID: 1, WantAtHead: false},
	{Name: "reviewer approves the new head after requesting changes",
		Reviews: []review.Review{rv(1, "agy", changes, Old, false), rv(2, "agy", approved, Head, true)},
		Want:    review.Approved, WantID: 2, WantAtHead: true},
	{Name: "comment verdict",
		Reviews: []review.Review{rv(1, "agy", comment, Head, false)},
		Want:    review.Commented, WantID: 1, WantAtHead: true},
	{Name: "comment after an approval",
		Reviews: []review.Review{rv(1, "agy", approved, Head, true), rv(2, "kimi", comment, Head, false)},
		Want:    review.Approved, WantID: 1, WantAtHead: true},
	{Name: "comment at an old head",
		Reviews: []review.Review{rv(1, "agy", comment, Old, false)},
		Want:    review.NeedsReview},
	{Name: "owner comment is discussion",
		Reviews: []review.Review{rv(1, Owner, comment, Head, false)},
		Want:    review.NeedsReview},
	{Name: "same-family approval without override",
		Reviews: []review.Review{rv(1, "haiku", approved, Head, true)},
		Want:    review.NeedsReview},
	{Name: "owner-authorized same-family approval",
		Reviews: []review.Review{rv(1, "haiku", approved, Head, true)},
		Runs:    []secrets.Run{sameFamily("haiku", Head)},
		Want:    review.Approved, WantID: 1, WantAtHead: true},
	{Name: "same-family override for an older head",
		Reviews: []review.Review{rv(1, "haiku", approved, Head, true)},
		Runs:    []secrets.Run{sameFamily("haiku", Old)},
		Want:    review.NeedsReview},
	{Name: "owner approval",
		Reviews: []review.Review{rv(1, Owner, approved, Head, true)},
		Want:    review.Approved, WantID: 1, WantAtHead: true},
	{Name: "non-official approval",
		Reviews: []review.Review{rv(1, "agy", approved, Head, false)},
		Want:    review.NeedsReview},
	{Name: "approval by a non-actor",
		Reviews: []review.Review{rv(1, "manager", approved, Head, true)},
		Want:    review.NeedsReview},
	{Name: "approval carried over from a rebased head",
		Reviews: []review.Review{rv(1, "agy", approved, Old, true)}, Carries: rebased,
		Want: review.Approved, WantID: 1, WantAtHead: false},
	{Name: "approval at an old head whose patch changed",
		Reviews: []review.Review{rv(1, "agy", approved, Old, true)}, Carries: []review.Carry{{From: "h0", Onto: Base}},
		Want: review.NeedsReview},
	{Name: "dismissed approval is not carried over",
		Reviews: []review.Review{dismissed(rv(1, "agy", approved, Old, true))}, Carries: rebased,
		Want: review.NeedsReview},
	{Name: "non-official approval is not carried over",
		Reviews: []review.Review{rv(1, "agy", approved, Old, false)}, Carries: rebased,
		Want: review.NeedsReview},
	{Name: "same-family approval is not carried over without override",
		Reviews: []review.Review{rv(1, "haiku", approved, Old, true)}, Carries: rebased,
		Want: review.NeedsReview},
	{Name: "owner-authorized same-family approval carried over",
		Reviews: []review.Review{rv(1, "haiku", approved, Old, true)}, Carries: rebased,
		Runs: []secrets.Run{sameFamily("haiku", Old)},
		Want: review.Approved, WantID: 1, WantAtHead: false},
	{Name: "approval at the head wins over a carried one",
		Reviews: []review.Review{rv(1, "agy", approved, Old, true), rv(2, "kimi", approved, Head, true)}, Carries: rebased,
		Want: review.Approved, WantID: 2, WantAtHead: true},
	{Name: "stale request for changes holds back a carried approval",
		Reviews: []review.Review{rv(1, "kimi", changes, "h0", true), rv(2, "agy", approved, Old, true)}, Carries: rebased,
		Want: review.ChangesRequested, WantID: 1, WantAtHead: false},
}
