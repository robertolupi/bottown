package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
)

// TestExitCode: a flow stopped because its agent did nothing exits with
// its own status, through the wrapping; every other error exits 1.
func TestExitCode(t *testing.T) {
	stop := func(r flow.StopReason) error { return &flow.StopError{Result: flow.Result{Issue: 9, Reason: r}} }
	for _, tc := range []struct {
		err  error
		want int
	}{
		{stop(flow.AgentDidNothing), ExitAgentDidNothing},
		{fmt.Errorf("flow: %w", stop(flow.AgentDidNothing)), ExitAgentDidNothing},
		{stop(flow.RunFailed), 1},
		{stop(flow.OutOfRounds), 1},
		{errors.New("boom"), 1},
	} {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// TestIdleCheck: implement and verify runs that succeed without a tool call
// fail, except under the testing hook's script; other kinds do not.
func TestIdleCheck(t *testing.T) {
	t.Setenv(runs.HarnessCmdEnv, "")
	for kind, want := range map[string]bool{policy.Implement: true, policy.Verify: true, policy.Discuss: false, policy.Retro: false, policy.Plan: false} {
		if got := idleCheck(kind); got != want {
			t.Errorf("idleCheck(%s) = %v, want %v", kind, got, want)
		}
	}
	t.Setenv(runs.HarnessCmdEnv, "true")
	if idleCheck(policy.Implement) {
		t.Error("idleCheck under the script hook = true")
	}
}
