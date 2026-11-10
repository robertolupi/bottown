package flow

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// TestFlowReportsStatus drives the flow through an implement run that
// waits for a busy actor, the post-implement checks, a verify round that
// requests changes, a second implement and verify, and an approval, and
// checks the steps the flow reports through Options.Status: the wait for
// the busy actor with its detail, the checks, the verify, and the wait
// between the review rounds.
func TestFlowReportsStatus(t *testing.T) {
	api := &fakeAPI{}
	checker := &fakeChecker{check: passing}
	var steps []runs.FlowStatusView
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
		runs.StepBetweenRuns, runs.StepChecks, runs.StepVerifying,
		runs.StepBetweenRuns, runs.StepImplementing,
		runs.StepBetweenRuns, runs.StepChecks, runs.StepVerifying,
		runs.StepBetweenRuns,
	}, ",")
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("steps =\n  %s\nwant\n  %s", got, want)
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
