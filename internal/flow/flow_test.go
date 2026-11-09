package flow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
	"github.com/robertolupi/bottown/internal/runs"
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
	// merge is the trial merge PRMergeStatus reports.
	merge server.PRMergeStatus
	// issueState is what Issue reports; empty is open.
	issueState string
}

func (f *fakeAPI) Issue(_ context.Context, _ string, number int64) (server.IssueOut, error) {
	state := f.issueState
	if state == "" {
		state = "open"
	}
	return server.IssueOut{Number: number, State: state}, nil
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

func (f *fakeAPI) ReconcileCarries(context.Context, string, int64) error {
	return nil
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

func (f *fakeAPI) RecordGate(_ context.Context, repo, sha, check, state, description string) error {
	if repo != "demo" {
		return errors.New("wrong repo")
	}
	if f.gate == nil {
		f.gate = map[string]server.CommitStatus{}
	}
	f.gate[sha] = server.CommitStatus{Context: "bottown/" + check, State: state, Description: description + " (flow)", Creator: "rlupi", Gate: true}
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
			var rounds []string
			runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
				rounds = append(rounds, fmt.Sprintf("%s %d/%d", req.Kind, req.Round, req.Rounds))
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
			// A run's round is one more than the requests for changes so far.
			if want := fmt.Sprintf("implement 1/%[1]d,verify 1/%[1]d,implement 2/%[1]d,verify 2/%[1]d", tc.rounds); strings.Join(rounds, ",") != want {
				t.Fatalf("rounds = %v, want %s", rounds, want)
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

func TestFlowSerializeActors(t *testing.T) {
	t.Run("concurrent by default", func(t *testing.T) {
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
			if req.SerializeActor {
				t.Errorf("%s run serialised its actor", req.Kind)
			}
		}
	})

	t.Run("wait when requested", func(t *testing.T) {
		api := &fakeAPI{}
		attempts := 0
		runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
			if !req.SerializeActor {
				t.Error("serialising flow did not ask to serialise its actor")
			}
			if req.Kind == policy.Implement {
				attempts++
				if req.Retry != (attempts > 1) {
					t.Errorf("implement attempt %d: Retry = %t", attempts, req.Retry)
				}
				if attempts <= 3 {
					return fmt.Errorf("%w: wording the flow does not read", ErrActorBusy)
				}
				api.open, api.pr = true, openPR()
				return nil
			}
			if req.Retry {
				t.Error("the verify run's first try is marked as a retry")
			}
			approve(api)
			return nil
		})
		opts := testOptions()
		opts.SerializeActors, opts.ActorWait = true, time.Minute
		result, err := (&Supervisor{API: api, Runner: runner, RetryDelay: time.Millisecond}).Run(context.Background(), opts)
		if err != nil || result.Reason != ReadyToMerge || attempts != 4 {
			t.Fatalf("result = %+v, error = %v, implement attempts = %d", result, err, attempts)
		}
	})

	t.Run("back off to a minute, and stop when the wait runs out", func(t *testing.T) {
		api := &fakeAPI{}
		clock := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
		var waits []time.Duration
		fired := make(chan time.Time)
		close(fired)
		attempts := 0
		runner := RunnerFunc(func(context.Context, RunRequest) error {
			attempts++
			return fmt.Errorf("%w: actor claude already has a live batch run: r-holder", ErrActorBusy)
		})
		s := &Supervisor{API: api, Runner: runner,
			now:   func() time.Time { return clock },
			after: func(d time.Duration) <-chan time.Time { waits = append(waits, d); clock = clock.Add(d); return fired }}
		opts := testOptions()
		opts.SerializeActors, opts.ActorWait = true, 4*time.Minute
		result, err := s.Run(context.Background(), opts)
		var stop *StopError
		if !errors.As(err, &stop) || result.Reason != ActorBusy || !strings.Contains(result.Detail, "r-holder") || !strings.Contains(result.Detail, "4m0s") {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		sec := time.Second
		want := []time.Duration{sec, 2 * sec, 4 * sec, 8 * sec, 16 * sec, 32 * sec, 60 * sec, 60 * sec, 57 * sec}
		if fmt.Sprint(waits) != fmt.Sprint(want) || attempts != len(want)+1 {
			t.Fatalf("waits = %v, want %v; attempts = %d", waits, want, attempts)
		}
		if len(api.comments) != 1 || !strings.Contains(api.comments[0], "actor busy") || !strings.Contains(api.comments[0], "r-holder") {
			t.Fatalf("comments = %q", api.comments)
		}
	})

	t.Run("a serialising flow needs a wait", func(t *testing.T) {
		opts := testOptions()
		opts.SerializeActors = true
		_, err := (&Supervisor{API: &fakeAPI{}, Runner: RunnerFunc(func(context.Context, RunRequest) error { return nil })}).Run(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "wait") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("a busy actor fails a flow that does not serialise", func(t *testing.T) {
		api := &fakeAPI{}
		attempts := 0
		runner := RunnerFunc(func(context.Context, RunRequest) error { attempts++; return ErrActorBusy })
		_, err := (&Supervisor{API: api, Runner: runner, RetryDelay: time.Millisecond}).Run(context.Background(), testOptions())
		var stop *StopError
		if !errors.As(err, &stop) || stop.Result.Reason != RunFailed || attempts != 1 {
			t.Fatalf("error = %v, attempts = %d", err, attempts)
		}
	})
}

// Two flows with the same implementer on different issues run their
// implement runs at the same time.
func TestFlowsShareActor(t *testing.T) {
	started := make(chan int64, 2)
	release := make(chan struct{})
	flowFor := func(issue int64) (Result, error) {
		api := &fakeAPI{}
		runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
			if req.Kind == policy.Implement {
				started <- req.Issue
				<-release
				api.open, api.pr = true, openPR()
				return nil
			}
			approve(api)
			return nil
		})
		opts := testOptions()
		opts.Issue = issue
		return (&Supervisor{API: api, Runner: runner}).Run(context.Background(), opts)
	}
	errs := make(chan error, 2)
	for _, issue := range []int64{1, 2} {
		go func() {
			result, err := flowFor(issue)
			if err == nil && result.Reason != ReadyToMerge {
				err = fmt.Errorf("flow #%d: %s", issue, result.Reason)
			}
			errs <- err
		}()
	}
	// Each implement run blocks until release, so both being started at
	// once is the overlap itself; the deadline only bounds a hang, and is
	// generous so a loaded host cannot trip it.
	deadline := time.After(30 * time.Second)
	for range 2 {
		select {
		case <-started:
		case <-deadline:
			t.Fatal("the implement runs did not start concurrently")
		}
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
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

// TestFlowRunWithoutToolCalls: a run whose agent made no tool call stops
// the flow as agent did nothing, whether it exited 0 or not, naming the
// actor and its model; a failed run after tool calls stays run failed.
func TestFlowRunWithoutToolCalls(t *testing.T) {
	const line = "error: failed to run prompt: provider.api_error: 429 You've reached your 5-hour usage limit."
	const hint = "rerun the flow with another `--implement`/`--verify`"
	for _, tc := range []struct {
		name, status, actor string
		toolCalls           bool
		want                StopReason
		wants               []string
	}{
		{"exit 0 before any tool call", runs.StatusSucceeded, "claude", false, AgentDidNothing,
			[]string{"the implement run r-1 by claude succeeded without a tool call", hint}},
		{"failed before any tool call", runs.StatusFailed, "agy:gemini-3.8-flash", false, AgentDidNothing,
			[]string{"the implement run r-1 by agy (model gemini-3.8-flash) failed without a tool call", hint}},
		{"failed after tool calls", runs.StatusFailed, "claude", true, RunFailed, []string{"run r-1 failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			var actor string
			runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
				actor = req.Actor
				return &runs.FailedRunError{ID: "r-1", Status: tc.status, Log: "/runs/r-1.log", LastError: line, ToolCalls: tc.toolCalls}
			})
			opts := testOptions()
			opts.Implement = tc.actor
			if strings.HasPrefix(tc.actor, "agy") {
				opts.Verify = "claude"
			}
			result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), opts)
			var stop *StopError
			if !errors.As(err, &stop) || result.Reason != tc.want || len(api.comments) != 1 || actor != tc.actor {
				t.Fatalf("result = %+v, error = %v, comments = %q, actor = %q", result, err, api.comments, actor)
			}
			marker := `<!-- bottown:flow-stop v=1 reason="` + string(tc.want) + `" -->`
			if tc.want == AgentDidNothing {
				name, _, _ := strings.Cut(tc.actor, ":")
				marker = `<!-- bottown:flow-stop v=1 reason="agent did nothing" actor="` + name + `" -->`
			}
			for _, want := range append(tc.wants, "log: /runs/r-1.log", "last error: "+line, marker) {
				if !strings.Contains(api.comments[0], want) {
					t.Errorf("stop comment lacks %q:\n%s", want, api.comments[0])
				}
			}
			if got := strings.Contains(api.comments[0], hint); got == tc.toolCalls {
				t.Errorf("stop comment has the hint: %v, want %v:\n%s", got, !tc.toolCalls, api.comments[0])
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

func TestFlowBehindCleanApprovedStopsReadyToMerge(t *testing.T) {
	for _, pr := range []server.PROut{openPR(), milestonePR()} {
		for _, attended := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/attended=%v", pr.BaseBranch, attended), func(t *testing.T) {
				api := &fakeAPI{open: true, pr: pr, merge: server.PRMergeStatus{Behind: true, BehindCommits: 2, Clean: true, HeadSHA: "h1", BaseSHA: "b2"}}
				approve(api)
				opts := testOptions()
				opts.Attended = attended
				var out bytes.Buffer
				runner := RunnerFunc(func(context.Context, RunRequest) error {
					t.Fatal("an approved PR behind a clean base should not start a run")
					return nil
				})
				pause := func() func() { t.Fatal("no prompt expected"); return func() {} }
				result, err := (&Supervisor{API: api, Runner: runner, In: strings.NewReader("y\n"), Out: &out, Pause: pause}).Run(context.Background(), opts)
				if err != nil || result.Reason != ReadyToMerge {
					t.Fatalf("result = %+v, err = %v", result, err)
				}
				if api.pr.HeadSHA != "h1" {
					t.Fatalf("head = %q, want h1 unchanged", api.pr.HeadSHA)
				}
				if got := out.String(); strings.Contains(got, "[Y/n]") || strings.Contains(got, "Merge ") {
					t.Fatalf("output = %q, want no merge prompt or merge", got)
				}
				want := fmt.Sprintf("!12 is approved at h1: behind `%s` by 2 commits, cleanly; `bottown merge-all` lands it", pr.BaseBranch)
				if result.Detail != want {
					t.Fatalf("detail = %q, want %q", result.Detail, want)
				}
				if len(api.comments) != 1 || !strings.Contains(api.comments[0], want) {
					t.Fatalf("comments = %v, want %q", api.comments, want)
				}
			})
		}
	}
}

func TestFlowBehindCleanUnapprovedGatedAndVerifiedAtOwnHead(t *testing.T) {
	for _, pr := range []server.PROut{openPR(), milestonePR()} {
		for _, attended := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/attended=%v", pr.BaseBranch, attended), func(t *testing.T) {
				api := &fakeAPI{open: true, pr: pr, merge: server.PRMergeStatus{Behind: true, BehindCommits: 3, Clean: true, HeadSHA: "h1", BaseSHA: "b2"}}
				checker := &fakeChecker{check: passing}
				opts := gateOptions()
				opts.Attended = attended
				var calls []RunRequest
				runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
					calls = append(calls, req)
					if req.Kind != policy.Verify {
						t.Fatalf("run = %s, want verify", req.Kind)
					}
					approve(api)
					return nil
				})
				pause := func() func() { t.Fatal("no prompt expected"); return func() {} }
				var out bytes.Buffer
				result, err := (&Supervisor{API: api, Runner: runner, Checker: checker, In: strings.NewReader("y\n"), Out: &out, Pause: pause}).Run(context.Background(), opts)
				if err != nil || result.Reason != ReadyToMerge {
					t.Fatalf("result = %+v, err = %v", result, err)
				}
				if api.pr.HeadSHA != "h1" {
					t.Fatalf("head = %q, want h1 unchanged", api.pr.HeadSHA)
				}
				if strings.Join(checker.checked, ",") != "h1" {
					t.Fatalf("checked = %v, want [h1]", checker.checked)
				}
				if len(calls) != 1 || calls[0].Kind != policy.Verify || calls[0].SHA != "h1" || !calls[0].ChecksPassed {
					t.Fatalf("calls = %+v, want one verify at h1 with ChecksPassed", calls)
				}
				if got := out.String(); strings.Contains(got, "[Y/n]") || strings.Contains(got, "merged ") {
					t.Fatalf("output = %q, want no merge prompt or merge", got)
				}
				want := fmt.Sprintf("!12 is approved at h1: behind `%s` by 3 commits, cleanly; `bottown merge-all` lands it", pr.BaseBranch)
				if result.Detail != want {
					t.Fatalf("detail = %q, want %q", result.Detail, want)
				}
			})
		}
	}
}

func TestFlowMergeBaseConflictStartsImplementRound(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR(), merge: server.PRMergeStatus{Behind: true, Clean: false, Conflicts: []string{"a.txt", "b.txt"}}}
	var calls []RunRequest
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		calls = append(calls, req)
		switch req.Kind {
		case policy.Implement:
			// The round merges the base and pushes a clean head.
			api.pr.HeadSHA = "h2"
			api.merge = server.PRMergeStatus{}
		case policy.Verify:
			approve(api)
		}
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(calls) != 2 || calls[0].Kind != policy.Implement || calls[1].Kind != policy.Verify || calls[1].SHA != "h2" {
		t.Fatalf("runs = %+v, want implement then verify at h2", calls)
	}
	note := calls[0].Note
	for _, want := range []string{"resolve merge conflicts", "`a.txt`, `b.txt`", "git merge bottown/main", "!12"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q, want %q in it", note, want)
		}
	}
	if len(api.comments) == 0 || !strings.Contains(api.comments[0], "conflicts with main") || !strings.Contains(api.comments[0], "a.txt, b.txt") {
		t.Fatalf("comments = %v, want the conflict announced on the issue", api.comments)
	}
}

func TestFlowApprovedBehindConflictingBaseGetsConflictRoundAndVerify(t *testing.T) {
	for _, pr := range []server.PROut{openPR(), milestonePR()} {
		t.Run(pr.BaseBranch, func(t *testing.T) {
			// Already approved at h1; a sibling then lands on the base, so
			// the trial merge conflicts. The flow starts a conflict round
			// instead of stopping ready to merge, and gates and verifies the
			// head the implementer pushes.
			api := &fakeAPI{open: true, pr: pr, merge: server.PRMergeStatus{Behind: true, Clean: false, Conflicts: []string{"a.txt"}}}
			approve(api)
			checker := &fakeChecker{check: passing}
			var calls []RunRequest
			runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
				calls = append(calls, req)
				switch req.Kind {
				case policy.Implement:
					// Conflict resolution changes the patch, so the old
					// approval goes stale and does not carry over.
					pushFix(api, "h2")
					api.merge = server.PRMergeStatus{}
				case policy.Verify:
					reapprove(api)
				}
				return nil
			})
			result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), gateOptions())
			if err != nil || result.Reason != ReadyToMerge {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if len(calls) != 2 || calls[0].Kind != policy.Implement || !strings.Contains(calls[0].Note, "resolve merge conflicts") ||
				calls[1].Kind != policy.Verify || calls[1].SHA != "h2" || !calls[1].ChecksPassed {
				t.Fatalf("runs = %+v, want conflict implement round then verify at h2", calls)
			}
			if strings.Join(checker.checked, ",") != "h2" {
				t.Fatalf("checked = %v, want [h2]", checker.checked)
			}
			if api.pr.HeadSHA != "h2" || result.Detail != "!12 is approved at h2" {
				t.Fatalf("head = %q, detail = %q", api.pr.HeadSHA, result.Detail)
			}
		})
	}
}

func requestChanges(api *fakeAPI) {
	api.pr.Reviews = append(api.pr.Reviews, server.ReviewOut{ID: int64(len(api.pr.Reviews) + 1), Reviewer: "agy", Official: true, State: "REQUEST_CHANGES", CommitID: api.pr.HeadSHA})
}

// pushFix moves the head to sha, as an implement round's push does: the
// earlier reviews go stale.
func pushFix(api *fakeAPI, sha string) {
	api.pr.HeadSHA = sha
	for i := range api.pr.Reviews {
		api.pr.Reviews[i].Stale = true
	}
}

// reapprove is the verifier's approval replacing its earlier verdict.
func reapprove(api *fakeAPI) {
	for i := range api.pr.Reviews {
		api.pr.Reviews[i].Official = false
	}
	approve(api)
}

func TestFlowChangesRequestedWhenBehindCleanVerifiesAtFixHead(t *testing.T) {
	// A request for changes at h1 while behind main cleanly: the implement
	// round pushes h1b, and the verifier reviews h1b directly with no merge
	// commit.
	api := &fakeAPI{open: true, pr: openPR(), merge: server.PRMergeStatus{Behind: true, Clean: true, HeadSHA: "h1", BaseSHA: "b2"}}
	requestChanges(api)
	opts := testOptions()
	opts.Attended = true
	var calls []RunRequest
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		calls = append(calls, req)
		switch req.Kind {
		case policy.Implement:
			pushFix(api, "h1b")
			api.merge.HeadSHA = "h1b"
		case policy.Verify:
			reapprove(api)
		}
		return nil
	})
	var out bytes.Buffer
	result, err := (&Supervisor{API: api, Runner: runner, In: strings.NewReader("y\n"), Out: &out}).Run(context.Background(), opts)
	if err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(calls) != 2 || calls[0].Kind != policy.Implement || calls[0].Note != "" || calls[1].Kind != policy.Verify || calls[1].SHA != "h1b" {
		t.Fatalf("runs = %+v, want implement, then verify at h1b", calls)
	}
}

func TestFlowChangesRequestedImplementsBeforeResolvingConflicts(t *testing.T) {
	// The request for changes is answered first, then the conflicts.
	pr := openPR()
	pr.Mergeable = false
	api := &fakeAPI{open: true, pr: pr, merge: server.PRMergeStatus{Behind: true, Clean: false, Conflicts: []string{"a.txt"}}}
	requestChanges(api)
	var calls []RunRequest
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		calls = append(calls, req)
		switch {
		case req.Kind == policy.Implement && req.Note == "":
			pushFix(api, "h1b")
		case req.Kind == policy.Implement:
			pushFix(api, "h2")
			api.pr.Mergeable = true
			api.merge = server.PRMergeStatus{}
		case req.Kind == policy.Verify:
			reapprove(api)
		}
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	if err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(calls) != 3 || calls[0].Kind != policy.Implement || calls[0].Note != "" ||
		calls[1].Kind != policy.Implement || !strings.Contains(calls[1].Note, "resolve merge conflicts") ||
		calls[2].Kind != policy.Verify || calls[2].SHA != "h2" {
		t.Fatalf("runs = %+v, want the review round, the conflict round, then verify at h2", calls)
	}
}

func TestFlowMergeBaseConflictRoundPushesNothing(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR(), merge: server.PRMergeStatus{Behind: true, Clean: false, Conflicts: []string{"a.txt"}}}
	implements := 0
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		if req.Kind != policy.Implement {
			t.Fatalf("run = %s, want implement", req.Kind)
		}
		implements++
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), testOptions())
	var stop *StopError
	if !errors.As(err, &stop) || result.Reason != ConflictsWithBase || implements != 1 {
		t.Fatalf("result = %+v, err = %v, implements = %d", result, err, implements)
	}
	if !strings.Contains(result.Detail, "pushed no new commit") || !strings.Contains(result.Detail, "a.txt") {
		t.Fatalf("detail = %q", result.Detail)
	}
}

func TestFlowMergeBaseConflictOutOfRounds(t *testing.T) {
	api := &fakeAPI{open: true, pr: openPR(), merge: server.PRMergeStatus{Behind: true, Clean: false, Conflicts: []string{"a.txt", "b.txt"}}}
	implements := 0
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		if req.Kind != policy.Implement {
			t.Fatalf("run = %s, want implement", req.Kind)
		}
		// Each round pushes, but the base keeps conflicting.
		implements++
		api.pr.HeadSHA = fmt.Sprintf("h%d", implements+1)
		return nil
	})
	opts := testOptions()
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), opts)
	var stop *StopError
	if !errors.As(err, &stop) || result.Reason != ConflictsWithBase || implements != opts.Rounds {
		t.Fatalf("result = %+v, err = %v, implements = %d", result, err, implements)
	}
	if !strings.Contains(result.Detail, "a.txt, b.txt") || !strings.Contains(result.Detail, "3 conflict rounds are used") {
		t.Fatalf("detail = %q", result.Detail)
	}
}

func milestonePR() server.PROut {
	pr := openPR()
	pr.BaseBranch = "milestone/run"
	return pr
}

func TestFlowMilestoneBaseConflictStartsImplementRound(t *testing.T) {
	api := &fakeAPI{open: true, pr: milestonePR(), merge: server.PRMergeStatus{Behind: true, Clean: false, Conflicts: []string{"a.txt"}}}
	opts := testOptions()
	var calls []RunRequest
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		calls = append(calls, req)
		switch req.Kind {
		case policy.Implement:
			api.pr.HeadSHA = "h2"
			api.merge = server.PRMergeStatus{}
		case policy.Verify:
			approve(api)
		}
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner}).Run(context.Background(), opts)
	if err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(calls) != 2 || calls[0].Kind != policy.Implement || calls[1].Kind != policy.Verify {
		t.Fatalf("runs = %+v, want implement then verify", calls)
	}
	if len(api.comments) == 0 || !strings.Contains(api.comments[0], "conflicts with milestone/run") {
		t.Fatalf("comments = %v", api.comments)
	}

	// Out of rounds, the flow stops as conflicts with base.
	api = &fakeAPI{open: true, pr: milestonePR(), merge: server.PRMergeStatus{Behind: true, Clean: false, Conflicts: []string{"a.txt"}}}
	implements := 0
	runner = RunnerFunc(func(context.Context, RunRequest) error {
		implements++
		api.pr.HeadSHA = fmt.Sprintf("h%d", implements+1)
		return nil
	})
	result, err = (&Supervisor{API: api, Runner: runner}).Run(context.Background(), opts)
	var stop *StopError
	if !errors.As(err, &stop) || result.Reason != ConflictsWithBase || implements != opts.Rounds {
		t.Fatalf("result = %+v, err = %v, implements = %d", result, err, implements)
	}
}
