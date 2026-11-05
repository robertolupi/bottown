package webui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

// TestCapture captures from a fixture-server instance, writes the fixture,
// and checks that it loads back strictly and that a preview of it answers
// as the live UI did, with what looks like a secret dropped.
func TestCapture(t *testing.T) {
	fix := defaultRunsFixture()
	now := time.Now().UTC()
	ended := now.Add(-20 * time.Minute)
	fix.issues = []*sdk.Issue{
		{Index: 41, Title: "Done issue", State: sdk.StateClosed, Poster: &sdk.User{UserName: "rlupi"}, Created: now.Add(-2 * time.Hour),
			Body: "seen at http://127.0.0.1:5555/?token=0123abcd&x=1 and http://127.0.0.1:3031/v1/web/login/Tick-et_1"},
		{Index: 42, Title: "Live issue", State: sdk.StateOpen, Poster: &sdk.User{UserName: "rlupi"}, Created: now.Add(-time.Hour)},
		{Index: 43, Title: "Unlinked issue", State: sdk.StateOpen, Poster: &sdk.User{UserName: "rlupi"}, Created: now.Add(-time.Hour)},
	}
	fix.runs = []secrets.Run{
		{ID: "r-live", Kind: "implement", Actor: "agy", Family: "google", Repo: "bottown", Issue: 42,
			Created: now.Add(-10 * time.Minute), Expires: now.Add(50 * time.Minute)},
		{ID: "r-done", Kind: "implement", Actor: "claude", Family: "anthropic", Repo: "bottown", Issue: 41,
			Created: now.Add(-time.Hour), Ended: &ended, Status: "succeeded", Digest: secrets.Digest{Summary: "done"}},
		{ID: "r-pr", Kind: "verify", Actor: "kimi", Family: "moonshot", Repo: "bottown", PR: 77,
			Created: now.Add(-time.Hour), Ended: &ended, Status: "succeeded"},
	}
	_, port := newRunsFixtureServer(t, fix)
	top := t.TempDir()
	runsDir := filepath.Join(top, ".bottown", "runs")
	if err := os.MkdirAll(runsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const runKey = "btk_AAAAbbbbCCCCdddd0123456789-_xyz"
	if err := os.WriteFile(filepath.Join(runsDir, "r-live.log"), []byte("working\nkey "+runKey+"\nowner owner-secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "r-done.log"), []byte("finished\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  top, FullRepo: "bottown/bottown", OwnerKey: "owner-secret-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	fx, _, err := s.Capture(ctx, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// It takes the issues and runs the views link to, and no others.
	for _, n := range []string{"41", "42"} {
		if _, ok := fx.Issues[n]; !ok {
			t.Errorf("issue #%s not captured: %v", n, fx.Summary())
		}
		if _, ok := fx.Timelines[n]; !ok {
			t.Errorf("timeline #%s not captured", n)
		}
	}
	if _, ok := fx.Issues["43"]; ok {
		t.Error("captured #43, which no view links to")
	}
	if _, ok := fx.Timelines["!77"]; !ok {
		t.Error("the issue-less PR's timeline !77 not captured")
	}
	if len(fx.Logs["r-live"]) == 0 || len(fx.Logs["r-done"]) == 0 {
		t.Errorf("logs not captured: %v", fx.Summary())
	}
	if len(fx.Actions) != 0 {
		t.Errorf("actions: %v", fx.Actions)
	}

	b, err := s.EncodeFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{runKey, "owner-secret-key", "0123abcd", "Tick-et_1"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("the capture keeps %q", secret)
		}
	}
	dir := t.TempDir()
	path, err := WriteFixture(dir, "live", b, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "live.json") {
		t.Errorf("path %s", path)
	}
	if _, err := WriteFixture(dir, "live", b, false); !errors.Is(err, ErrFixtureExists) {
		t.Errorf("a second capture without force: %v", err)
	}
	if _, err := WriteFixture(dir, "live", b, true); err != nil {
		t.Errorf("with force: %v", err)
	}
	loaded, err := LoadFixture(dir, "live")
	if err != nil {
		t.Fatal(err)
	}

	// The preview of the capture answers as the live UI does.
	_, c := startPreviewOf(t, dir, "live")
	same := func(path string, want any) {
		t.Helper()
		// Both through any, so they compare as the page reads them.
		var got, w any
		c.get(path, &got)
		wb, _ := json.Marshal(want)
		_ = json.Unmarshal(wb, &w)
		gb, _ := json.Marshal(got)
		wb, _ = json.Marshal(w)
		if string(gb) != string(wb) {
			t.Errorf("GET %s:\n got %s\nwant %s", path, gb, wb)
		}
	}
	inbox, err := s.ComputeInbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	same("/api/inbox", orEmpty(inbox))
	history, err := s.ComputeHistoryRuns(ctx, HistoryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	same("/api/runs?state=ended", history)
	milestones, err := s.ComputeMilestones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	same("/api/milestones", orEmpty(milestones))
	tl, err := s.ComputeIssueTimeline(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	same("/api/issues/42/timeline", tl)
	prTL, err := s.ComputeFlowTimeline(ctx, 0, 77)
	if err != nil {
		t.Fatal(err)
	}
	same("/api/issues/0/timeline?pr=77", prTL)
	done, _, _ := s.ComputeRunLog(ctx, "r-done")
	same("/api/runs/r-done/log", map[string]any{"id": "r-done", "lines": done})

	var st UIStatusResponse
	c.get("/api/status", &st)
	if st.Repo != "bottown/bottown" || len(st.LiveRuns) != 1 || st.LiveRuns[0].ID != "r-live" {
		t.Errorf("status: %+v", st)
	}
	var issue IssueDetails
	c.get("/api/issues/41", &issue)
	if issue.Title != "Done issue" || !strings.Contains(issue.Body, "?token=[elided by bottown]&x=1") ||
		!strings.Contains(issue.Body, "/v1/web/login/[elided by bottown]") {
		t.Errorf("issue #41: %+v", issue)
	}
	var log struct {
		Lines []runs.LogLine `json:"lines"`
	}
	c.get("/api/runs/r-live/log", &log)
	if len(log.Lines) != len(loaded.Logs["r-live"]) || !strings.Contains(string(b), "btk_[elided by bottown]") {
		t.Errorf("r-live log: %+v", log.Lines)
	}
}

// TestCaptureNamed takes only what it is asked for, and fails on what it
// cannot read.
func TestCaptureNamed(t *testing.T) {
	fix := defaultRunsFixture()
	now := time.Now().UTC()
	fix.issues = []*sdk.Issue{
		{Index: 5, Title: "Five", State: sdk.StateOpen, Poster: &sdk.User{UserName: "rlupi"}, Created: now},
	}
	fix.runs = []secrets.Run{
		{ID: "r-6", Kind: "implement", Actor: "agy", Repo: "bottown", Issue: 6,
			Created: now.Add(-10 * time.Minute), Expires: now.Add(50 * time.Minute)},
	}
	_, port := newRunsFixtureServer(t, fix)
	top := t.TempDir()
	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  top, FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	fx, _, err := s.Capture(ctx, CaptureOptions{Issues: []int64{5}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.Issues) != 1 || fx.Issues["5"].Title != "Five" || len(fx.Logs) != 0 {
		t.Errorf("named capture: %s", fx.Summary())
	}
	if _, _, err := s.Capture(ctx, CaptureOptions{Issues: []int64{99}}); err == nil {
		t.Error("a missing issue asked for by number should fail")
	}
	if _, _, err := s.Capture(ctx, CaptureOptions{Logs: []string{"r-6"}}); err == nil || !strings.Contains(err.Error(), "no log") {
		t.Errorf("a run without a log: %v", err)
	}
}
