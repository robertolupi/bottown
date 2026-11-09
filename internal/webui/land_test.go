package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
)

func TestLandingActionLifecycle(t *testing.T) {
	fix := defaultFixture()
	fix.prs = []*sdk.PullRequest{{Index: 15, State: sdk.StateOpen, Head: &sdk.PRBranchInfo{Ref: "issue/15", Sha: "abc123"}, Base: &sdk.PRBranchInfo{Ref: "main"}}}
	_, port := newFixtureServer(t, fix)
	top := t.TempDir()
	fake := filepath.Join(top, "bottown")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\"\nsleep 30 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Config{Instance: config.Instance{Name: "prod", ServerPort: port}, RepoTop: top, FullRepo: "bottown/bottown", BottownPath: fake})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/actions/land", strings.NewReader(`{"number":15,"head_sha":"abc123"}`))
	w := httptest.NewRecorder()
	s.handleActionLand(w, req)
	var result ActionResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Success {
		t.Fatalf("action: %s (%v)", w.Body, err)
	}
	t.Cleanup(func() { _, _ = s.ExecuteStop(context.Background(), "land-15") })
	rec := s.getLanding(15)
	if rec == nil || rec.Issue != 15 {
		t.Fatalf("record: %+v", rec)
	}
	if _, err := s.ExecuteLand(context.Background(), LandRequest{Number: 15, HeadSHA: "abc123"}); err == nil {
		t.Fatal("duplicate landing accepted")
	}
	live, err := s.ComputeLiveRuns(context.Background())
	if err != nil || len(live) != 1 || live[0].ID != "land-15" || live[0].Kind != "land" {
		t.Fatalf("live: %+v, %v", live, err)
	}
	// The subprocess writes its log on its own schedule, which under load can
	// be after ExecuteLand returns: wait for the line rather than assume it.
	var (
		lines  []runs.LogLine
		exists bool
		b      []byte
	)
	deadline := time.Now().Add(10 * time.Second)
	for {
		lines, exists, err = s.ComputeRunLog(context.Background(), "land-15")
		b, _ = os.ReadFile(s.landingPath(15, ".log"))
		if err == nil && exists && len(lines) > 0 && strings.Contains(string(b), "land 15 --head-sha abc123") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("log: %+v %v %v; file: %q", lines, exists, err, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := s.ExecuteStop(context.Background(), "land-15"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for s.getLanding(15) != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.getLanding(15) != nil {
		t.Fatal("landing still live after stop")
	}
}
