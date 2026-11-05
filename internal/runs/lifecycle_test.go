package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
)

func TestStore(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "run.json")}
	if got, want := s.LockPath(), strings.TrimSuffix(s.Path, ".json")+".lock"; got != want {
		t.Errorf("LockPath = %s, want %s", got, want)
	}
	if err := s.Create(Record{ID: "r-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(Record{ID: "r-2"}); !errors.Is(err, os.ErrExist) {
		t.Errorf("Create over a record: %v, want ErrExist", err)
	}
	// Save replaces whatever is there: a worktree's next run.
	if err := s.Save(Record{ID: "r-2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(r *Record) error { r.Status = StatusRunning; return nil }); err != nil {
		t.Fatal(err)
	}
	r, err := s.Load()
	if err != nil || r.ID != "r-2" || r.Status != StatusRunning {
		t.Fatalf("Load = %+v, %v", r, err)
	}
	if fi, err := os.Stat(s.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode: %v, %v", fi, err)
	}
	ents, _ := os.ReadDir(filepath.Dir(s.Path))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "run.json run.lock" {
		t.Errorf("files = %v, want run.json and its lock only", names)
	}
}

// fakeServer counts the ends of one run, and fails them while down.
type fakeServer struct {
	run    secrets.Run
	down   bool
	ends   []string
	digest secrets.Digest
}

func (f *fakeServer) end(_ context.Context, id, status string, d secrets.Digest) (secrets.Run, error) {
	f.ends = append(f.ends, status)
	if f.down {
		return secrets.Run{}, errors.New("connection refused")
	}
	if id != f.run.ID {
		return secrets.Run{}, errors.New("no such run " + id)
	}
	if f.run.Ended == nil {
		now := time.Now()
		f.run.Ended, f.run.Status, f.digest = &now, status, d
	}
	return f.run, nil
}

func (f *fakeServer) get(_ context.Context, id string) (secrets.Run, error) {
	if f.down {
		return secrets.Run{}, errors.New("connection refused")
	}
	return f.run, nil
}

func newLifecycle(t *testing.T, mode Mode) (*Lifecycle, *fakeServer) {
	t.Helper()
	srv := &fakeServer{run: secrets.Run{ID: "r-1", Actor: "codex"}}
	l := &Lifecycle{Mode: mode, Store: Store{Path: filepath.Join(t.TempDir(), "run.json")}, EndRun: srv.end, GetRun: srv.get}
	return l, srv
}

func fastBackoff(t *testing.T) {
	b := EndBackoff
	EndBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() { EndBackoff = b })
}

// A launched run whose setup fails ends as a startup failure, once, with
// its record final; the launcher's deferred End then does nothing.
func TestLaunchedSetupFailure(t *testing.T) {
	l, srv := newLifecycle(t, Launched)
	boom := errors.New("no worktree")
	err := l.Prepare(context.Background(), Record{ID: "r-1", Actor: "codex", Key: "k"}, func() error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("Prepare = %v", err)
	}
	if err := l.End(context.Background(), StatusFailed, secrets.Digest{}); err != nil {
		t.Fatal(err)
	}
	if len(srv.ends) != 1 || srv.ends[0] != StatusFailed {
		t.Errorf("ends = %v, want one failed", srv.ends)
	}
	if f := srv.digest.Failure; f == nil || f.Cause != secrets.FailureStartup || f.Detail != boom.Error() {
		t.Errorf("failure = %+v", f)
	}
	r, err := l.Store.Load()
	if err != nil || r.Status != StatusFailed || r.Ended == nil || r.Key != "" {
		t.Errorf("record = %+v, %v", r, err)
	}
}

// A launched run that fails after it was running (its harness could not
// start, say) still gets a final record; one never saved gets none.
func TestLaunchedEnd(t *testing.T) {
	l, srv := newLifecycle(t, Launched)
	if err := l.Prepare(context.Background(), Record{ID: "r-1", Actor: "codex", Key: "k"}, func() error {
		return l.Running(func(r *Record) { r.Worktree = "/wt" })
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.End(context.Background(), StatusFailed, secrets.Digest{}); err != nil {
		t.Fatal(err)
	}
	r, _ := l.Store.Load()
	if r.Status != StatusFailed || r.Ended == nil || r.Key != "" || r.Worktree != "/wt" || len(srv.ends) != 1 {
		t.Errorf("record = %+v, ends = %v", r, srv.ends)
	}

	l, srv = newLifecycle(t, Launched)
	l.Resume(Record{ID: "r-1"})
	if err := l.End(context.Background(), StatusFailed, secrets.Digest{}); err != nil || len(srv.ends) != 1 {
		t.Errorf("End before Prepare = %v, ends = %v", err, srv.ends)
	}
	if _, err := os.Stat(l.Store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("End before Prepare wrote a record: %v", err)
	}
}

// A launched run whose server end fails keeps its finished record, and
// says gc revokes it; it is not retried again.
func TestLaunchedEndFailure(t *testing.T) {
	fastBackoff(t)
	l, srv := newLifecycle(t, Launched)
	if err := l.Prepare(context.Background(), Record{ID: "r-1", Actor: "codex", Key: "k"}, func() error { return l.Running(nil) }); err != nil {
		t.Fatal(err)
	}
	code := 0
	if _, err := l.Finished(func(r *Record) { r.Status, r.ExitCode = StatusSucceeded, &code }); err != nil {
		t.Fatal(err)
	}
	srv.down = true
	err := l.End(context.Background(), StatusSucceeded, secrets.Digest{})
	if err == nil || !strings.Contains(err.Error(), "run gc") {
		t.Fatalf("End = %v", err)
	}
	n := len(srv.ends)
	if err := l.End(context.Background(), StatusSucceeded, secrets.Digest{}); err != nil || len(srv.ends) != n {
		t.Errorf("second End = %v, ends %d -> %d", err, n, len(srv.ends))
	}
	r, _ := l.Store.Load()
	if r.Status != StatusSucceeded || r.Ended == nil || r.Key != "" || r.ExitCode == nil {
		t.Errorf("record = %+v", r)
	}
}

// A leased run's record keeps its key until the server has ended the run
// and Release has run; Recover then finishes it without ending it again.
func TestLeasedEnd(t *testing.T) {
	l, srv := newLifecycle(t, Leased)
	released := errors.New("worktree busy")
	l.Release = func(Record) error { return released }
	var final *Record
	l.Ended = func(r Record) { final = &r }
	if err := l.Prepare(context.Background(), Record{ID: "r-1", Actor: "codex", Key: "k"}, func() error { return l.Running(nil) }); err != nil {
		t.Fatal(err)
	}
	srv.down = true
	if err := l.End(context.Background(), StatusSucceeded, secrets.Digest{}); err == nil {
		t.Fatal("End succeeded on a server that is down")
	}
	srv.down = false
	if err := l.End(context.Background(), StatusSucceeded, secrets.Digest{}); !errors.Is(err, released) {
		t.Fatalf("End = %v, want the release failure", err)
	}
	r, _ := l.Store.Load()
	if r.Key == "" || r.Ended != nil || final != nil {
		t.Fatalf("record finalized before release: %+v", r)
	}

	l2 := &Lifecycle{Mode: Leased, Store: l.Store, EndRun: srv.end, GetRun: srv.get, Release: func(Record) error { return nil }}
	recovered, err := l2.Recover(context.Background(), r)
	if err != nil || !recovered {
		t.Fatalf("Recover = %v, %v", recovered, err)
	}
	if len(srv.ends) != 2 {
		t.Errorf("Recover ended the run again: %v", srv.ends)
	}
	r, _ = l.Store.Load()
	if r.Key != "" || r.Ended == nil || r.Status != StatusSucceeded {
		t.Errorf("record = %+v", r)
	}
}

func TestLeasedRecover(t *testing.T) {
	t.Run("interrupted startup is revoked", func(t *testing.T) {
		l, srv := newLifecycle(t, Leased)
		rec := Record{ID: "r-1", Actor: "codex", Key: "k", Status: StatusPreparing}
		if recovered, err := l.Recover(context.Background(), rec); err != nil || !recovered {
			t.Fatalf("Recover = %v, %v", recovered, err)
		}
		if srv.run.Status != "revoked" || srv.digest.Failure == nil || srv.digest.Failure.Cause != secrets.FailureStartup {
			t.Errorf("server run = %+v, digest %+v", srv.run, srv.digest)
		}
	})
	t.Run("running run is left alone", func(t *testing.T) {
		l, srv := newLifecycle(t, Leased)
		rec := Record{ID: "r-1", Actor: "codex", Key: "k", Status: StatusRunning}
		if recovered, err := l.Recover(context.Background(), rec); err != nil || recovered || len(srv.ends) != 0 {
			t.Errorf("Recover = %v, %v; ends %v", recovered, err, srv.ends)
		}
	})
	t.Run("another server run is refused", func(t *testing.T) {
		l, srv := newLifecycle(t, Leased)
		srv.run.ID = "r-2"
		rec := Record{ID: "r-1", Actor: "codex", Key: "k", Status: StatusPreparing}
		if _, err := l.Recover(context.Background(), rec); err == nil || len(srv.ends) != 0 {
			t.Errorf("Recover = %v; ends %v", err, srv.ends)
		}
	})
}
