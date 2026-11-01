package server

import (
	"testing"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
	"github.com/robertolupi/bottown/internal/secrets"
)

// TestAssessReviewsSharedCases runs the shared review fixtures through the
// conversion from forge reviews that pr show and pr list use; the PR list's
// review state is the assessment's decision.
func TestAssessReviewsSharedCases(t *testing.T) {
	d := config.Desired{}
	for name, family := range reviewtest.Families {
		d.Actors = append(d.Actors, config.Actor{Name: name, Family: family})
	}
	for _, tc := range reviewtest.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			pr := &sdk.PullRequest{Index: reviewtest.PR, Poster: &sdk.User{UserName: reviewtest.Author}, Head: &sdk.PRBranchInfo{Sha: reviewtest.Head}}
			var rs []*sdk.PullReview
			for _, r := range tc.Reviews {
				rs = append(rs, &sdk.PullReview{
					ID: r.ID, Reviewer: &sdk.User{UserName: r.Reviewer}, State: sdk.ReviewStateType(r.State),
					CommitID: r.CommitID, Stale: r.Stale, Dismissed: r.Dismissed, Official: r.Official,
				})
			}
			// Another PR's override must not count for this one.
			other := secrets.Run{Kind: "verify", Actor: "haiku", Repo: reviewtest.Repo, PR: reviewtest.PR + 1, SHA: reviewtest.Head, AllowSameFamily: true}
			got := assessReviews(d, reviewtest.Owner, append([]secrets.Run{other}, tc.Runs...), reviewtest.Repo, pr, rs)
			if got.Decision != tc.Want {
				t.Fatalf("decision = %q, want %q", got.Decision, tc.Want)
			}
			if tc.WantID != 0 && (got.Review == nil || got.Review.ID != tc.WantID) {
				t.Fatalf("review = %+v, want id %d", got.Review, tc.WantID)
			}
		})
	}
}
