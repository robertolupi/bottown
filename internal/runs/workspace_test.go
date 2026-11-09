package runs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestTmpDirOutsideCheckout(t *testing.T) {
	hostTmp := t.TempDir()
	t.Setenv("TMPDIR", hostTmp)
	// Ignore any checkout containing the test runner's own temporary root.
	t.Setenv("GIT_CEILING_DIRECTORIES", hostTmp)
	top := filepath.Join(hostTmp, "main")
	if err := os.MkdirAll(top, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(top, nil, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	w := &Workspace{RunsDir: filepath.Join(top, ".bottown", "runs")}
	dir := w.TmpDir("r-1")
	if filepath.Dir(dir) != hostTmp {
		t.Fatalf("scratch root: %s", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// A harness's Go tests inherit TMPDIR. Their temporary directories must
	// not discover the owner's checkout by walking up to main.
	t.Setenv("TMPDIR", dir)
	t.Run("harness test", func(t *testing.T) {
		child := t.TempDir()
		if out, err := Git(child, nil, "rev-parse", "--show-toplevel"); err == nil {
			t.Fatalf("temporary directory is in a checkout: %s", out)
		}
	})
	t.Setenv("TMPDIR", hostTmp)
	if other := w.TmpDir("r-2"); other == dir {
		t.Fatal("runs share scratch directory")
	}
	other := &Workspace{RunsDir: filepath.Join(hostTmp, "other", ".bottown", "runs")}
	if other.TmpDir("r-1") == dir {
		t.Fatal("workspaces share scratch directory")
	}
}

func TestImplementWorktreeRemoteAndUpstream(t *testing.T) {
	tmp := t.TempDir()
	forge := filepath.Join(tmp, "forge.git")
	seed := filepath.Join(tmp, "seed")
	top := filepath.Join(tmp, "main")
	for _, dir := range []string{seed, top} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Git(tmp, nil, "init", "-q", "--bare", "-b", "main", forge); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(seed, nil, "init", "-q", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][]string{{"user.name", "owner"}, {"user.email", "owner@example.test"}} {
		if _, err := Git(seed, nil, "config", kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Git(seed, nil, "commit", "-q", "--allow-empty", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(seed, nil, "remote", "add", "bottown", forge); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(seed, nil, "push", "-q", "bottown", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(top, nil, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	w, err := Open(top, "bottown", forge, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(w.Mirror, nil, "remote", "rename", "bottown", "origin"); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(w.Mirror, nil, "rev-parse", "--verify", "refs/remotes/bottown/main"); err != nil {
		t.Fatalf("old mirror's refs were not migrated: %v", err)
	}
	dir, err := w.AddImplement("r-add1", "issue/7", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Git(dir, nil, "remote", "get-url", "bottown"); err != nil || got != forge {
		t.Fatalf("forge remote = %q, %v; want %q", got, err, forge)
	}
	if _, err := Git(dir, nil, "fetch", "-q", "bottown", "main"); err != nil {
		t.Fatal(err)
	}
	if got, err := Git(dir, nil, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil || got != "bottown/main" {
		t.Fatalf("fresh upstream = %q, %v; want bottown/main", got, err)
	}
	if got, err := Git(dir, nil, "status", "--short", "--branch"); err != nil || got != "## issue/7...bottown/main" {
		t.Fatalf("fresh status = %q, %v", got, err)
	}
	if _, err := Git(dir, nil, "push", "-q", "bottown", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A live launcher must keep its branch, even when its record says failed.
	if err := w.Save(Record{ID: "r-add1", Status: StatusFailed, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if err := w.PrepareImplement("issue/7"); err == nil || !strings.Contains(err.Error(), "live run r-add1") {
		t.Fatalf("live branch holder: %v", err)
	}
	if got, err := Git(dir, nil, "symbolic-ref", "--short", "HEAD"); err != nil || got != "issue/7" {
		t.Fatalf("live worktree branch = %q, %v", got, err)
	}
	if err := w.Update("r-add1", func(r *Record) error { r.PID = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	// Keep an unpushed commit and dirty files for inspection after retrying.
	if err := Configure(dir, "codex", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(dir, nil, "commit", "-q", "--allow-empty", "-m", "unpublished work"); err != nil {
		t.Fatal(err)
	}
	oldHEAD, err := Git(dir, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes"), []byte("kept work"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldDir := dir

	dir, err = w.AddImplement("r-beef", "issue/7", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Git(oldDir, nil, "rev-parse", "HEAD"); err != nil || got != oldHEAD {
		t.Fatalf("kept HEAD = %q, %v; want %s", got, err, oldHEAD)
	}
	if got, err := Git(oldDir, nil, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || got != "HEAD" {
		t.Fatalf("kept worktree is not detached: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(oldDir, "notes")); err != nil || string(got) != "kept work" {
		t.Fatalf("kept files = %q, %v", got, err)
	}
	if got, err := Git(dir, nil, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil || got != "bottown/issue/7" {
		t.Fatalf("resumed upstream = %q, %v; want bottown/issue/7", got, err)
	}
}

// Runs started together share the mirror's config, refs and worktree list;
// git fails rather than waits on their lock files, so bottown serializes its
// writes to the mirror (#453).
func TestConcurrentAddImplement(t *testing.T) {
	tmp := t.TempDir()
	forge := filepath.Join(tmp, "forge.git")
	seed := filepath.Join(tmp, "seed")
	top := filepath.Join(tmp, "main")
	for _, dir := range []string{seed, top} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		dir  string
		args []string
	}{
		{tmp, []string{"init", "-q", "--bare", "-b", "main", forge}},
		{seed, []string{"init", "-q", "-b", "main"}},
		{seed, []string{"config", "user.name", "owner"}},
		{seed, []string{"config", "user.email", "owner@example.test"}},
		{seed, []string{"commit", "-q", "--allow-empty", "-m", "initial"}},
		{seed, []string{"remote", "add", "bottown", forge}},
		{seed, []string{"push", "-q", "bottown", "main", "main:issue/1", "main:issue/3", "main:issue/5"}},
		{top, []string{"init", "-q"}},
	} {
		if _, err := Git(c.dir, nil, c.args...); err != nil {
			t.Fatal(err)
		}
	}
	w, err := Open(top, "bottown", forge, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	dirs := make([]string, n)
	errs := make([]error, n+2)
	for i := range n {
		wg.Go(func() {
			dirs[i], errs[i] = w.AddImplement(fmt.Sprintf("r-c%d", i), fmt.Sprintf("issue/%d", i), "main")
		})
	}
	// Fetches race the worktree adds too.
	for i := range 2 {
		wg.Go(func() { errs[n+i] = w.Sync(t.Context()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	if t.Failed() {
		return
	}
	for i, dir := range dirs {
		want := "bottown/main"
		if i%2 == 1 && i < 6 {
			want = fmt.Sprintf("bottown/issue/%d", i)
		}
		if got, err := Git(dir, nil, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil || got != want {
			t.Errorf("issue/%d upstream = %q, %v; want %s", i, got, err, want)
		}
		if got, err := Git(dir, nil, "symbolic-ref", "--short", "HEAD"); err != nil || got != fmt.Sprintf("issue/%d", i) {
			t.Errorf("worktree %d branch = %q, %v", i, got, err)
		}
	}
}
