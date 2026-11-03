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
			api := &fakeAPI{open: true, pr: openPR(), runs: tc.Runs, carries: tc.Carries}
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

// TestFlowCarriedApprovalNotStale: Forgejo leaves an approval at the old
// head unstale when a push leaves the PR's diff alone. When it carries over,
// the flow stops ready to merge with no verify run instead of waiting for
// the reviews to settle.
func TestFlowCarriedApprovalNotStale(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR(), carries: []review.Carry{{From: reviewtest.Old, Onto: reviewtest.Base}}}
	api.pr.Number, api.pr.HeadSHA = reviewtest.PR, reviewtest.Head
	api.pr.Reviews = []server.ReviewOut{{ID: 1, Reviewer: "agy", State: review.StateApproved, CommitID: reviewtest.Old, Official: true}}
	runner := RunnerFunc(func(context.Context, RunRequest) error {
		t.Fatal("no run expected")
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("result = %+v, err = %v; want ready to merge", result, err)
	}
	if want := "approval at h1 carried over: same patch rebased onto b2"; !strings.Contains(result.Detail, want) {
		t.Fatalf("detail = %q, want it to say %q", result.Detail, want)
	}
}
