package flow

import (
	"bytes"
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
	// gate is the flow's bottown/e2e status per commit, and gated the
	// order it recorded them in.
	gate  map[string]server.CommitStatus
	gated []string
	// merge is the trial merge PRMergeStatus reports; mergeCalls the heads
	// MergeBase was asked to merge into.
	merge      server.PRMergeStatus
	mergeCalls []string
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
	pr.Carries = f.carries
	pr.Checks = nil
	if st, ok := f.gate[pr.HeadSHA]; ok {
		pr.Checks = []server.CommitStatus{st}
	}
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

func (f *fakeAPI) PRMergeStatus(context.Context, string, int64) (server.PRMergeStatus, error) {
	return f.merge, nil
}

func (f *fakeAPI) MergeBase(_ context.Context, _ string, _ int64, head string) (server.PRMergeOut, error) {
	f.mergeCalls = append(f.mergeCalls, head)
	old := f.pr.HeadSHA
	f.pr.HeadSHA = "h2"
	f.carries = append(f.carries, review.Carry{From: old, Onto: "b2"})
	f.merge.Behind = false
	return server.PRMergeOut{HeadSHA: f.pr.HeadSHA, BaseSHA: f.merge.BaseSHA}, nil
}

func (f *fakeAPI) RecordGate(_ context.Context, repo, sha, state, description string) error {
	if repo != "demo" {
		return errors.New("wrong repo")
	}
	if f.gate == nil {
		f.gate = map[string]server.CommitStatus{}
	}
	f.gate[sha] = server.CommitStatus{Context: "bottown/e2e", State: state, Description: description + " (flow)", Creator: "rlupi", Gate: true}
	f.gated = append(f.gated, sha)
	return nil
}

func (f *fakeAPI) GateFailures(context.Context, string, int64) ([]string, error) {
	failed := []string{}
	for sha, st := range f.gate {
		if st.State == "failure" {
			failed = append(failed, sha)
		}
	}
	return failed, nil
}

func testOptions() Options {
	return Options{Issue: 9, Repo: "demo", Implement: "claude", Verify: "agy", Rounds: 3, Timeout: time.Second}
}

func openPR() server.PROut {
	return server.PROut{Number: 12, HeadSHA: "h1", HeadBranch: "issue/9", BaseBranch: "main", Mergeable: true}
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

func TestFlowChangesRequestedWithoutPushFails(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR()}
	api.pr.Reviews = []server.ReviewOut{{ID: 1, Reviewer: "agy", Official: true, State: "REQUEST_CHANGES", CommitID: api.pr.HeadSHA}}
	calls := 0
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		if req.Kind != policy.Implement {
			t.Fatalf("run = %s, want implement", req.Kind)
		}
		calls++
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	var stop *StopError
	if !errors.As(err, &stop) || result.Reason != RunFailed || calls != 1 || !strings.Contains(result.Detail, "implement run ended without pushing a new head") {
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
	if err == nil || !strings.Contains(err.Error(), "r-live") || !strings.Contains(err.Error(), "bottown run stop --revoke r-live") || called {
		t.Fatalf("error = %v, runner called = %v", err, called)
	}
}

func TestFlowMergeBaseCleanApproved(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR(), merge: server.PRMergeStatus{Behind: true, Clean: true, HeadSHA: "h1", BaseSHA: "b2"}}
	approve(api)
	opts := testOptions()
	opts.Attended = true
	var out bytes.Buffer
	runner := RunnerFunc(func(context.Context, RunRequest) error {
		t.Fatal("an unchanged approval should not cost another run")
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner, In: strings.NewReader("y\n"), Out: &out}).Run(context.Background(), opts)
	if err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(api.mergeCalls) != 1 || api.mergeCalls[0] != "h1" {
		t.Fatalf("merge calls = %v, want the offered head", api.mergeCalls)
	}
	if got := out.String(); !strings.Contains(got, "Merge main into issue/9 for !12? [y/N]") || !strings.Contains(got, "merged main into issue/9") {
		t.Fatalf("output = %q", got)
	}
	if len(api.comments) != 1 || !strings.Contains(api.comments[0], "carried over") {
		t.Fatalf("comments = %v, want the approval to carry over", api.comments)
	}
}

func TestFlowMergeBaseDefaultNoAndUnattended(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attended bool
		input    string
	}{
		{name: "default no", attended: true, input: "\n"},
		{name: "unattended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{open: true, pr: openPR(), merge: server.PRMergeStatus{Behind: true, Clean: true, HeadSHA: "h1"}}
			opts := testOptions()
			opts.Attended = tc.attended
			verifies := 0
			runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
				if req.Kind != policy.Verify {
					t.Fatalf("run = %s, want verify", req.Kind)
				}
				verifies++
				approve(api)
				return nil
			})
			var out bytes.Buffer
			result, err := (&Supervisor{API: api, Runner: runner, In: strings.NewReader(tc.input), Out: &out}).Run(context.Background(), opts)
			if err != nil || result.Reason != ReadyToMerge || verifies != 1 || len(api.mergeCalls) != 0 {
				t.Fatalf("result = %+v, err = %v, verifies = %d, merges = %v", result, err, verifies, api.mergeCalls)
			}
			if got, want := strings.Contains(out.String(), "[y/N]"), tc.attended; got != want {
				t.Fatalf("prompt present = %v, want %v; output %q", got, want, out.String())
			}
		})
	}
}

func TestFlowMergeBaseConflictReportsFiles(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR(), merge: server.PRMergeStatus{Behind: true, Clean: false, Conflicts: []string{"a.txt", "b.txt"}}}
	runner := RunnerFunc(func(context.Context, RunRequest) error {
		t.Fatal("a conflict must not start an automatic run")
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	var stop *StopError
	if !errors.As(err, &stop) || result.Reason != ConflictsWithMain {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if !strings.Contains(result.Detail, "a.txt, b.txt") || len(api.mergeCalls) != 0 {
		t.Fatalf("detail = %q, merges = %v", result.Detail, api.mergeCalls)
	}
}
