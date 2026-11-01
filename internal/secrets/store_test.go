package secrets

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRunKeyLifecycle(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	key, err := s.NewRun(Run{ID: "r-1", Kind: "implement", Actor: "claude", Created: now, Expires: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p, ok, _ := s.Lookup(key, now)
	if !ok || p.Actor != "claude" || p.Run == nil || p.Run.ID != "r-1" {
		t.Fatalf("lookup = %+v %v", p, ok)
	}
	if _, ok, _ := s.Lookup(key, now.Add(2*time.Hour)); ok {
		t.Fatal("expired run key still resolves")
	}
	if _, ok, _ := s.EndRun("r-1", "succeeded", now, Digest{}); !ok {
		t.Fatal("EndRun: no run")
	}
	if _, ok, _ := s.Lookup(key, now); ok {
		t.Fatal("ended run key still resolves")
	}
	st, _ := s.Read()
	if st.Runs["r-1"].Status != "succeeded" || len(st.Keys) != 0 {
		t.Fatalf("after end: %+v keys=%d", st.Runs["r-1"], len(st.Keys))
	}
	// A plain actor key has no run.
	k2, _ := s.NewAPIKey("codex", "manual", "")
	if p, ok, _ := s.Lookup(k2, now); !ok || p.Run != nil || p.ReadOnly {
		t.Fatalf("full actor key = %+v %v", p, ok)
	}
	k3, _ := s.NewAPIKey("codex", "worktree", ScopeRead)
	if p, ok, _ := s.Lookup(k3, now); !ok || !p.ReadOnly {
		t.Fatalf("read actor key = %+v %v", p, ok)
	}
}

func TestPurgeRuns(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, r := range []Run{
		{ID: "r-old", Repo: "demo", Created: now.Add(-3 * time.Hour), Expires: now.Add(time.Hour)},
		{ID: "r-new", Repo: "demo", Created: now.Add(-time.Hour), Expires: now.Add(time.Hour)},
		{ID: "r-other", Repo: "other", Created: now.Add(-3 * time.Hour), Expires: now.Add(time.Hour)},
		{ID: "r-live", Repo: "demo", Created: now, Expires: now.Add(time.Hour)},
		{ID: "r-stuck", Repo: "demo", Created: now.Add(-5 * time.Hour), Expires: now.Add(-time.Hour)},
	} {
		if _, err := s.NewRun(r); err != nil {
			t.Fatal(err)
		}
	}
	cost := 1.25
	s.EndRun("r-old", "succeeded", now.Add(-2*time.Hour), Digest{Summary: "old done", Usage: &Usage{CostUSD: &cost}})
	s.EndRun("r-new", "failed", now.Add(-time.Minute), Digest{})
	s.EndRun("r-other", "succeeded", now.Add(-2*time.Hour), Digest{})

	// Even a match-everything purge leaves runs that have not ended.
	ids, err := s.PurgeRuns(func(r Run) bool { return r.Repo == "demo" && r.Ended.Before(now.Add(-time.Hour)) })
	if err != nil || len(ids) != 1 || ids[0] != "r-old" {
		t.Fatalf("purge old demo runs = %v, %v", ids, err)
	}
	ids, _ = s.PurgeRuns(func(Run) bool { return true })
	if len(ids) != 2 || ids[0] != "r-new" || ids[1] != "r-other" {
		t.Fatalf("purge all = %v", ids)
	}
	st, _ := s.Read()
	if len(st.Runs) != 2 || st.Runs["r-live"].ID == "" || st.Runs["r-stuck"].ID == "" {
		t.Fatalf("left: %+v", st.Runs)
	}
	// The live run's key survives; ended runs had theirs deleted by EndRun.
	if len(st.Keys) != 2 {
		t.Fatalf("keys left = %d, want 2 (r-live, r-stuck)", len(st.Keys))
	}
	// Purged runs leave their digests.
	old := st.History["r-old"]
	if len(st.History) != 3 || !old.Purged || old.Status != "succeeded" || old.Summary != "old done" || *old.Usage.CostUSD != 1.25 {
		t.Fatalf("history: %+v", st.History)
	}
	// A purged run's id is not reused.
	if _, err := s.NewRun(Run{ID: "r-old", Created: now, Expires: now.Add(time.Hour)}); err == nil {
		t.Fatal("NewRun reused a purged id")
	}
}

func TestEndRunDigest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.NewRun(Run{ID: "r-1", Created: now, Expires: now.Add(time.Hour)})
	long := strings.Repeat("é", MaxSummary)
	run, _, _ := s.EndRun("r-1", "failed", now, Digest{Summary: long})
	if len(run.Summary) > MaxSummary || !utf8.ValidString(run.Summary) || !strings.HasSuffix(run.Summary, "…") {
		t.Fatalf("summary not cut cleanly: %d bytes", len(run.Summary))
	}
	// A second end changes neither status nor digest.
	run, _, _ = s.EndRun("r-1", "revoked", now.Add(time.Minute), Digest{Summary: "later"})
	if run.Status != "failed" || run.Summary == "later" {
		t.Fatalf("second end: %+v", run)
	}
}
