package cli

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is an io.Writer that is safe to read while a command writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestWebPreview runs `web preview` in a checkout without an instance, with
// a stand-in for vite that writes the build where it is told to.
func TestWebPreview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the vite stand-in is a shell script")
	}
	top := t.TempDir()
	if _, err := git(top, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(top, "web")
	writeFile(t, filepath.Join(web, "package.json"), "{}\n")
	writeFile(t, filepath.Join(web, "package-lock.json"), "{}\n")
	writeFile(t, filepath.Join(web, "fixtures", "default.json"), `{"status": {"repo": "demo/app"}}`+"\n")
	// node_modules newer than the lock file: no npm ci.
	vite := filepath.Join(web, "node_modules", ".bin", "vite")
	writeFile(t, vite, `#!/bin/sh
out=
while [ $# -gt 0 ]; do
  case "$1" in --outDir) out="$2"; shift ;; esac
  shift
done
[ -n "$out" ] || { echo "no --outDir" >&2; exit 1; }
printf '<!doctype html><title>built by the stand-in</title>' > "$out/index.html"
`)
	if err := os.Chmod(vite, 0o755); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(web, "node_modules"), later, later); err != nil {
		t.Fatal(err)
	}

	t.Chdir(top)
	t.Setenv("BOTTOWN_INSTANCE", "")
	run := func(ctx context.Context, args ...string) (*lockedBuffer, *lockedBuffer, <-chan error) {
		root := newRoot()
		out, errb := &lockedBuffer{}, &lockedBuffer{}
		root.SetOut(out)
		root.SetErr(errb)
		root.SetArgs(append([]string{"web", "preview"}, args...))
		done := make(chan error, 1)
		go func() { done <- root.ExecuteContext(ctx) }()
		return out, errb, done
	}

	// A fixture that is not there fails before the build.
	_, _, done := run(context.Background(), "--fixture", "nope")
	if err := <-done; err == nil || !strings.Contains(err.Error(), "have: default") {
		t.Errorf("missing fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, errb, done := run(ctx)
	var base string
	for deadline := time.Now().Add(10 * time.Second); base == ""; {
		select {
		case err := <-done:
			t.Fatalf("preview exited: %v\n%s", err, errb.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no URL printed; stderr:\n%s", errb.String())
		}
		base = strings.TrimSpace(out.String())
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("URL %q", base)
	}
	get := func(path string) string {
		resp, err := http.Get(strings.TrimSuffix(base, "/") + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d %s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	if body := get("/"); !strings.Contains(body, "built by the stand-in") {
		t.Errorf("index: %q", body)
	}
	if body := get("/api/status"); !strings.Contains(body, `"repo":"demo/app"`) {
		t.Errorf("status: %q", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("after interrupt: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("preview did not stop")
	}
	// The build directory is removed on exit.
	if m, _ := filepath.Glob(filepath.Join(os.TempDir(), "bottown-preview-*", "index.html")); len(m) > 0 {
		for _, p := range m {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "stand-in") {
				t.Errorf("build left behind: %s", p)
			}
		}
	}
}
