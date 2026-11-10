package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeMilestoneRunRecord(t *testing.T, top string, rec MilestoneRunRecord) {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(top, ".bottown"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(top, ".bottown", "milestone-"+rec.Slug+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestActions_MilestoneRunStaleRecord: a record whose process is gone is
// not running, and is removed.
func TestActions_MilestoneRunStaleRecord(t *testing.T) {
	top := t.TempDir()
	s, err := NewServer(Config{RepoTop: top})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	writeMilestoneRunRecord(t, top, MilestoneRunRecord{Slug: "demo", Agents: "claude=*", Parallel: 1, PID: 1 << 30})
	if rec := s.getMilestoneRun("demo"); rec != nil {
		t.Fatalf("stale record read as running: %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(top, ".bottown", "milestone-demo.json")); !os.IsNotExist(err) {
		t.Fatalf("stale record kept: %v", err)
	}
}
