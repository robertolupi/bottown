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

type reconcilingAPI struct {
	*fakeAPI
	reconciled int
}

func (a *reconcilingAPI) ReconcileCarries(context.Context, string, int64) error {
	a.reconciled++
	for i := range a.pr.Reviews {
		a.pr.Reviews[i].Dismissed = false
	}
	return nil
}

func TestFlowReconcilesDismissedCarriedApproval(t *testing.T) {
	base := &fakeAPI{open: true, pr: openPR(), carries: []review.Carry{{From: reviewtest.Old, Onto: reviewtest.Base}}}
	base.pr.Number, base.pr.HeadSHA = reviewtest.PR, reviewtest.Head
	base.pr.Reviews = []server.ReviewOut{{ID: 1, Reviewer: "agy", State: review.StateApproved, CommitID: reviewtest.Old, Official: true, Dismissed: true}}
	api := &reconcilingAPI{fakeAPI: base}
	runner := RunnerFunc(func(context.Context, RunRequest) error {
		t.Fatal("no run expected")
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge || api.reconciled != 1 {
		t.Fatalf("result = %+v, reconciled = %d, err = %v; want one reconcile and ready to merge", result, api.reconciled, err)
	}
}

// TestFlowCarriedApprovalBesideHeadApproval: an unstale approval that carries
// over is settled even when a head approval decides the assessment, so the
// flow does not wait for Forgejo to mark it stale.
func TestFlowCarriedApprovalBesideHeadApproval(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR(), carries: []review.Carry{{From: reviewtest.Old, Onto: reviewtest.Base}}}
	api.pr.Number, api.pr.HeadSHA = reviewtest.PR, reviewtest.Head
	api.pr.Reviews = []server.ReviewOut{
		{ID: 1, Reviewer: "agy", State: review.StateApproved, CommitID: reviewtest.Old, Official: true},
		{ID: 2, Reviewer: "kimi", State: review.StateApproved, CommitID: reviewtest.Head, Official: true},
	}
	runner := RunnerFunc(func(context.Context, RunRequest) error {
		t.Fatal("no run expected")
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("result = %+v, err = %v; want ready to merge", result, err)
	}
}

// TestReviewsSettled: every carried commit is settled, not only the one
// whose approval decides; an unstale review at an older head that does not
// carry over is still a pending Forgejo update.
func TestReviewsSettled(t *testing.T) {
	approval := func(id int64, reviewer, commit string, stale bool) server.ReviewOut {
		return server.ReviewOut{ID: id, Reviewer: reviewer, State: review.StateApproved, CommitID: commit, Stale: stale, Official: true}
	}
	for _, tc := range []struct {
		name    string
		reviews []server.ReviewOut
		carries []review.Carry
		want    bool
	}{
		{"carried beside a head approval", []server.ReviewOut{approval(1, "agy", "h1", false), approval(2, "kimi", "h2", false)},
			[]review.Carry{{From: "h1", Onto: "b2"}}, true},
		{"several carried commits", []server.ReviewOut{approval(1, "agy", "h0", false), approval(2, "kimi", "h1", false)},
			[]review.Carry{{From: "h0", Onto: "b2"}, {From: "h1", Onto: "b2"}}, true},
		{"one of two older heads carried", []server.ReviewOut{approval(1, "agy", "h0", false), approval(2, "kimi", "h1", false)},
			[]review.Carry{{From: "h1", Onto: "b2"}}, false},
		{"older head not carried, stale", []server.ReviewOut{approval(1, "agy", "h1", true)}, nil, true},
		{"older head not carried, not yet stale", []server.ReviewOut{approval(1, "agy", "h1", false)}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr := server.PROut{HeadSHA: "h2", Reviews: tc.reviews, Carries: tc.carries}
			if got := reviewsSettled(pr); got != tc.want {
				t.Fatalf("reviewsSettled = %v, want %v", got, tc.want)
			}
		})
	}
}
