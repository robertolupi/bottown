package runs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLauncherAlive(t *testing.T) {
	if !(Record{PID: os.Getpid()}).LauncherAlive() {
		t.Error("own process reported dead")
	}
	if (Record{}).LauncherAlive() {
		t.Error("record without a launcher pid reported alive")
	}
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if (Record{PID: c.Process.Pid}).LauncherAlive() {
		t.Errorf("exited launcher %d reported alive", c.Process.Pid)
	}
}

// A run that records its launcher's lock is alive exactly while the lock is
// held, even when its pid names a live process (a reused one, say).
func TestLauncherAliveByLock(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	path, release, err := w.HoldLauncher("r-1")
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{ID: "r-1", PID: os.Getpid(), LauncherLock: path}
	if !rec.LauncherAlive() {
		t.Error("run whose launcher holds its lock reported dead")
	}
	if _, _, err := w.HoldLauncher("r-1"); err == nil {
		t.Error("second HoldLauncher took a held lock")
	}
	release()
	if rec.LauncherAlive() {
		t.Error("run whose launcher lock is free reported alive (reused pid)")
	}
	w.RemoveFiles("r-1")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("RemoveFiles left %s: %v", path, err)
	}
	if rec.LauncherAlive() {
		t.Error("run whose launcher lock is gone reported alive")
	}
}

func TestProcessAliveEPERM(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root may signal pid 1")
	}
	if !ProcessAlive(1) {
		t.Error("pid 1, another user's process, reported dead")
	}
}

// The child holds the OS lock while the parent attempts another update.
func TestRecordUpdateProcess(t *testing.T) {
	if dir := os.Getenv("BOTTOWN_RECORD_TEST_DIR"); dir != "" {
		w := &Workspace{RunsDir: dir}
		err := w.Update("test", func(r *Record) error {
			r.StopRequested = true
			if os.Getenv("BOTTOWN_RECORD_TEST_COMPLETE") != "" {
				now := time.Now().UTC()
				code := 1
				r.Status, r.Ended, r.ExitCode, r.Key = StatusFailed, &now, &code, ""
			}
			fmt.Println("locked")
			var b [1]byte
			_, err := os.Stdin.Read(b[:])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	w := &Workspace{RunsDir: t.TempDir()}
	stale := Record{ID: "test", Status: StatusRunning, Key: "secret", Actor: "codex"}
	if err := w.Save(stale); err != nil {
		t.Fatal(err)
	}
	for _, complete := range []bool{false, true} {
		c := exec.Command(os.Args[0], "-test.run=^TestRecordUpdateProcess$")
		c.Env = append(os.Environ(), "BOTTOWN_RECORD_TEST_DIR="+w.RunsDir)
		if complete {
			c.Env = append(c.Env, "BOTTOWN_RECORD_TEST_COMPLETE=1")
		}
		in, err := c.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := c.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		c.Stderr = os.Stderr
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill() })
		if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "locked\n" {
			t.Fatalf("child lock: %q, %v", line, err)
		}
		done := make(chan error, 1)
		go func() {
			done <- w.Update("test", func(r *Record) error {
				r.HarnessPID = 123
				return nil
			})
		}()
		select {
		case err := <-done:
			t.Fatalf("update bypassed child lock: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if _, err := in.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		_ = in.Close()
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		r, err := w.Load("test")
		if err != nil {
			t.Fatal(err)
		}
		if !r.StopRequested || r.HarnessPID != 123 || r.Actor != stale.Actor {
			t.Fatalf("lost update: %+v", r)
		}
		if complete && (r.Ended == nil || r.Status != StatusFailed || r.Key != "" || r.ExitCode == nil) {
			t.Fatalf("lost completion: %+v", r)
		}
	}
	completed, err := w.Load("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Save(stale); !errors.Is(err, os.ErrExist) {
		t.Fatalf("stale save: %v", err)
	}
	after, err := w.Load("test")
	if err != nil || !reflect.DeepEqual(completed, after) {
		t.Fatalf("stale save changed completion: %+v, %v", after, err)
	}

	info, err := os.Stat(w.Path("test", ".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
}

func TestInvalidRunIDRefused(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	for _, id := range []string{"", "runs", ".", "..", "r-", "r-nothex", "r-2ecab347.check"} {
		if got := w.WorktreeDir(id); got != "" {
			t.Errorf("WorktreeDir(%q) = %q; want empty", id, got)
		}
		if err := w.Remove(id); err == nil {
			t.Errorf("Remove(%q) succeeded", id)
		}
		w.RemoveFiles(id)
		if _, err := os.Stat(w.RunsDir); err != nil {
			t.Fatalf("RemoveFiles(%q) removed the runs directory: %v", id, err)
		}
	}
	if got, want := w.WorktreeDir("r-2ecab347"), filepath.Join(w.RunsDir, "r-2ecab347"); got != want {
		t.Errorf("WorktreeDir(valid) = %q; want %q", got, want)
	}
}

func TestRecordsSkipsNonRunFiles(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	if err := w.Save(Record{ID: "r-1234abcd", Status: StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	// A check result is the run's own file: skipped without a warning.
	if err := os.WriteFile(w.Path("r-1234abcd", ".check.json"), []byte("{\n  \"sha\": \"deadbeef\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A stray file with no id unmarshals into an empty record.
	if err := os.WriteFile(filepath.Join(w.RunsDir, "notes.json"), []byte("{\n  \"sha\": \"deadbeef\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A record whose id does not match its filename.
	if err := os.WriteFile(filepath.Join(w.RunsDir, "r-9999.json"), []byte("{\n  \"id\": \"r-8888\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	recs, err := w.Records(&warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != "r-1234abcd" {
		t.Fatalf("records: %+v", recs)
	}
	for _, name := range []string{"notes.json", "r-9999.json"} {
		if !strings.Contains(warn.String(), name) {
			t.Errorf("no warning naming %s: %q", name, warn.String())
		}
	}
	if strings.Contains(warn.String(), ".check.json") {
		t.Errorf("warning for a run's check result: %q", warn.String())
	}
}

// A record file that cannot be parsed is listed as its run, unknown, with
// the error; an unparsable file not named for a run is skipped with a
// warning.
func TestRecordsListsUnreadableRecord(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	if err := w.Save(Record{ID: "r-1234abcd", Status: StatusSucceeded, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.Path("r-dead00", ".json"), []byte("{\"id\": \"r-dead00\", "), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.RunsDir, "notes.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	recs, err := w.Records(&warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].ID != "r-1234abcd" || recs[1].ID != "r-dead00" {
		t.Fatalf("records: %+v", recs)
	}
	if u := recs[1]; u.Status != StatusUnknown || u.LoadError == "" {
		t.Errorf("unreadable record: %+v", u)
	}
	if !strings.Contains(warn.String(), "notes.json") || strings.Contains(warn.String(), "r-dead00") {
		t.Errorf("warnings: %q", warn.String())
	}
}

func TestConcurrentRecordSaves(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	start := make(chan struct{})
	results := make(chan error, 20)
	for i := 0; i < cap(results); i++ {
		go func() { <-start; results <- w.Save(Record{ID: "test", Key: "secret"}) }()
	}
	close(start)
	saved := 0
	for i := 0; i < cap(results); i++ {
		err := <-results
		if err == nil {
			saved++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if saved != 1 {
		t.Fatalf("saved %d records", saved)
	}
	if _, err := w.Load("test"); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(w.Path("test", ".json.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files: %v, %v", matches, err)
	}
}

func TestRecordUpdateFailure(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	// A leftover file from an older writer must neither block nor receive writes.
	legacy := w.Path("test", ".json.tmp")
	if err := os.WriteFile(legacy, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Save(Record{ID: "test", Key: "secret"}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("cancel update")
	if err := w.Update("test", func(r *Record) error {
		r.Key = ""
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("update error: %v", err)
	}
	r, err := w.Load("test")
	if err != nil || r.Key != "secret" {
		t.Fatalf("failed update changed record: %+v, %v", r, err)
	}
	b, err := os.ReadFile(legacy)
	if err != nil || string(b) != "untouched" {
		t.Fatalf("legacy temporary file changed: %q, %v", b, err)
	}
}

// Removing a run's files removes its lock file too (#233), and an Update
// after that fails without leaving one behind for good: the sweep takes it.
func TestRemoveFilesRemovesLock(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	if err := w.Save(Record{ID: "r-1", Status: StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path("r-1", ".lock")); err != nil {
		t.Fatalf("no lock after Save: %v", err)
	}
	w.RemoveFiles("r-1")
	if _, err := os.Stat(w.Path("r-1", ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock survives RemoveFiles: %v", err)
	}
	if err := w.Update("r-1", func(*Record) error { return nil }); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Update of a removed run: %v", err)
	}
	w.RemoveOrphanLocks()
	if ents, _ := os.ReadDir(w.RunsDir); len(ents) != 0 {
		t.Fatalf("runs directory not empty: %v", ents)
	}
}

// The sweep removes lock files whose record is gone, and keeps those of
// existing runs and files that are not run locks.
func TestRemoveOrphanLocks(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	if err := w.Save(Record{ID: "r-a1", Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"r-b1.lock", "r-b2.lock", "other.lock"} {
		if err := os.WriteFile(filepath.Join(w.RunsDir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.RemoveOrphanLocks()
	for name, kept := range map[string]bool{"r-a1.lock": true, "r-a1.json": true, "other.lock": true, "r-b1.lock": false, "r-b2.lock": false} {
		_, err := os.Stat(filepath.Join(w.RunsDir, name))
		if kept && err != nil {
			t.Errorf("%s removed: %v", name, err)
		}
		if !kept && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s kept: %v", name, err)
		}
	}
}

// A waiter whose lock file is unlinked while it waits takes the lock again
// on the file the path names, so it never holds a lock beside another
// holder of the new file.
func TestLockRecordRetakesUnlinkedLock(t *testing.T) {
	w := &Workspace{RunsDir: t.TempDir()}
	first, err := lockFile(w.Path("r-1", ".lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *os.File)
	go func() {
		f, err := lockFile(w.Path("r-1", ".lock"), true)
		if err != nil {
			t.Error(err)
		}
		got <- f
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter open the old file
	if err := os.Remove(w.Path("r-1", ".lock")); err != nil {
		t.Fatal(err)
	}
	first.Close()
	f := <-got
	defer f.Close()
	held, _ := f.Stat()
	named, err := os.Stat(w.Path("r-1", ".lock"))
	if err != nil || !os.SameFile(held, named) {
		t.Fatalf("waiter holds an unlinked lock file: %v", err)
	}
}
