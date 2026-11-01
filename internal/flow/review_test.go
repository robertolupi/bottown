package flow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
	"github.com/robertolupi/bottown/internal/server"
)

// TestFlowSharedReviewCases checks the flow's next step for each shared
// review fixture: approved stops ready to merge, a request for changes at
// the head gets a follow-up implement run, an older head's request that
// still blocks an approval stops, a comment stops undecided, and anything
// else gets a verify run.
func TestFlowSharedReviewCases(t *testing.T) {
	for _, tc := range reviewtest.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			api := &fakeAPI{open: true, pr: openPR(), runs: tc.Runs}
			api.pr.Number, api.pr.HeadSHA = reviewtest.PR, reviewtest.Head
			for _, r := range tc.Reviews {
				api.pr.Reviews = append(api.pr.Reviews, server.ReviewOut{ID: r.ID, Reviewer: r.Reviewer, State: r.State, CommitID: r.CommitID, Stale: r.Stale, Dismissed: r.Dismissed, Official: r.Official})
			}
			var calls []string
			runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
				calls = append(calls, req.Kind)
				return errors.New("stop here")
			})
			opts := testOptions()
			opts.Rounds = 5
			result, _ := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), opts)
			wantReason, wantCall := RunFailed, ""
			switch {
			case tc.Want == review.Approved:
				wantReason = ReadyToMerge
			case tc.Want == review.ChangesRequested && tc.WantAtHead:
				wantCall = policy.Implement
			case tc.Want == review.ChangesRequested:
				wantReason = ChangesRequested
			case tc.Want == review.Commented:
				wantReason = VerifierUndecided
			default:
				wantCall = policy.Verify
			}
			if result.Reason != wantReason || strings.Join(calls, ",") != wantCall {
				t.Fatalf("result = %+v, calls = %v; want %s after %q", result, calls, wantReason, wantCall)
			}
		})
	}
}
