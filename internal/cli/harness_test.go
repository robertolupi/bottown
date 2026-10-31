package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestInstallCommands(t *testing.T) {
	home := t.TempDir()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	for _, h := range []string{"claude", "agy"} {
		if err := installCommands(cmd, home, h, false); err != nil {
			t.Fatal(err)
		}
	}
	impl, err := os.ReadFile(filepath.Join(home, ".claude", "commands", "bottown-implement.md"))
	if err != nil {
		t.Fatal(err)
	}
	self := selfPath()
	for _, want := range []string{"!`" + self + " run start implement $ARGUMENTS`", "allowed-tools: Bash(" + self + ":*)", harnessMarker, "argument-hint: <issue number>"} {
		if !strings.Contains(string(impl), want) {
			t.Errorf("claude implement command lacks %q:\n%s", want, impl)
		}
	}
	ver, err := os.ReadFile(filepath.Join(home, ".gemini", "config", "global_workflows", "bottown-verify.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ver), self+" run start verify <number>") || !strings.Contains(string(ver), "/bottown-verify") {
		t.Errorf("agy verify workflow:\n%s", ver)
	}

	// Re-running is a no-op; a foreign file is protected unless --force.
	out.Reset()
	if err := installCommands(cmd, home, "claude", false); err != nil || strings.Count(out.String(), "up to date") != 2 {
		t.Errorf("second install: %v %s", err, out.String())
	}
	foreign := filepath.Join(home, ".claude", "commands", "bottown-verify.md")
	os.WriteFile(foreign, []byte("my own command\n"), 0o644)
	if err := installCommands(cmd, home, "claude", false); err == nil || !strings.Contains(err.Error(), "not written by bottown") {
		t.Errorf("overwrote a foreign file: %v", err)
	}
	if err := installCommands(cmd, home, "claude", true); err != nil {
		t.Errorf("--force: %v", err)
	}
	if err := installCommands(cmd, home, "nope", false); err == nil {
		t.Error("unknown harness accepted")
	}
}
