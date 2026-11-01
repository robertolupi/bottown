package review_test

import (
	"testing"

	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
	"github.com/robertolupi/bottown/internal/secrets"
)

func TestAssess(t *testing.T) {
	for _, tc := range reviewtest.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := review.Assess(reviewtest.Head, tc.Reviews, tc.Policy())
			if got.Decision != tc.Want {
				t.Fatalf("decision = %q, want %q", got.Decision, tc.Want)
			}
			var id int64
			if got.Review != nil {
				id = got.Review.ID
			}
			if id != tc.WantID || got.AtHead(reviewtest.Head) != tc.WantAtHead {
				t.Fatalf("review = %+v (at head %v), want id %d at head %v", got.Review, got.AtHead(reviewtest.Head), tc.WantID, tc.WantAtHead)
			}
		})
	}
}

func TestAssessOrderIndependent(t *testing.T) {
	for _, tc := range reviewtest.Cases {
		rev := make([]review.Review, len(tc.Reviews))
		for i, r := range tc.Reviews {
			rev[len(rev)-1-i] = r
		}
		a := review.Assess(reviewtest.Head, tc.Reviews, tc.Policy())
		b := review.Assess(reviewtest.Head, rev, tc.Policy())
		if a.Decision != b.Decision || (a.Review == nil) != (b.Review == nil) || a.Review != nil && a.Review.ID != b.Review.ID {
			t.Errorf("%s: %+v reversed to %+v", tc.Name, a, b)
		}
	}
}

func TestGrants(t *testing.T) {
	grants := review.Grants([]secrets.Run{
		{Kind: "verify", Actor: "haiku", Repo: "demo", PR: 12, SHA: "h2", AllowSameFamily: true},
		{Kind: "verify", Actor: "agy", Repo: "demo", PR: 12, SHA: "h2"},
		{Kind: "verify", Actor: "haiku", Repo: "other", PR: 12, SHA: "h2", AllowSameFamily: true},
		{Kind: "verify", Actor: "haiku", Repo: "demo", PR: 13, SHA: "h2", AllowSameFamily: true},
		{Kind: "implement", Actor: "haiku", Repo: "demo", PR: 12, SHA: "h2", AllowSameFamily: true},
	}, "demo", 12)
	if len(grants) != 1 || grants[0] != (review.Grant{Reviewer: "haiku", Commit: "h2"}) {
		t.Fatalf("grants = %+v", grants)
	}
}
