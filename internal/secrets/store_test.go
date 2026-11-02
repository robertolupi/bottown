package secrets

import (
	"errors"
	"fmt"
	"strings"
	"sync"
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
	key, err := s.NewRun(Run{ID: "r-1", Kind: "implement", Actor: "claude", Created: now, Expires: now.Add(time.Hour)}, 1)
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
		if _, err := s.NewRun(r, 0); err != nil {
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
	if _, err := s.NewRun(Run{ID: "r-old", Created: now, Expires: now.Add(time.Hour)}, 0); err == nil {
		t.Fatal("NewRun reused a purged id")
	}
}

func TestEndRunDigest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.NewRun(Run{ID: "r-1", Created: now, Expires: now.Add(time.Hour)}, 1)
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

func TestSetOutcome(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.NewRun(Run{ID: "r-1", Created: now, Expires: now.Add(time.Hour)}, 1)
	if ok, err := s.SetOutcome("r-1", OutcomeCommented); !ok || err != nil {
		t.Fatalf("set outcome: %v %v", ok, err)
	}
	// A newer verdict replaces the older, and survives the run's end.
	s.SetOutcome("r-1", OutcomeChangesRequested)
	run, _, _ := s.EndRun("r-1", "succeeded", now, Digest{Summary: "done"})
	if run.Outcome != OutcomeChangesRequested {
		t.Fatalf("outcome after end: %q", run.Outcome)
	}
	// An ended or unknown run takes no outcome.
	if ok, _ := s.SetOutcome("r-1", OutcomeApproved); ok {
		t.Error("outcome set on an ended run")
	}
	if ok, _ := s.SetOutcome("r-2", OutcomeApproved); ok {
		t.Error("outcome set on an unknown run")
	}
	st, _ := s.Read()
	if st.Runs["r-1"].Outcome != OutcomeChangesRequested {
		t.Fatalf("stored outcome: %q", st.Runs["r-1"].Outcome)
	}
}

func TestBatchRunLimit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	newRun := func(id, actor string, interactive bool, expires time.Time, limit int) error {
		_, err := s.NewRun(Run{ID: id, Actor: actor, Interactive: interactive, Created: now, Expires: expires}, limit)
		return err
	}
	if err := newRun("r-live", "claude", false, now.Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	err = newRun("r-refused", "claude", false, now.Add(time.Hour), 1)
	var limit *RunLimitError
	if !errors.As(err, &limit) || limit.Actor != "claude" || len(limit.Runs) != 1 || limit.Runs[0] != "r-live" {
		t.Fatalf("second batch run: %+v", err)
	}
	st, readErr := s.Read()
	if readErr != nil || st.Runs["r-refused"].ID != "" {
		t.Fatalf("refused run was stored: %v", readErr)
	}
	if err := newRun("r-interactive", "claude", true, now.Add(time.Hour), 1); err != nil {
		t.Fatalf("interactive run: %v", err)
	}
	if err := newRun("r-other", "agy", false, now.Add(time.Hour), 1); err != nil {
		t.Fatalf("other actor: %v", err)
	}
	if err := newRun("r-unlimited", "claude", false, now.Add(time.Hour), 0); err != nil {
		t.Fatalf("unlimited run: %v", err)
	}
	if err := newRun("r-expired", "kimi", false, now.Add(-time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	if err := newRun("r-after-expiry", "kimi", false, now.Add(time.Hour), 1); err != nil {
		t.Fatalf("expired run counted as live: %v", err)
	}
	if _, ok, err := s.EndRun("r-live", "succeeded", now, Digest{}); err != nil || !ok {
		t.Fatalf("end live run: %v %v", ok, err)
	}
	if err := newRun("r-after-end", "claude", false, now.Add(time.Hour), 2); err != nil {
		t.Fatalf("ended run counted as live: %v", err)
	}
}

func TestBatchRunLimitAtomic(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const attempts = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			now := time.Now()
			_, err := s.NewRun(Run{ID: fmt.Sprintf("r-%d", i), Actor: "claude", Created: now, Expires: now.Add(time.Hour)}, 1)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var succeeded, refused int
	for err := range results {
		var limit *RunLimitError
		switch {
		case err == nil:
			succeeded++
		case errors.As(err, &limit):
			refused++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || refused != attempts-1 {
		t.Fatalf("concurrent creations: %d succeeded, %d refused", succeeded, refused)
	}
}
