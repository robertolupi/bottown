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
