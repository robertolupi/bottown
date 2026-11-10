package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// forgeCall is one call the flow made to the forge, with the step it had
// last reported when it made it.
type forgeCall struct {
	method string
	step   string
}

// watchedAPI wraps the fake API and records every call with the flow's
// reported step, so a test can assert a run or check is never reported as
// current while the flow is reading the forge. This is what the step
// sequence alone does not catch: the reads that follow a run, and the
// recording that follows the checks, used to run under the old step.
type watchedAPI struct {
	*fakeAPI
	step  func() string
	calls []forgeCall
}

func (w *watchedAPI) seen(method string) {
	w.calls = append(w.calls, forgeCall{method: method, step: w.step()})
}

func (w *watchedAPI) Actors(ctx context.Context) ([]server.ActorInfo, error) {
	w.seen("Actors")
	return w.fakeAPI.Actors(ctx)
}

func (w *watchedAPI) Issue(ctx context.Context, repo string, number int64) (server.IssueOut, error) {
	w.seen("Issue")
	return w.fakeAPI.Issue(ctx, repo, number)
}

func (w *watchedAPI) OpenPR(ctx context.Context, repo, branch string) (server.PRItem, bool, error) {
	w.seen("OpenPR")
	return w.fakeAPI.OpenPR(ctx, repo, branch)
}

func (w *watchedAPI) PR(ctx context.Context, repo string, number int64) (server.PROut, error) {
	w.seen("PR")
	return w.fakeAPI.PR(ctx, repo, number)
}

func (w *watchedAPI) ReconcileCarries(ctx context.Context, repo string, number int64) error {
	w.seen("ReconcileCarries")
	return w.fakeAPI.ReconcileCarries(ctx, repo, number)
}

func (w *watchedAPI) LiveRuns(ctx context.Context, repo string) ([]secrets.Run, error) {
	w.seen("LiveRuns")
	return w.fakeAPI.LiveRuns(ctx, repo)
}

func (w *watchedAPI) Comment(ctx context.Context, repo string, number int64, body string) error {
	w.seen("Comment")
	return w.fakeAPI.Comment(ctx, repo, number, body)
}

func (w *watchedAPI) PRMergeStatus(ctx context.Context, repo string, number int64) (server.PRMergeStatus, error) {
	w.seen("PRMergeStatus")
	return w.fakeAPI.PRMergeStatus(ctx, repo, number)
}

func (w *watchedAPI) RecordGate(ctx context.Context, repo, sha, check, state, description string) error {
	w.seen("RecordGate")
	return w.fakeAPI.RecordGate(ctx, repo, sha, check, state, description)
}

func (w *watchedAPI) GateFailures(ctx context.Context, repo string, number int64) ([]string, error) {
	w.seen("GateFailures")
	return w.fakeAPI.GateFailures(ctx, repo, number)
}

// TestFlowReportsStatus drives the flow through an implement run that
// waits for a busy actor, the post-implement checks, a verify round that
// requests changes, a second implement and verify, and an approval, and
// checks the steps the flow reports through Options.Status: the wait for
// the busy actor with its detail, the checks, the verify, and the wait
// between the review rounds. It also watches every forge call, so the
// status never claims a run or check that has ended is still going.
func TestFlowReportsStatus(t *testing.T) {
	var steps []runs.FlowStatusView
	api := &watchedAPI{fakeAPI: &fakeAPI{}}
	api.step = func() string {
		if len(steps) == 0 {
			return ""
		}
		return steps[len(steps)-1].Step
	}
	checker := &fakeChecker{check: passing}
	attempts, implements := 0, 0
	verdicts := []string{"REQUEST_CHANGES", "APPROVED"}
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		if req.Kind == policy.Implement {
			attempts++
			if attempts == 1 {
				return fmt.Errorf("%w: actor claude already has a live batch run: r-holder", ErrActorBusy)
			}
			implements++
			reviews := api.pr.Reviews
			for i := range reviews {
				reviews[i].Stale = true
			}
			api.open, api.pr = true, openPR()
			api.pr.HeadSHA = "h" + string(rune('0'+implements))
			api.pr.Reviews = reviews
			return nil
		}
		for i := range api.pr.Reviews {
			api.pr.Reviews[i].Official = false
		}
		verdict := verdicts[0]
		verdicts = verdicts[1:]
		api.pr.Reviews = append(api.pr.Reviews, server.ReviewOut{ID: 99, Reviewer: "agy", Official: true, State: verdict, CommitID: api.pr.HeadSHA})
		return nil
	})
	opts := gateOptions()
	opts.SerializeActors, opts.ActorWait = true, time.Minute
	opts.Status = func(st runs.FlowStatusView) { steps = append(steps, st) }
	result, err := (&Supervisor{API: api, Runner: runner, Checker: checker, RetryDelay: time.Millisecond}).Run(context.Background(), opts)
	if err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("result = %+v, error = %v", result, err)
	}

	var names []string
	for _, st := range steps {
		names = append(names, st.Step)
	}
	want := strings.Join([]string{
		runs.StepBetweenRuns,
		runs.StepImplementing, runs.StepWaitingActor, runs.StepImplementing,
		runs.StepBetweenRuns, runs.StepChecks, runs.StepBetweenRuns, runs.StepVerifying,
		runs.StepBetweenRuns, runs.StepImplementing,
		runs.StepBetweenRuns, runs.StepChecks, runs.StepBetweenRuns, runs.StepVerifying,
		runs.StepBetweenRuns,
	}, ",")
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("steps =\n  %s\nwant\n  %s", got, want)
	}

	// Every forge call happens while the flow reports between runs, or
	// waiting for a busy actor: never while it claims a run or check that
	// has ended is still going. The reads that follow a run and the
	// recording that follows the checks are the cases this guards.
	var reads, recorded bool
	for _, c := range api.calls {
		if c.step != runs.StepBetweenRuns && c.step != runs.StepWaitingActor {
			t.Errorf("%s ran while the flow reported %q, want between runs or waiting for actor", c.method, c.step)
		}
		switch c.method {
		case "OpenPR", "PR":
			reads = true
		case "RecordGate":
			recorded = true
		}
	}
	if !reads || !recorded {
		t.Fatalf("the flow never read the PR or recorded the checks: %+v", api.calls)
	}

	// The status carries the issue, the actors with their specs, the
	// rounds and the round the flow is in, and this process's pid.
	first := steps[0]
	if first.Issue != 9 || first.Implement != "claude" || first.Verify != "agy" || first.Rounds != 3 || first.PID != os.Getpid() || first.Since.IsZero() {
		t.Fatalf("first status = %+v", first)
	}
	if first.Round != 1 {
		t.Fatalf("first status round = %d, want 1", first.Round)
	}
	if last := steps[len(steps)-1]; last.Round != 2 {
		t.Fatalf("the second round's status round = %d, want 2", last.Round)
	}

	// The wait names the actor and the run that holds it.
	wait := steps[2]
	if wait.Step != runs.StepWaitingActor || !strings.Contains(wait.Detail, "claude") || !strings.Contains(wait.Detail, "r-holder") {
		t.Fatalf("waiting status = %+v", wait)
	}
	// Every other step carries no detail.
	for _, st := range steps {
		if st.Step != runs.StepWaitingActor && st.Detail != "" {
			t.Fatalf("%s carries detail %q", st.Step, st.Detail)
		}
	}
}

// TestFlowRefusedLockWritesNothing: a flow that cannot take the lock (a
// second flow on the issue) neither reports a step nor runs its end
// cleanup, so it never deletes the holder's status file.
func TestFlowRefusedLockWritesNothing(t *testing.T) {
	var statuses, ended int
	busy := errors.New("another flow is already driving demo#9")
	s := &Supervisor{
		API:    &fakeAPI{},
		Runner: RunnerFunc(func(context.Context, RunRequest) error { return nil }),
		Lock:   func(string, int64) (func(), error) { return nil, busy },
	}
	opts := testOptions()
	opts.Status = func(runs.FlowStatusView) { statuses++ }
	opts.OnEnd = func() { ended++ }
	_, err := s.Run(context.Background(), opts)
	if !errors.Is(err, busy) {
		t.Fatalf("Run = %v, want the lock's refusal", err)
	}
	if statuses != 0 || ended != 0 {
		t.Fatalf("a refused flow reported %d statuses and ran its cleanup %d times, want none", statuses, ended)
	}
}
