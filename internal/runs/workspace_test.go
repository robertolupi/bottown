package runs

import (
	"os"
	"path/filepath"
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
	if err := w.Remove("r-add1"); err != nil {
		t.Fatal(err)
	}
	dir, err = w.AddImplement("r-beef", "issue/7", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Git(dir, nil, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil || got != "bottown/issue/7" {
		t.Fatalf("resumed upstream = %q, %v; want bottown/issue/7", got, err)
	}
}
