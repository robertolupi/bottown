package secrets

import (
	"testing"
	"time"
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
	if _, ok, _ := s.EndRun("r-1", "succeeded", now); !ok {
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
	s.EndRun("r-old", "succeeded", now.Add(-2*time.Hour))
	s.EndRun("r-new", "failed", now.Add(-time.Minute))
	s.EndRun("r-other", "succeeded", now.Add(-2*time.Hour))

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
}
