package webui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
)

// TestActions_FlowRefusesHeldIssue: the web UI's flow start goes through
// the CLI flow's lock, refusing an issue a flow already drives.
func TestActions_FlowRefusesHeldIssue(t *testing.T) {
	_, port := newRunsFixtureServer(t, defaultRunsFixture())
	top := t.TempDir()
	cfg := Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  top,
		FullRepo: "bottown/bottown",
	}
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started := filepath.Join(top, "started")
	fake := filepath.Join(top, "fake_bottown.sh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+started+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.cfg.BottownPath = fake

	dir := filepath.Join(top, ".bottown", "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := runs.HoldFlow(dir, "bottown", 50)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = s.ExecuteFlow(context.Background(), FlowRequest{Issue: 50, Implement: "claude", Verify: "agy"})
	var busy *runs.FlowBusyError
	if !errors.As(err, &busy) || busy.PID != os.Getpid() {
		t.Fatalf("ExecuteFlow on a held issue = %v", err)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("the flow started anyway")
	}
}
