package webui

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
)

// TestInboxSharedReviewCases checks the inbox for each shared review
// fixture on a mergeable PR with no live runs: approved is ready to merge, a
// request for changes or a comment is a stopped flow (changes requested,
// verifier undecided), and needing a review is nothing for the owner.
func TestInboxSharedReviewCases(t *testing.T) {
	for _, tc := range reviewtest.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			fix := defaultFixture()
			fix.actors = nil
			for name, family := range reviewtest.Families {
				fix.actors = append(fix.actors, map[string]any{"name": name, "family": family})
			}
			fix.whoami = map[string]any{"actor": reviewtest.Owner}
			fix.runs = tc.Runs
			now := time.Now()
			fix.prs = []*sdk.PullRequest{{
				Index: reviewtest.PR, State: sdk.StateOpen, Mergeable: true, Created: &now,
				Poster: &sdk.User{UserName: reviewtest.Author},
				Head:   &sdk.PRBranchInfo{Ref: "issue/9", Sha: reviewtest.Head},
			}}
			for _, r := range tc.Reviews {
				fix.reviews[reviewtest.PR] = append(fix.reviews[reviewtest.PR], &sdk.PullReview{
					ID: r.ID, Reviewer: &sdk.User{UserName: r.Reviewer}, State: sdk.ReviewStateType(r.State),
					CommitID: r.CommitID, Stale: r.Stale, Dismissed: r.Dismissed, Official: r.Official, Submitted: now,
				})
			}
			_, port := newFixtureServer(t, fix)
			s, err := NewServer(Config{
				Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: reviewtest.Owner},
				RepoTop:  t.TempDir(), FullRepo: "bottown/" + reviewtest.Repo,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			items, err := s.ComputeInbox(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var want InboxItemKind
			title := ""
			switch tc.Want {
			case review.Approved:
				want, title = InboxReadyToMerge, "ready to merge"
			case review.ChangesRequested:
				want, title = InboxStoppedFlow, "changes requested"
			case review.Commented:
				want, title = InboxStoppedFlow, "verifier undecided"
			}
			if want == "" {
				if len(items) != 0 {
					t.Fatalf("items = %+v, want none", items)
				}
				return
			}
			if len(items) != 1 || items[0].Kind != want || !strings.Contains(items[0].Title, title) {
				t.Fatalf("items = %+v, want one %s (%s)", items, want, title)
			}
			if want == InboxReadyToMerge && len(tc.Runs) > 0 && !strings.Contains(items[0].Description, "same-family override") {
				t.Fatalf("description %q does not name the override", items[0].Description)
			}
		})
	}
}
