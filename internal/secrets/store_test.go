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
	k2, _ := s.NewAPIKey("codex", "manual")
	if p, ok, _ := s.Lookup(k2, now); !ok || p.Run != nil {
		t.Fatalf("actor key = %+v %v", p, ok)
	}
}
