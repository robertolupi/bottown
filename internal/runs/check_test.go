package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveChecks(t *testing.T) {
	dir := t.TempDir()
	if got := LiveChecks(dir); len(got) != 0 {
		t.Fatalf("no checks: %+v", got)
	}
	started := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	newer := CheckRecord{ID: "flow-12-bbbbbbbbbb.e2e", Repo: "bottown", Issue: 12, PR: 30, SHA: "bbbb", Commands: []string{"make e2e"}, Started: started.Add(time.Minute)}
	older := CheckRecord{ID: "flow-7-aaaaaaaaaa.e2e", Issue: 7, PR: 9, SHA: "aaaa", Started: started, Expires: started.Add(time.Hour)}
	releaseNewer, err := HoldCheck(dir, newer)
	if err != nil {
		t.Fatal(err)
	}
	releaseOlder, err := HoldCheck(dir, older)
	if err != nil {
		t.Fatal(err)
	}

	// A record nobody holds is a check whose flow died: not live, and gone.
	stale := filepath.Join(dir, "flow-3-cccccccccc.e2e"+CheckRecordSuffix)
	if err := os.WriteFile(stale, []byte(`{"id":"flow-3-cccccccccc.e2e"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := LiveChecks(dir)
	if len(got) != 2 || got[0].ID != older.ID || got[1].ID != newer.ID {
		t.Fatalf("live checks, oldest first: %+v", got)
	}
	if got[0].Expires != older.Expires || got[1].Commands[0] != "make e2e" || got[1].PR != 30 {
		t.Errorf("records round-trip: %+v", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale record kept: %v", err)
	}

	releaseOlder()
	if got := LiveChecks(dir); len(got) != 1 || got[0].ID != newer.ID {
		t.Errorf("after release: %+v", got)
	}
	releaseNewer()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("release leaves nothing behind: %v", entries)
	}
}
