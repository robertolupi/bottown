package flow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
)

// pinningRunner is pushingRunner behind the server's pin: a verify run whose
// SHA is not the PR's head is refused with ErrHeadMoved. before runs ahead
// of each request, to push or refuse it first.
func pinningRunner(api *fakeAPI, reqs *[]RunRequest, before func(RunRequest) error) Runner {
	inner := pushingRunner(api, reqs, func() string { return "APPROVED" })
	return RunnerFunc(func(ctx context.Context, req RunRequest) error {
		if err := before(req); err != nil {
			return err
		}
		if req.Kind == policy.Verify && req.SHA != api.pr.HeadSHA {
			return ErrHeadMoved
		}
		return inner.Run(ctx, req)
	})
}

// A push while the checks run: the passed checks at h1 do not reach a
// verifier pinned at h2; the flow gates h2 before verifying it.
func TestGateHeadMovesDuringChecks(t *testing.T) {
	api := &fakeAPI{}
	checker := &fakeChecker{}
	checker.check = func(ctx context.Context, req CheckRequest) (CheckOutcome, error) {
		if req.SHA == "h1" {
			api.pr.HeadSHA = "h2"
		}
		return passing(ctx, req)
	}
	var reqs, refused []RunRequest
	runner := pinningRunner(api, &reqs, func(req RunRequest) error {
		if req.Kind == policy.Verify && req.SHA != api.pr.HeadSHA {
			refused = append(refused, req)
		}
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), gateOptions())
	if err != nil || result.Reason != ReadyToMerge || kinds(reqs) != "implement,verify" {
		t.Fatalf("result = %+v, error = %v, runs = %s", result, err, kinds(reqs))
	}
	if strings.Join(checker.checked, ",") != "h1,h2" {
		t.Fatalf("checked %v", checker.checked)
	}
	if len(refused) != 1 || refused[0].SHA != "h1" {
		t.Fatalf("refused verify requests = %+v", refused)
	}
	v := reqs[1]
	if v.SHA != "h2" || !v.ChecksPassed || !strings.Contains(v.Note, "`h2`") {
		t.Fatalf("verify request = %+v", v)
	}
}

// A push while the flow waits for the verifier's other batch run: the retry is
// still pinned at the gated head, so it is refused, and the new head is
// gated.
func TestGateHeadMovesWhileWaitingForActor(t *testing.T) {
	api := &fakeAPI{}
	checker := &fakeChecker{check: passing}
	limited := true
	var reqs []RunRequest
	runner := pinningRunner(api, &reqs, func(req RunRequest) error {
		if req.Kind == policy.Verify && limited {
			limited = false
			api.pr.HeadSHA = "h2"
			return ErrActorBusy
		}
		return nil
	})
	opts := gateOptions()
	opts.SerializeActors, opts.ActorWait = true, time.Minute
	result, err := (&Supervisor{API: api, Runner: runner, Checker: checker, RetryDelay: time.Millisecond}).Run(context.Background(), opts)
	if err != nil || result.Reason != ReadyToMerge || kinds(reqs) != "implement,verify" {
		t.Fatalf("result = %+v, error = %v, runs = %s", result, err, kinds(reqs))
	}
	if strings.Join(checker.checked, ",") != "h1,h2" || reqs[1].SHA != "h2" || !strings.Contains(reqs[1].Note, "`h2`") {
		t.Fatalf("checked %v, verify request = %+v", checker.checked, reqs[1])
	}
}
