package cli

import (
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
)

func TestStoppedHandover(t *testing.T) {
	rec := runs.Record{ID: "r-stop", Kind: policy.Discuss, Actor: "claude", Family: "anthropic", SHA: "abc123", StopRequested: true}
	got := handoverText(rec, runs.StatusFailed, 0, "harness done")
	for _, want := range []string{"**failed**", "Stopped by the owner.", "harness done"} {
		if !strings.Contains(got, want) {
			t.Errorf("handover lacks %q:\n%s", want, got)
		}
	}
}
