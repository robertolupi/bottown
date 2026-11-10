package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/webui"
)

// Validate without executing: suggestions must never start a command in tests.
func validateSuggestedCommand(sample string) error {
	args := strings.Fields(sample)
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	if args[0] == "git" {
		if sample == "git pull --ff-only bottown main" || sample == "git push origin main" {
			return nil
		}
		return fmt.Errorf("unexpected git command shape: %s", sample)
	}
	if args[0] != "bottown" {
		return fmt.Errorf("unexpected executable: %s", args[0])
	}
	cmd, rest, err := newRoot().Find(args[1:])
	if err != nil {
		return err
	}
	if !cmd.Runnable() {
		return fmt.Errorf("not a runnable command: %s", cmd.CommandPath())
	}
	if err := cmd.ParseFlags(rest); err != nil {
		return err
	}
	if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
		return err
	}
	return cmd.ValidateRequiredFlags()
}

func TestSuggestedCommands(t *testing.T) {
	for _, sample := range webui.SuggestedCommands() {
		t.Run(sample, func(t *testing.T) {
			if err := validateSuggestedCommand(sample); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSuggestedCommandsRejectInvalid(t *testing.T) {
	for _, tc := range []struct{ sample, want string }{
		{"bottown nonexistent 42", "unknown command"},
		{"bottown flow 42 --unknown-flag", "unknown flag"},
		{"bottown milestone run demo", `required flag(s) "agents", "parallel" not set`},
		{"git push --force origin main", "unexpected git command shape"},
	} {
		t.Run(tc.sample, func(t *testing.T) {
			if err := validateSuggestedCommand(tc.sample); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
