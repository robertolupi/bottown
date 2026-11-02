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
