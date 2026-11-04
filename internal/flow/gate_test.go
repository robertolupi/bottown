package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/server"
)

// fakeChecker passes or fails the post-implement checks per head.
type fakeChecker struct {
	check   func(context.Context, CheckRequest) (CheckOutcome, error)
	checked []string
}

func (c *fakeChecker) Check(ctx context.Context, req CheckRequest) (CheckOutcome, error) {
	c.checked = append(c.checked, req.SHA)
	return c.check(ctx, req)
}

func (c *fakeChecker) LogPath(req CheckRequest) string { return "/runs/flow-9-" + req.SHA + ".e2e.log" }

func passing(ctx context.Context, req CheckRequest) (CheckOutcome, error) {
	return CheckOutcome{Passed: true, Description: "passed: make e2e in 4m10s", Log: "/runs/flow-9-" + req.SHA + ".e2e.log"}, nil
}

func failing(ctx context.Context, req CheckRequest) (CheckOutcome, error) {
	return CheckOutcome{Description: "failed: make e2e (exit 2) after 3m", Summary: "  1204  --- FAIL: TestE2E/flow", Log: "/runs/flow-9-" + req.SHA + ".e2e.log"}, nil
}

func gateOptions() Options {
	opts := testOptions()
	opts.CheckRounds, opts.CheckTimeout = 2, time.Second
	return opts
}

// pushingRunner implements by pushing a new head h1, h2, … and verifies
// with verdict.
func pushingRunner(api *fakeAPI, reqs *[]RunRequest, verdict func() string) Runner {
	implements := 0
	return RunnerFunc(func(_ context.Context, req RunRequest) error {
		*reqs = append(*reqs, req)
		if req.Kind == policy.Implement {
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
		api.pr.Reviews = append(api.pr.Reviews, server.ReviewOut{ID: int64(len(api.pr.Reviews) + 1), Reviewer: "agy", Official: true, State: verdict(), CommitID: api.pr.HeadSHA})
		return nil
	})
}

func kinds(reqs []RunRequest) string {
	var k []string
	for _, r := range reqs {
		k = append(k, r.Kind)
	}
	return strings.Join(k, ",")
}

func TestGatePassStartsVerify(t *testing.T) {
	api := &fakeAPI{}
	checker := &fakeChecker{check: passing}
	var reqs []RunRequest
	runner := pushingRunner(api, &reqs, func() string { return "APPROVED" })
	result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), gateOptions())
	if err != nil || result.Reason != ReadyToMerge || kinds(reqs) != "implement,verify" {
		t.Fatalf("result = %+v, error = %v, runs = %s", result, err, kinds(reqs))
	}
	if strings.Join(checker.checked, ",") != "h1" || api.gate["h1"].State != "success" {
		t.Fatalf("checked %v, gate %+v", checker.checked, api.gate)
	}
	v := reqs[1]
	if !v.ChecksPassed || !strings.Contains(v.Note, "`h1`") || !strings.Contains(v.Note, "/runs/flow-9-h1.e2e.log") || !strings.Contains(v.Note, "passed: make e2e") {
		t.Fatalf("verify request = %+v", v)
	}
}

// A failure starts an implement round with the failure and the log; it does
// not use up a review round: with Rounds: 2, one failed check and one
// request for changes still leave a round for the approval.
func TestGateFailureStartsImplementRound(t *testing.T) {
	api := &fakeAPI{}
	checker := &fakeChecker{}
	checker.check = func(ctx context.Context, req CheckRequest) (CheckOutcome, error) {
		if req.SHA == "h1" {
			return failing(ctx, req)
		}
		return passing(ctx, req)
	}
	var reqs []RunRequest
	verdicts := []string{"REQUEST_CHANGES", "APPROVED"}
	runner := pushingRunner(api, &reqs, func() string { v := verdicts[0]; verdicts = verdicts[1:]; return v })
	opts := gateOptions()
	opts.Rounds = 2
	result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), opts)
	if err != nil || result.Reason != ReadyToMerge || kinds(reqs) != "implement,implement,verify,implement,verify" {
		t.Fatalf("result = %+v, error = %v, runs = %s", result, err, kinds(reqs))
	}
	fix := reqs[1]
	if fix.ChecksPassed || !strings.Contains(fix.Note, "`h1`") || !strings.Contains(fix.Note, "--- FAIL: TestE2E/flow") || !strings.Contains(fix.Note, "/runs/flow-9-h1.e2e.log") {
		t.Fatalf("fix request = %+v", fix)
	}
	if reqs[0].Note != "" || reqs[3].Note != "" {
		t.Fatalf("plain implement rounds got a note: %q, %q", reqs[0].Note, reqs[3].Note)
	}
	if strings.Join(checker.checked, ",") != "h1,h2,h3" || api.gate["h1"].State != "failure" || api.gate["h3"].State != "success" {
		t.Fatalf("checked %v, gate %+v", checker.checked, api.gate)
	}
	if len(api.comments) != 2 || !strings.Contains(api.comments[0], "checks failed") || !strings.Contains(api.comments[0], "/runs/flow-9-h1.e2e.log") {
		t.Fatalf("comments = %v", api.comments)
	}
}

func TestGateCheckRoundsLimit(t *testing.T) {
	for _, rounds := range []int{0, 2} {
		api := &fakeAPI{}
		checker := &fakeChecker{check: failing}
		var reqs []RunRequest
		runner := pushingRunner(api, &reqs, func() string { t.Fatal("verifier started on failed checks"); return "" })
		opts := gateOptions()
		opts.CheckRounds = rounds
		opts.Rounds = 1
		result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), opts)
		var stop *StopError
		if !errors.As(err, &stop) || result.Reason != ChecksFailed || len(reqs) != 1+rounds || len(checker.checked) != 1+rounds {
			t.Fatalf("check rounds %d: result = %+v, error = %v, runs = %s, checked = %v", rounds, result, err, kinds(reqs), checker.checked)
		}
		if last := api.comments[len(api.comments)-1]; !strings.Contains(last, "checks failed") || !strings.Contains(last, ".e2e.log") {
			t.Fatalf("stop comment = %q", last)
		}
	}
}

func TestGateFixRoundWithoutPush(t *testing.T) {
	api := &fakeAPI{}
	checker := &fakeChecker{check: failing}
	implements := 0
	runner := RunnerFunc(func(_ context.Context, req RunRequest) error {
		if req.Kind != policy.Implement {
			t.Fatal("verifier started on failed checks")
		}
		implements++
		api.open, api.pr = true, openPR()
		return nil
	})
	result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), gateOptions())
	if result.Reason != RunFailed || implements != 2 || len(checker.checked) != 1 || !strings.Contains(result.Detail, "pushed no new commit") {
		t.Fatalf("result = %+v, error = %v, implements = %d, checked = %v", result, err, implements, checker.checked)
	}
}

// A resumed flow follows the gate status at the head and does not check
// that head again.
func TestGateResumeFollowsHeadStatus(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		api := &fakeAPI{open: true, pr: openPR()}
		_ = api.RecordGate(context.Background(), "demo", "h1", "success", "passed: make e2e in 4m")
		checker := &fakeChecker{check: failing}
		var reqs []RunRequest
		runner := pushingRunner(api, &reqs, func() string { return "APPROVED" })
		result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), gateOptions())
		if err != nil || result.Reason != ReadyToMerge || kinds(reqs) != "verify" || len(checker.checked) != 0 {
			t.Fatalf("result = %+v, error = %v, runs = %s, checked = %v", result, err, kinds(reqs), checker.checked)
		}
		if !reqs[0].ChecksPassed || !strings.Contains(reqs[0].Note, "/runs/flow-9-h1.e2e.log") {
			t.Fatalf("verify request = %+v", reqs[0])
		}
	})
	t.Run("failure with rounds left", func(t *testing.T) {
		api := &fakeAPI{open: true, pr: openPR()}
		api.pr.HeadSHA = "h0"
		_ = api.RecordGate(context.Background(), "demo", "h0", "failure", "failed: make e2e (exit 2)")
		checker := &fakeChecker{check: passing}
		var reqs []RunRequest
		runner := pushingRunner(api, &reqs, func() string { return "APPROVED" })
		result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), gateOptions())
		if err != nil || result.Reason != ReadyToMerge || kinds(reqs) != "implement,verify" || strings.Join(checker.checked, ",") != "h1" {
			t.Fatalf("result = %+v, error = %v, runs = %s, checked = %v", result, err, kinds(reqs), checker.checked)
		}
		if !strings.Contains(reqs[0].Note, "failed: make e2e (exit 2)") || !strings.Contains(reqs[0].Note, "/runs/flow-9-h0.e2e.log") {
			t.Fatalf("fix request = %+v", reqs[0])
		}
	})
	t.Run("failure out of rounds", func(t *testing.T) {
		api := &fakeAPI{open: true, pr: openPR()}
		for _, sha := range []string{"h0", "h1"} {
			_ = api.RecordGate(context.Background(), "demo", sha, "failure", "failed: make e2e (exit 2)")
		}
		checker := &fakeChecker{check: passing}
		called := false
		runner := RunnerFunc(func(context.Context, RunRequest) error { called = true; return nil })
		opts := gateOptions()
		opts.CheckRounds = 1
		result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), opts)
		var stop *StopError
		if !errors.As(err, &stop) || result.Reason != ChecksFailed || called || len(checker.checked) != 0 {
			t.Fatalf("result = %+v, error = %v, runner called = %v, checked = %v", result, err, called, checker.checked)
		}
	})
}

func TestGateSkippedAndTimeout(t *testing.T) {
	t.Run("no checks named", func(t *testing.T) {
		api := &fakeAPI{}
		checker := &fakeChecker{check: func(context.Context, CheckRequest) (CheckOutcome, error) { return CheckOutcome{Skipped: true}, nil }}
		var reqs []RunRequest
		runner := pushingRunner(api, &reqs, func() string { return "APPROVED" })
		result, err := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), gateOptions())
		if err != nil || result.Reason != ReadyToMerge || kinds(reqs) != "implement,verify" || reqs[1].ChecksPassed || len(api.gate) != 0 {
			t.Fatalf("result = %+v, error = %v, runs = %+v, gate = %v", result, err, reqs, api.gate)
		}
	})
	t.Run("timeout is a failure", func(t *testing.T) {
		api := &fakeAPI{}
		checker := &fakeChecker{check: func(ctx context.Context, req CheckRequest) (CheckOutcome, error) {
			<-ctx.Done()
			return CheckOutcome{Description: "failed: make e2e (exit -1)", Log: "/runs/x.e2e.log"}, ctx.Err()
		}}
		var reqs []RunRequest
		runner := pushingRunner(api, &reqs, func() string { return "APPROVED" })
		opts := gateOptions()
		opts.CheckRounds, opts.CheckTimeout = 0, 5*time.Millisecond
		result, _ := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), opts)
		if result.Reason != ChecksFailed || api.gate["h1"].State != "failure" || !strings.Contains(api.gate["h1"].Description, "timed out after 5ms") {
			t.Fatalf("result = %+v, gate = %+v", result, api.gate)
		}
	})
	t.Run("checks that cannot run stop the flow", func(t *testing.T) {
		api := &fakeAPI{}
		checker := &fakeChecker{check: func(context.Context, CheckRequest) (CheckOutcome, error) {
			return CheckOutcome{}, errors.New("no worktree")
		}}
		var reqs []RunRequest
		runner := pushingRunner(api, &reqs, func() string { return "APPROVED" })
		result, _ := (&Supervisor{API: api, Runner: runner, Checker: checker}).Run(context.Background(), gateOptions())
		if result.Reason != RunFailed || !strings.Contains(result.Detail, "no worktree") || len(api.gate) != 0 {
			t.Fatalf("result = %+v, gate = %v", result, api.gate)
		}
	})
}
