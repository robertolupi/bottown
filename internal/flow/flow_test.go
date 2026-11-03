package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

type fakeAPI struct {
	pr       server.PROut
	open     bool
	runs     []secrets.Run
	carries  []review.Carry
	comments []string
}

func (f *fakeAPI) Actors(context.Context) ([]server.ActorInfo, error) {
	return []server.ActorInfo{{Name: "claude", Family: "anthropic"}, {Name: "agy", Family: "google"}}, nil
}

func (f *fakeAPI) OpenPR(context.Context, string, string) (server.PRItem, bool, error) {
	if !f.open {
		return server.PRItem{}, false, nil
	}
	return server.PRItem{Number: f.pr.Number, HeadBranch: "issue/9"}, true, nil
}

// PR assesses the reviews the way the server does, with the shared
// fixtures' families: claude (anthropic) is the author.
func (f *fakeAPI) PR(context.Context, string, int64) (server.PROut, error) {
	pr := f.pr
	reviews := make([]review.Review, 0, len(pr.Reviews))
	for _, r := range pr.Reviews {
		reviews = append(reviews, review.Review{ID: r.ID, Reviewer: r.Reviewer, State: r.State, CommitID: r.CommitID, Stale: r.Stale, Dismissed: r.Dismissed, Official: r.Official})
	}
	pr.Assessment = review.Assess(pr.HeadSHA, reviews, review.Policy{
		Author: reviewtest.Author, Owner: reviewtest.Owner, Families: reviewtest.Families, Grants: review.Grants(f.runs, "demo", pr.Number), Carries: f.carries,
	})
	return pr, nil
}

func (f *fakeAPI) LiveRuns(context.Context, string) ([]secrets.Run, error) {
	return f.runs, nil
}

func (f *fakeAPI) Comment(_ context.Context, repo string, _ int64, body string) error {
	if repo != "demo" {
		return errors.New("wrong repo")
	}
	f.comments = append(f.comments, body)
	return nil
}

func testOptions() Options {
	return Options{Issue: 9, Repo: "demo", Implement: "claude", Verify: "agy", Rounds: 3, Timeout: time.Second}
}

func openPR() server.PROut {
	return server.PROut{Number: 12, HeadSHA: "h1", Mergeable: true}
}

func approve(api *fakeAPI) {
	api.pr.Reviews = append(api.pr.Reviews, server.ReviewOut{ID: int64(len(api.pr.Reviews) + 1), Reviewer: "agy", Official: true, State: "APPROVED", CommitID: api.pr.HeadSHA})
}

func TestFlowApproveFirstTime(t *testing.T) {
	api := &fakeAPI{}
	var calls []string
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		calls = append(calls, req.Kind)
		if req.Kind == policy.Implement {
			api.open, api.pr = true, openPR()
		} else {
			approve(api)
		}
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge || result.PR != 12 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if strings.Join(calls, ",") != "implement,verify" || len(api.comments) != 1 || !strings.Contains(api.comments[0], "ready to merge") {
		t.Fatalf("calls = %v, comments = %v", calls, api.comments)
	}
}

func TestFlowFollowUpAndOutOfRounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rounds     int
		approve    bool
		wantReason StopReason
		wantCalls  int
	}{
		{name: "follow-up", rounds: 3, approve: true, wantReason: ReadyToMerge, wantCalls: 4},
		{name: "out of rounds", rounds: 2, wantReason: OutOfRounds, wantCalls: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			opts := testOptions()
			opts.Rounds = tc.rounds
			implements, verifies := 0, 0
			runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
				if req.Kind == policy.Implement {
					implements++
					// A push makes earlier reviews stale; Forgejo does not
					// dismiss requests for changes, which stay official.
					reviews := api.pr.Reviews
					for i := range reviews {
						reviews[i].Stale = true
					}
					api.open, api.pr = true, openPR()
					api.pr.HeadSHA = string(rune('a' + implements))
					api.pr.Reviews = reviews
					return nil
				}
				verifies++
				// The verifier's new verdict replaces its earlier one as official.
				for i := range api.pr.Reviews {
					api.pr.Reviews[i].Official = false
				}
				if tc.approve && verifies == 2 {
					approve(api)
					return nil
				}
				api.pr.Reviews = append(api.pr.Reviews, server.ReviewOut{ID: int64(len(api.pr.Reviews) + 1), Reviewer: "agy", Official: true, State: "REQUEST_CHANGES", CommitID: api.pr.HeadSHA})
				return nil
			})
			result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), opts)
			if result.Reason != tc.wantReason || implements+verifies != tc.wantCalls {
				t.Fatalf("result = %+v, error = %v, calls = %d/%d", result, err, implements, verifies)
			}
			if tc.wantReason == ReadyToMerge && err != nil {
				t.Fatal(err)
			}
			var stop *StopError
			if tc.wantReason != ReadyToMerge && (!errors.As(err, &stop) || stop.Result.Reason != tc.wantReason) {
				t.Fatalf("stop error = %v", err)
			}
		})
	}
}

type delayedReviewSettlementAPI struct {
	*fakeAPI
	reads int
}

func (f *delayedReviewSettlementAPI) PR(ctx context.Context, repo string, number int64) (server.PROut, error) {
	if f.pr.HeadSHA == "h2" {
		f.reads++
		if f.reads == 3 {
			for i := range f.pr.Reviews {
				f.pr.Reviews[i].Stale = f.pr.Reviews[i].CommitID != f.pr.HeadSHA
			}
		}
	}
	return f.fakeAPI.PR(ctx, repo, number)
}

func TestFlowWaitsForPushBeforeVerify(t *testing.T) {
	base := &fakeAPI{}
	api := &delayedReviewSettlementAPI{fakeAPI: base}
	implements, verifies := 0, 0
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		if req.Kind == policy.Implement {
			implements++
			reviews := base.pr.Reviews
			base.open, base.pr = true, openPR()
			base.pr.HeadSHA = []string{"", "h1", "h2"}[implements]
			base.pr.Reviews = reviews
			return nil
		}
		verifies++
		for _, r := range base.pr.Reviews {
			if r.CommitID != base.pr.HeadSHA && !r.Stale {
				t.Errorf("verify started before Forgejo settled the push: %+v", r)
			}
		}
		for i := range base.pr.Reviews {
			base.pr.Reviews[i].Official = false
		}
		if verifies == 2 {
			approve(base)
			return nil
		}
		base.pr.Reviews = append(base.pr.Reviews, server.ReviewOut{ID: 1, Reviewer: "agy", Official: true, State: "REQUEST_CHANGES", CommitID: base.pr.HeadSHA})
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge || implements != 2 || verifies != 2 {
		t.Fatalf("result = %+v, error = %v, calls = %d/%d", result, err, implements, verifies)
	}
}

func TestFlowResumeStartsAtVerify(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR()}
	var calls []string
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		calls = append(calls, req.Kind)
		approve(api)
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge || strings.Join(calls, ",") != "verify" {
		t.Fatalf("result = %+v, error = %v, calls = %v", result, err, calls)
	}
}

func TestFlowVerifierUndecided(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR()}
	api.pr.Reviews = []server.ReviewOut{{ID: 1, Reviewer: "agy", State: "COMMENT", CommitID: api.pr.HeadSHA}}
	called := false
	runner := RunnerFunc(func(context.Context, RunRequest) error { called = true; return nil })
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	var stop *StopError
	if !errors.As(err, &stop) || result.Reason != VerifierUndecided || called {
		t.Fatalf("result = %+v, error = %v, runner called = %v", result, err, called)
	}
}

func TestFlowMovedHeadGetsAnotherVerify(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR()}
	verifies := 0
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		if req.Kind != policy.Verify {
			t.Fatalf("unexpected run %s", req.Kind)
		}
		verifies++
		if verifies == 1 {
			api.pr.HeadSHA = "h2"
		} else {
			approve(api)
		}
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge || verifies != 2 {
		t.Fatalf("result = %+v, error = %v, verifies = %d", result, err, verifies)
	}
}

func TestFlowActorLimitPolicy(t *testing.T) {
	t.Run("bypass by default", func(t *testing.T) {
		api := &fakeAPI{}
		var calls []RunRequest
		runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
			calls = append(calls, req)
			if req.Kind == policy.Implement {
				api.open, api.pr = true, openPR()
			} else {
				approve(api)
			}
			return nil
		})
		result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
		if err != nil || result.Reason != ReadyToMerge || len(calls) != 2 {
			t.Fatalf("result = %+v, error = %v, calls = %v", result, err, calls)
		}
		for _, req := range calls {
			if !req.BypassActorLimit {
				t.Errorf("%s run did not bypass the actor limit", req.Kind)
			}
		}
	})

	t.Run("wait when requested", func(t *testing.T) {
		api := &fakeAPI{}
		attempts := 0
		runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
			if req.BypassActorLimit {
				t.Error("waiting flow bypassed the actor limit")
			}
			if req.Kind == policy.Implement {
				attempts++
				if attempts == 1 {
					return ErrActorLimit
				}
				api.open, api.pr = true, openPR()
				return nil
			}
			approve(api)
			return nil
		})
		opts := testOptions()
		opts.WaitActors = true
		result, err := (&Supervisor{API: api, Runner: runner, RetryDelay: time.Millisecond}).Run(context.Background(), opts)
		if err != nil || result.Reason != ReadyToMerge || attempts != 2 {
			t.Fatalf("result = %+v, error = %v, implement attempts = %d", result, err, attempts)
		}
	})
}

func TestFlowImplementWithoutPRFails(t *testing.T) {
	api := &fakeAPI{}
	calls := 0
	runner := RunnerFunc(func(context.Context, RunRequest) error { calls++; return nil })
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	var stop *StopError
	if !errors.As(err, &stop) || result.Reason != RunFailed || calls != 1 || !strings.Contains(result.Detail, "without opening a PR") {
		t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
	}
}

func TestFlowRunFailureAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runner  Runner
		timeout time.Duration
		want    string
	}{
		{name: "failed", runner: RunnerFunc(func(context.Context, RunRequest) error { return errors.New("harness exited 3") }), timeout: time.Second, want: "harness exited 3"},
		{name: "timeout", runner: RunnerFunc(func(ctx context.Context, _ RunRequest) error { <-ctx.Done(); return ctx.Err() }), timeout: 5 * time.Millisecond, want: "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			opts := testOptions()
			opts.Timeout = tc.timeout
			result, err := (&Supervisor{API: api, Runner: tc.runner}).Run(context.Background(), opts)
			var stop *StopError
			if !errors.As(err, &stop) || result.Reason != RunFailed || !strings.Contains(result.Detail, tc.want) {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if len(api.comments) != 1 || !strings.Contains(api.comments[0], "run failed") {
				t.Fatalf("comments = %v", api.comments)
			}
		})
	}
}

func TestFlowRejectsSameFamilyBeforeRunning(t *testing.T) {
	called := false
	runner := RunnerFunc(func(context.Context, RunRequest) error { called = true; return nil })
	opts := testOptions()
	opts.Verify = "claude"
	_, err := (&Supervisor{API: &fakeAPI{}, Runner: runner}).Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "cross-family") || called {
		t.Fatalf("error = %v, runner called = %v", err, called)
	}
}

// ACTOR:SPEC picks a model: the family check reads the actor, and the
// runner gets the spec as given.
func TestFlowActorSpecs(t *testing.T) {
	opts := testOptions()
	opts.Implement, opts.Verify = "claude:opus", "agy:gemini-3.8-flash"
	api := &fakeAPI{}
	var got []string
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		got = append(got, req.Kind+" "+req.Actor)
		if req.Kind == policy.Implement {
			api.open, api.pr = true, openPR()
		} else {
			approve(api)
		}
		return nil
	})
	if _, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if want := "implement claude:opus,verify agy:gemini-3.8-flash"; strings.Join(got, ",") != want {
		t.Fatalf("runs %v, want %s", got, want)
	}

	// A model does not change the actor's family.
	opts.Verify = "claude:sonnet"
	if _, err := (&Supervisor{API: &fakeAPI{}, Runner: runner}).Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "cross-family") {
		t.Fatalf("same family through specs: %v", err)
	}
	opts.Verify = "agy:"
	if _, err := (&Supervisor{API: &fakeAPI{}, Runner: runner}).Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "verifier") {
		t.Fatalf("empty spec: %v", err)
	}
}

func TestFlowRefusesLiveRun(t *testing.T) {
	now := time.Now()
	api := &fakeAPI{runs: []secrets.Run{{ID: "r-live", Issue: 9, Created: now, Expires: now.Add(time.Hour)}}}
	called := false
	runner := RunnerFunc(func(context.Context, RunRequest) error { called = true; return nil })
	_, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err == nil || !strings.Contains(err.Error(), "r-live") || called {
		t.Fatalf("error = %v, runner called = %v", err, called)
	}
}
