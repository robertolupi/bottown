package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/flow"
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
