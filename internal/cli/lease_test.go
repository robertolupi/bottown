package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

type leaseFixture struct {
	a       *agentCtx
	rec     runs.Record
	remote  secrets.Run
	failEnd bool
	ends    int
}

func newLeaseFixture(t *testing.T) *leaseFixture {
	t.Helper()
	t.Setenv("BOTTOWN_RUN_FILE", "")
	dir := t.TempDir()
	if _, err := runs.Git(dir, nil, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Git(dir, nil, "config", "extensions.worktreeConfig", "true"); err != nil {
		t.Fatal(err)
	}
	f := &leaseFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer actor-key" {
			t.Error("recovery used run credential")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/v1/runs/r-test" {
			t.Errorf("unexpected target: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.Method == "DELETE" {
			f.ends++
			if f.failEnd {
				w.WriteHeader(503)
				return
			}
			var end server.EndRunRequest
			if err := json.NewDecoder(r.Body).Decode(&end); err != nil {
				t.Error(err)
			}
			if f.remote.Ended == nil {
				now := time.Now()
				f.remote.Ended = &now
				f.remote.Status = end.Status
			}
		}
		_ = json.NewEncoder(w).Encode(f.remote)
	}))
	t.Cleanup(srv.Close)
	keyDir := filepath.Join(dir, ".git", "bottown")
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	f.a = &agentCtx{Actor: "codex", Worktree: dir, KeyDir: keyDir, ServerURL: srv.URL, Key: "run-key", RunID: "r-test"}
	f.rec = runs.Record{ID: "r-test", Actor: "codex", Worktree: dir, ServerURL: srv.URL, Key: "run-key", Status: runs.StatusPreparing}
	f.remote = secrets.Run{ID: "r-test", Actor: "codex"}
	if err := saveRecord(filepath.Join(keyDir, "actor.json"), runs.Record{ID: "actor", Actor: "codex", ServerURL: srv.URL, Key: "actor-key"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLeaseSetupRollback(t *testing.T) {
	for _, stage := range []string{"record", "config", "context", "prompt"} {
		t.Run(stage, func(t *testing.T) {
			f := newLeaseFixture(t)
			boom := errors.New("injected " + stage + " failure")
			err := prepareLease(context.Background(), f.a, f.rec, func() error {
				if stage != "record" {
					if _, err := runs.Git(f.a.Worktree, nil, "config", "--worktree", gitRunKey, f.rec.ID); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "prompt" {
					if err := os.WriteFile(filepath.Join(f.a.KeyDir, "prompt.md"), []byte("prepared"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return boom
			})
			if !errors.Is(err, boom) {
				t.Fatalf("error = %v", err)
			}
			rec, err := runs.LoadFile(filepath.Join(f.a.KeyDir, "run.json"))
			if err != nil || rec.Key != "" || rec.Ended == nil || rec.Status != "revoked" {
				t.Fatalf("record = %+v, %v", rec, err)
			}
			if id, _ := runs.Git(f.a.Worktree, nil, "config", "--worktree", "--get", gitRunKey); id != "" {
				t.Fatalf("run config = %s", id)
			}
			if f.ends != 1 {
				t.Fatalf("ends = %d", f.ends)
			}
		})
	}
}

func TestLeaseRollbackFailureCanRecover(t *testing.T) {
	f := newLeaseFixture(t)
	f.failEnd = true
	err := prepareLease(context.Background(), f.a, f.rec, func() error { return errors.New("setup failed") })
	if err == nil || !strings.Contains(err.Error(), "rollback run r-test") {
		t.Fatalf("error = %v", err)
	}
	rec, err := runs.LoadFile(filepath.Join(f.a.KeyDir, "run.json"))
	if err != nil || rec.Key == "" || rec.Status != runs.StatusPreparing {
		t.Fatalf("lost recovery identity: %+v, %v", rec, err)
	}
	f.failEnd = false
	if recovered, err := recoverLease(context.Background(), f.a); err != nil || !recovered {
		t.Fatalf("recovery = %v, %v", recovered, err)
	}
}

func TestLeaseEndLocalFailureCanRecover(t *testing.T) {
	for _, stage := range []string{"record", "config"} {
		t.Run(stage, func(t *testing.T) {
			f := newLeaseFixture(t)
			f.rec.Status = runs.StatusRunning
			if err := saveRecord(filepath.Join(f.a.KeyDir, "run.json"), f.rec); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.Git(f.a.Worktree, nil, "config", "--worktree", gitRunKey, f.rec.ID); err != nil {
				t.Fatal(err)
			}
			obstruction := filepath.Join(f.a.KeyDir, "r-test.lock")
			if stage == "config" {
				obstruction = filepath.Join(f.a.Worktree, ".git", "config.worktree.lock")
			}
			if stage == "record" {
				if err := os.Remove(obstruction); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(obstruction, 0700); err != nil {
				t.Fatal(err)
			}
			if err := endLease(context.Background(), f.a, f.rec, server.EndRunRequest{Status: runs.StatusSucceeded}); err == nil {
				t.Fatal("expected local persistence failure")
			}
			if f.remote.Ended == nil {
				t.Fatal("server key not revoked")
			}
			if err := os.Remove(obstruction); err != nil {
				t.Fatal(err)
			}
			if recovered, err := recoverLease(context.Background(), f.a); err != nil || !recovered {
				t.Fatalf("recovery = %v, %v", recovered, err)
			}
			if f.ends != 1 {
				t.Fatalf("recovery ended run again: %d", f.ends)
			}
			rec, err := runs.LoadFile(filepath.Join(f.a.KeyDir, "run.json"))
			if err != nil || rec.Key != "" || rec.Status != runs.StatusSucceeded {
				t.Fatalf("record = %+v, %v", rec, err)
			}
		})
	}
}

func TestLeaseRecoveryRefusesDifferentRun(t *testing.T) {
	f := newLeaseFixture(t)
	if err := saveRecord(filepath.Join(f.a.KeyDir, "run.json"), f.rec); err != nil {
		t.Fatal(err)
	}
	f.remote.ID = "r-other"
	if _, err := recoverLease(context.Background(), f.a); err == nil {
		t.Fatal("accepted different server run")
	}
	if f.ends != 0 {
		t.Fatal("ended a different run")
	}
}

func TestLeaseSetupSuccessKeepsRun(t *testing.T) {
	f := newLeaseFixture(t)
	if err := prepareLease(context.Background(), f.a, f.rec, func() error {
		f.rec.Status = runs.StatusRunning
		return saveRecord(filepath.Join(f.a.KeyDir, "run.json"), f.rec)
	}); err != nil {
		t.Fatal(err)
	}
	if recovered, err := recoverLease(context.Background(), f.a); err != nil || recovered {
		t.Fatalf("live run recovery = %v, %v", recovered, err)
	}
	if f.ends != 0 {
		t.Fatal("ended successful setup")
	}
	if err := endLease(context.Background(), f.a, f.rec, server.EndRunRequest{Status: runs.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseInterruptedStartupRecovery(t *testing.T) {
	f := newLeaseFixture(t)
	// Simulate process death: the preparing record survives without deferred rollback.
	if err := saveRecord(filepath.Join(f.a.KeyDir, "run.json"), f.rec); err != nil {
		t.Fatal(err)
	}
	if recovered, err := recoverLease(context.Background(), f.a); err != nil || !recovered {
		t.Fatalf("recovery = %v, %v", recovered, err)
	}
	if f.a.Key != "actor-key" || f.a.RunID != "" {
		t.Fatal("recovered context still selects the revoked run key")
	}
	if f.remote.Status != "revoked" || f.ends != 1 {
		t.Fatalf("remote = %+v, ends = %d", f.remote, f.ends)
	}
}

func TestLeaseRecoveryPreservesDifferentWorktreeRun(t *testing.T) {
	f := newLeaseFixture(t)
	if err := saveRecord(filepath.Join(f.a.KeyDir, "run.json"), f.rec); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f.remote.Ended, f.remote.Status = &now, "revoked"
	if _, err := runs.Git(f.a.Worktree, nil, "config", "--worktree", gitRunKey, "r-other"); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverLease(context.Background(), f.a); err == nil {
		t.Fatal("accepted different worktree run")
	}
	if id, _ := runs.Git(f.a.Worktree, nil, "config", "--worktree", "--get", gitRunKey); id != "r-other" {
		t.Fatalf("changed other run: %q", id)
	}
	if f.ends != 0 {
		t.Fatal("issued revocation during mismatched recovery")
	}
}

// A leased run's attachments end with it: on run end, on recovery of an
// interrupted startup, and on a failed setup's rollback.
func TestLeaseRemovesAttachments(t *testing.T) {
	fill := func(t *testing.T, f *leaseFixture) string {
		t.Helper()
		dir := leaseAttachments(f.a)
		if dir != filepath.Join(f.a.KeyDir, "attachments") {
			t.Fatalf("attachments dir %s", dir)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "1-shot.png"), []byte("png"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	gone := func(t *testing.T, dir string) {
		t.Helper()
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("attachments survive: %v", err)
		}
	}
	t.Run("end", func(t *testing.T) {
		f := newLeaseFixture(t)
		dir := fill(t, f)
		if err := endLease(context.Background(), f.a, f.rec, server.EndRunRequest{Status: runs.StatusSucceeded}); err != nil {
			t.Fatal(err)
		}
		gone(t, dir)
	})
	t.Run("interrupted startup", func(t *testing.T) {
		f := newLeaseFixture(t)
		dir := fill(t, f)
		if err := saveRecord(filepath.Join(f.a.KeyDir, "run.json"), f.rec); err != nil {
			t.Fatal(err)
		}
		if recovered, err := recoverLease(context.Background(), f.a); err != nil || !recovered {
			t.Fatalf("recovery = %v, %v", recovered, err)
		}
		gone(t, dir)
	})
	t.Run("rollback", func(t *testing.T) {
		f := newLeaseFixture(t)
		var dir string
		if err := prepareLease(context.Background(), f.a, f.rec, func() error {
			dir = fill(t, f)
			return errors.New("prompt failed")
		}); err == nil {
			t.Fatal("expected setup failure")
		}
		gone(t, dir)
	})
	t.Run("removal failure is retried", func(t *testing.T) {
		f := newLeaseFixture(t)
		dir := fill(t, f)
		if err := saveRecord(filepath.Join(f.a.KeyDir, "run.json"), f.rec); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := endLease(context.Background(), f.a, f.rec, server.EndRunRequest{Status: runs.StatusSucceeded}); err == nil {
			t.Fatal("expected cleanup failure")
		}
		if rec, err := runs.LoadFile(filepath.Join(f.a.KeyDir, "run.json")); err != nil || rec.Key == "" {
			t.Fatalf("record finalized before cleanup: %+v %v", rec, err)
		}
		_ = os.Chmod(dir, 0o700)
		if recovered, err := recoverLease(context.Background(), f.a); err != nil || !recovered {
			t.Fatalf("recovery = %v, %v", recovered, err)
		}
		gone(t, dir)
	})
}

func TestRunEndRetryAfterCleanup(t *testing.T) {
	f := newLeaseFixture(t)
	if _, err := runs.Git(f.a.Worktree, nil, "config", "--worktree", gitActorKey, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := saveRecord(filepath.Join(f.a.KeyDir, "run.json"), f.rec); err != nil {
		t.Fatal(err)
	}
	if err := endLease(context.Background(), f.a, f.rec, server.EndRunRequest{Status: runs.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(f.a.Worktree)
	cmd := newRunEnd()
	var output strings.Builder
	cmd.SetOut(&output)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "already ended") || f.ends != 1 {
		t.Fatalf("retry: %q, ends = %d", output.String(), f.ends)
	}
}
