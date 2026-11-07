package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
)

// TestFlowOnePerIssue: a second supervisor on an issue a flow holds
// refuses, naming the holder's pid, and starts nothing; once the first
// ends, or when the lock's pid is dead, the next one takes the lock.
func TestFlowOnePerIssue(t *testing.T) {
	dir := t.TempDir()
	lock := func(repo string, issue int64) (func(), error) { return runs.HoldFlow(dir, repo, issue) }
	approveAll := func(api *fakeAPI) Runner {
		return RunnerFunc(func(_ context.Context, req RunRequest) error {
			if req.Kind == policy.Implement {
				api.open, api.pr = true, openPR()
			} else {
				approve(api)
			}
			return nil
		})
	}

	var second error
	secondCalled := false
	first := &fakeAPI{}
	inner := approveAll(first)
	runner := RunnerFunc(func(ctx context.Context, req RunRequest) error {
		if req.Kind == policy.Implement {
			other := RunnerFunc(func(context.Context, RunRequest) error { secondCalled = true; return nil })
			_, second = (&Supervisor{API: &fakeAPI{}, Runner: other, Lock: lock}).Run(ctx, testOptions())
		}
		return inner.Run(ctx, req)
	})
	if result, err := (&Supervisor{API: first, Runner: runner, Lock: lock}).Run(context.Background(), testOptions()); err != nil || result.Reason != ReadyToMerge {
		t.Fatalf("first flow = %+v, %v", result, err)
	}
	var busy *runs.FlowBusyError
	if !errors.As(second, &busy) || busy.PID != os.Getpid() || !strings.Contains(second.Error(), fmt.Sprintf("kill %d", os.Getpid())) || secondCalled {
		t.Fatalf("second flow error = %v, runner called = %v", second, secondCalled)
	}

	// The first flow released the lock when it ended.
	api := &fakeAPI{}
	if _, err := (&Supervisor{API: api, Runner: approveAll(api), Lock: lock}).Run(context.Background(), testOptions()); err != nil {
		t.Fatalf("flow after the first ended: %v", err)
	}

	// A lock left by a flow that died names a dead pid and is not held.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runs.FlowLockPath(dir, "demo", 9), fmt.Appendln(nil, dead.Process.Pid), 0o600); err != nil {
		t.Fatal(err)
	}
	api = &fakeAPI{}
	if _, err := (&Supervisor{API: api, Runner: approveAll(api), Lock: lock}).Run(context.Background(), testOptions()); err != nil {
		t.Fatalf("flow over a stale lock: %v", err)
	}
}
