package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// The flow tells a busy actor by the server's error code, whatever the
// message says, and passes the refusal on without the layers' prefixes.
func TestActorBusyRefusal(t *testing.T) {
	refusal := &client.APIError{Status: 409, Method: "POST", Path: "/v1/runs", Code: server.ErrorCodeActorBusy,
		Message: "claude is occupied by r-holder"}
	// As startRun returns it, through callServer.
	err := fmt.Errorf("%w: %w", errActorBusy, fmt.Errorf("instance prod: %w", refusal))
	if !errors.Is(err, errActorBusy) {
		t.Fatalf("errors.Is(%v, errActorBusy) is false", err)
	}
	got := actorBusyRefusal(err)
	if !errors.Is(got, flow.ErrActorBusy) || got.Error() != refusal.Message {
		t.Fatalf("refusal = %q, Is ErrActorBusy: %t", got, errors.Is(got, flow.ErrActorBusy))
	}
}

// A batch run shows a tail of its own on a terminal, an interactive run
// does not, and a flow's runs show in the flow's tail or, without one
// (--unattended), keep the plain output.
func TestRunTailChoice(t *testing.T) {
	if !(runOpts{}).ownTail() {
		t.Error("a batch run does not show its own tail")
	}
	if (runOpts{interactive: true}).ownTail() {
		t.Error("an interactive run shows a tail")
	}
	req := flow.RunRequest{Kind: "implement", Issue: 7, Actor: "claude"}
	unattended := flowRunOpts(req, "", nil)
	if unattended.ownTail() || !unattended.plain {
		t.Errorf("a flow run without the flow's tail shows its own: %+v", unattended)
	}
	shared := &liveTail{}
	attended := flowRunOpts(req, "", shared)
	if attended.ownTail() || attended.tail != shared || attended.plain {
		t.Errorf("a flow run does not show in the flow's tail: %+v", attended)
	}
}

// --repo ORG/NAME and NAME resolve to one repository, so a flow started
// either way takes the same lock, and a qualified name's slash never
// reaches the lock path.
func TestFlowRepoQualifiedSharesLock(t *testing.T) {
	short, err := repoInOrg("bottown", "bottown")
	if err != nil || short != "bottown" {
		t.Fatalf("repoInOrg(bottown) = %q, %v", short, err)
	}
	qualified, err := repoInOrg("bottown", "bottown/bottown")
	if err != nil || qualified != "bottown" {
		t.Fatalf("repoInOrg(bottown/bottown) = %q, %v", qualified, err)
	}
	if _, err := repoInOrg("bottown", "other/bottown"); err == nil {
		t.Fatal("repoInOrg(other/bottown) in org bottown succeeded")
	}

	dir := t.TempDir()
	release, err := runs.HoldFlow(dir, qualified, 338)
	if err != nil {
		t.Fatalf("HoldFlow(%q): %v", qualified, err)
	}
	defer release()
	var busy *runs.FlowBusyError
	if _, err := runs.HoldFlow(dir, short, 338); !errors.As(err, &busy) {
		t.Fatalf("second HoldFlow(%q) = %v, want *FlowBusyError", short, err)
	}
}

func TestFlowDoesNotKnowMergeMilestoneBaseFlag(t *testing.T) {
	cmd := newFlow(&globals{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "merge-milestone-base") {
		t.Fatalf("flow --help still mentions --merge-milestone-base:\n%s", out.String())
	}
	cmd = newFlow(&globals{})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"1", "--implement", "claude", "--verify", "agy", "--merge-milestone-base"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --merge-milestone-base") {
		t.Fatalf("flow --merge-milestone-base = %v, want unknown flag", err)
	}
}
