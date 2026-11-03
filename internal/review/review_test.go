package review_test

import (
	"context"
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

func TestSamePatch(t *testing.T) {
	for _, tc := range reviewtest.PatchCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			onto, same, err := review.SamePatch(context.Background(), tc.GitDir, tc.BaseRef, tc.Old, tc.Head)
			if err != nil {
				t.Fatal(err)
			}
			if same != tc.Same {
				t.Fatalf("same = %v, want %v", same, tc.Same)
			}
			if onto == "" {
				t.Fatal("no merge base")
			}
		})
	}
	if _, _, err := review.SamePatch(context.Background(), t.TempDir(), "refs/heads/main", "--output=x", "abcdef0"); err == nil {
		t.Fatal("accepted an option as a commit")
	}
}

func TestCarriedReason(t *testing.T) {
	a := review.Assess("e50b14a99", []review.Review{{ID: 1, Reviewer: "agy", State: review.StateApproved, CommitID: "7d27b2e11", Official: true, Stale: true}},
		review.Policy{Author: "claude", Families: reviewtest.Families, Carries: []review.Carry{{From: "7d27b2e11", Onto: "e50b14a00"}}})
	if want := "approval at 7d27b2e carried over: same patch rebased onto e50b14a"; a.Decision != review.Approved || a.Reason != want {
		t.Fatalf("assessment = %+v, want approved with reason %q", a, want)
	}
}
