package secrets

import (
	"errors"
	"fmt"
	"slices"
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
	key, err := s.NewRun(Run{ID: "r-1", Kind: "implement", Actor: "claude", Created: now, Expires: now.Add(time.Hour)}, false)
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
		if _, err := s.NewRun(r, false); err != nil {
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
	if _, err := s.NewRun(Run{ID: "r-old", Created: now, Expires: now.Add(time.Hour)}, false); err == nil {
		t.Fatal("NewRun reused a purged id")
	}
}

func TestTruncateUTF8(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"abcdefgh", 6, "abc…"},
		{"ééééé", 6, "é…"},              // "é" is 2 bytes: 3 bytes left, cut back to 2
		{"aéééé", 7, "aé…"},             // cut inside the second é
		{"\xffabcdefgh", 7, "\xffabc…"}, // invalid bytes earlier do not empty it
	} {
		got := TruncateUTF8(c.s, c.n)
		if got != c.want || len(got) > max(c.n, len(c.want)) {
			t.Errorf("TruncateUTF8(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

func TestEndRunDigest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.NewRun(Run{ID: "r-1", Created: now, Expires: now.Add(time.Hour)}, false)
	long := strings.Repeat("é", MaxSummary)
	failure := &Failure{Cause: FailureStartup, Detail: long}
	run, _, _ := s.EndRun("r-1", "failed", now, Digest{Summary: long, Failure: failure})
	if len(run.Summary) > MaxSummary || !utf8.ValidString(run.Summary) || !strings.HasSuffix(run.Summary, "…") {
		t.Fatalf("summary not cut cleanly: %d bytes", len(run.Summary))
	}
	if run.Failure == nil || run.Failure.Cause != FailureStartup || len(run.Failure.Detail) > MaxFailureDetail || !utf8.ValidString(run.Failure.Detail) {
		t.Fatalf("failure not kept or not cut cleanly: %+v", run.Failure)
	}
	if failure.Detail != long {
		t.Fatal("EndRun changed the caller's failure")
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
	s.NewRun(Run{ID: "r-1", Created: now, Expires: now.Add(time.Hour)}, false)
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

// Batch runs by one actor on different targets run concurrently, unless a
// run asks for its actor to be serialised.
func TestSerializeActor(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	issue := int64(0)
	newRun := func(id, actor string, interactive bool, expires time.Time, serialize bool) error {
		issue++
		_, err := s.NewRun(Run{ID: id, Kind: "implement", Repo: "demo", Issue: issue, Actor: actor, Interactive: interactive, Created: now, Expires: expires}, serialize)
		return err
	}
	if err := newRun("r-live", "claude", false, now.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := newRun("r-concurrent", "claude", false, now.Add(time.Hour), false); err != nil {
		t.Fatalf("second batch run on another issue: %v", err)
	}
	err = newRun("r-refused", "claude", false, now.Add(time.Hour), true)
	var busy *RunBusyError
	if !errors.As(err, &busy) || busy.Actor != "claude" || !slices.Equal(busy.Runs, []string{"r-concurrent", "r-live"}) ||
		!strings.Contains(err.Error(), ActorBusyMessage) {
		t.Fatalf("serialised batch run: %+v", err)
	}
	st, readErr := s.Read()
	if readErr != nil || st.Runs["r-refused"].ID != "" {
		t.Fatalf("refused run was stored: %v", readErr)
	}
	if err := newRun("r-interactive", "claude", true, now.Add(time.Hour), true); err != nil {
		t.Fatalf("interactive run: %v", err)
	}
	if err := newRun("r-other", "agy", false, now.Add(time.Hour), true); err != nil {
		t.Fatalf("other actor: %v", err)
	}
	if err := newRun("r-expired", "kimi", false, now.Add(-time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := newRun("r-after-expiry", "kimi", false, now.Add(time.Hour), true); err != nil {
		t.Fatalf("expired run counted as live: %v", err)
	}
	for _, id := range []string{"r-live", "r-concurrent"} {
		if _, ok, err := s.EndRun(id, "succeeded", now, Digest{}); err != nil || !ok {
			t.Fatalf("end %s: %v %v", id, ok, err)
		}
	}
	// The interactive run does not hold the slot.
	if err := newRun("r-after-end", "claude", false, now.Add(time.Hour), true); err != nil {
		t.Fatalf("ended run counted as live: %v", err)
	}
}

// A second live run on the same target is refused, whoever runs it and
// whether or not it serialises its actor.
func TestSameTargetRefused(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	live := func(r Run) Run {
		r.Org, r.Created, r.Expires = "bottown", now, now.Add(time.Hour)
		if r.Repo == "" {
			r.Repo = "demo"
		}
		return r
	}
	for _, r := range []Run{
		{ID: "r-impl", Kind: "implement", Actor: "claude", Issue: 4},
		{ID: "r-verify", Kind: "verify", Actor: "codex", PR: 9, SHA: "abc"},
		{ID: "r-discuss", Kind: "discuss", Actor: "kimi", Issue: 5},
	} {
		if _, err := s.NewRun(live(r), false); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		run     Run
		refused bool
	}{
		{Run{ID: "r-impl-again", Kind: "implement", Actor: "agy", Issue: 4}, true},
		{Run{ID: "r-plan-same", Kind: "plan", Actor: "agy", Issue: 4, Interactive: true}, true},
		{Run{ID: "r-verify-again", Kind: "verify", Actor: "agy", PR: 9, SHA: "abc"}, true},
		{Run{ID: "r-impl-other-repo", Kind: "implement", Actor: "agy", Repo: "other", Issue: 4}, false},
		{Run{ID: "r-impl-other-issue", Kind: "implement", Actor: "claude", Issue: 6}, false},
		{Run{ID: "r-verify-new-head", Kind: "verify", Actor: "agy", PR: 9, SHA: "def"}, false},
		{Run{ID: "r-discuss-impl", Kind: "discuss", Actor: "agy", Issue: 4}, false},
		{Run{ID: "r-discuss-again", Kind: "discuss", Actor: "agy", Issue: 5}, false},
	} {
		_, err := s.NewRun(live(c.run), false)
		var busy *RunBusyError
		switch {
		case c.refused && (!errors.As(err, &busy) || busy.Actor != "" || len(busy.Runs) != 1):
			t.Errorf("%s: %v, want a target refusal", c.run.ID, err)
		case !c.refused && err != nil:
			t.Errorf("%s: %v", c.run.ID, err)
		}
	}
	if _, _, err := s.EndRun("r-impl", "succeeded", now, Digest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewRun(live(Run{ID: "r-impl-after-end", Kind: "implement", Actor: "agy", Issue: 4}), false); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
}

func TestSerializeActorAtomic(t *testing.T) {
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
			_, err := s.NewRun(Run{ID: fmt.Sprintf("r-%d", i), Kind: "implement", Issue: int64(i), Actor: "claude", Created: now, Expires: now.Add(time.Hour)}, true)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var succeeded, refused int
	for err := range results {
		var busy *RunBusyError
		switch {
		case err == nil:
			succeeded++
		case errors.As(err, &busy):
			refused++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || refused != attempts-1 {
		t.Fatalf("concurrent creations: %d succeeded, %d refused", succeeded, refused)
	}
}

func TestRunState(t *testing.T) {
	now := time.Now()
	ended := now.Add(-time.Minute)
	for _, c := range []struct {
		run  Run
		want string
	}{
		{Run{Expires: now.Add(time.Hour)}, RunActive},
		{Run{Expires: now.Add(-time.Second)}, RunExpired},
		{Run{}, RunExpired}, // no expiry: its key is not usable
		{Run{Expires: now.Add(time.Hour), Ended: &ended}, RunEnded},
		{Run{Expires: now.Add(-time.Hour), Ended: &ended}, RunEnded},
	} {
		if got := c.run.State(now); got != c.want {
			t.Errorf("%+v: State = %s, want %s", c.run, got, c.want)
		}
	}
}
