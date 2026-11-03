package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
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

// fakeCheckout makes a checkout whose web/ has a stand-in for vite that
// writes the build where it is told to, and returns its top directory.
func fakeCheckout(t *testing.T) string {
	t.Helper()
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
mkdir -p "$out"
printf '<!doctype html><title>built by the stand-in</title>' > "$out/index.html"
`)
	if err := os.Chmod(vite, 0o755); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(web, "node_modules"), later, later); err != nil {
		t.Fatal(err)
	}
	return top
}

// runPreviewCmd runs `web preview` with args, capturing its output.
func runPreviewCmd(ctx context.Context, args ...string) (*lockedBuffer, *lockedBuffer, <-chan error) {
	root := newRoot()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	root.SetOut(out)
	root.SetErr(errb)
	root.SetArgs(append([]string{"web", "preview"}, args...))
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	return out, errb, done
}

// TestWebPreview runs `web preview` in a checkout without an instance, with
// a stand-in for vite that writes the build where it is told to.
func TestWebPreview(t *testing.T) {
	top := fakeCheckout(t)
	t.Chdir(top)
	t.Setenv("BOTTOWN_INSTANCE", "")

	// A fixture that is not there fails before the build.
	_, _, done := runPreviewCmd(context.Background(), "--fixture", "nope")
	if err := <-done; err == nil || !strings.Contains(err.Error(), "have: default") {
		t.Errorf("missing fixture: %v", err)
	}
	// So does --detach, before it starts a child that would only find it
	// after the build, from the log.
	_, _, done = runPreviewCmd(context.Background(), "--detach", "--fixture", "nope")
	if err := <-done; err == nil || !strings.Contains(err.Error(), "have: default") {
		t.Errorf("missing fixture with --detach: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, errb, done := runPreviewCmd(ctx)
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

// TestWebPreviewDetachedChild runs the detached child in-process: it
// records where it serves, and removes the record when it exits.
func TestWebPreviewDetachedChild(t *testing.T) {
	top := fakeCheckout(t)
	t.Chdir(top)
	t.Setenv("BOTTOWN_INSTANCE", "")
	t.Setenv(previewChildEnv, "1")
	state := filepath.Join(t.TempDir(), "state.json")
	t.Setenv(previewStateEnv, state)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, errb, done := runPreviewCmd(ctx)

	var st previewState
	for deadline := time.Now().Add(10 * time.Second); ; {
		if s, err := loadPreviewState(state); err == nil {
			st = s
			break
		}
		select {
		case err := <-done:
			t.Fatalf("child exited: %v\n%s", err, errb.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no state file; stderr:\n%s", errb.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.PID != os.Getpid() {
		t.Errorf("state pid %d, want %d", st.PID, os.Getpid())
	}
	if st.URL == "" {
		t.Errorf("state has no URL: %+v", st)
	}
	resp, err := http.Get(st.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("child: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not stop")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state file left behind: %v", err)
	}
}

// fakePreviewServer answers the preview's /api/preview with pid, standing
// in for a running preview without a real one.
func fakePreviewServer(t *testing.T, pid int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"fixture": "default", "fixtures": ["default"], "pid": %d}`, pid)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestStopPreviewStopsPreview stops the process the state file names when
// it is still the preview serving there.
func TestStopPreviewStopsPreview(t *testing.T) {
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Skipf("cannot start a sleeper: %v", err)
	}
	// Reap it as soon as it dies, so processAlive does not read the zombie.
	go func() { _ = sleeper.Wait() }()
	defer sleeper.Process.Kill()

	state := filepath.Join(t.TempDir(), "state.json")
	logPath := state + ".log"
	writeFile(t, logPath, "log\n")
	st := previewState{PID: sleeper.Process.Pid, Log: logPath}
	st.URL = fakePreviewServer(t, sleeper.Process.Pid)
	if err := savePreviewState(state, st); err != nil {
		t.Fatal(err)
	}

	out := &lockedBuffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	if err := stopPreview(cmd, state); err != nil {
		t.Fatal(err)
	}
	if processAlive(sleeper.Process.Pid) {
		t.Errorf("process %d still alive", sleeper.Process.Pid)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state file left behind: %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("log left behind: %v", err)
	}
	if !strings.Contains(out.String(), "stopped preview") {
		t.Errorf("output: %q", out.String())
	}

	// With no state file, there is nothing to stop.
	if err := stopPreview(cmd, filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Errorf("expected an error for a missing state file")
	}
}

// TestStopPreviewLeavesReusedPID stops nothing when the pid a stale state
// file names is now an unrelated process, not the preview.
func TestStopPreviewLeavesReusedPID(t *testing.T) {
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Skipf("cannot start a sleeper: %v", err)
	}
	go func() { _ = sleeper.Wait() }()
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()

	state := filepath.Join(t.TempDir(), "state.json")
	st := previewState{PID: sleeper.Process.Pid}
	// The URL serves a preview that reports a different pid.
	st.URL = fakePreviewServer(t, sleeper.Process.Pid+1)
	if err := savePreviewState(state, st); err != nil {
		t.Fatal(err)
	}

	out := &lockedBuffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	if err := stopPreview(cmd, state); err != nil {
		t.Fatal(err)
	}
	if !processAlive(sleeper.Process.Pid) {
		t.Errorf("process %d was signalled", sleeper.Process.Pid)
	}
	if !strings.Contains(out.String(), "was not running") {
		t.Errorf("output: %q", out.String())
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state file left behind: %v", err)
	}
}

// TestStopPreviewLeavesUnrelatedPID stops nothing when the state file's
// URL answers nothing: the pid is not a preview.
func TestStopPreviewLeavesUnrelatedPID(t *testing.T) {
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Skipf("cannot start a sleeper: %v", err)
	}
	go func() { _ = sleeper.Wait() }()
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()

	state := filepath.Join(t.TempDir(), "state.json")
	if err := savePreviewState(state, previewState{PID: sleeper.Process.Pid, URL: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	out := &lockedBuffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	if err := stopPreview(cmd, state); err != nil {
		t.Fatal(err)
	}
	if !processAlive(sleeper.Process.Pid) {
		t.Errorf("process %d was signalled", sleeper.Process.Pid)
	}
}

// TestDetachPreviewRefusesWhenRunning leaves a live preview in place alone.
func TestDetachPreviewRefusesWhenRunning(t *testing.T) {
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Skipf("cannot start a sleeper: %v", err)
	}
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()

	state := filepath.Join(t.TempDir(), "state.json")
	st := previewState{PID: sleeper.Process.Pid, URL: fakePreviewServer(t, sleeper.Process.Pid)}
	if err := savePreviewState(state, st); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetOut(&lockedBuffer{})
	cmd.SetErr(&lockedBuffer{})
	err := detachPreview(cmd, t.TempDir(), state, previewSettings{})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("detach with a live preview: %v", err)
	}
}

// TestPreviewAlive checks that a state file counts as running only while
// its pid still serves its URL, so a stale or reused pid is not the preview.
func TestPreviewAlive(t *testing.T) {
	if previewAlive(previewState{PID: 0, URL: "http://127.0.0.1:1"}) {
		t.Errorf("pid 0 reported alive")
	}
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Skipf("cannot start a sleeper: %v", err)
	}
	go func() { _ = sleeper.Wait() }()
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()

	if previewAlive(previewState{PID: sleeper.Process.Pid, URL: "http://127.0.0.1:1"}) {
		t.Errorf("a process with no preview at its URL reported alive")
	}
	if !previewAlive(previewState{PID: sleeper.Process.Pid, URL: fakePreviewServer(t, sleeper.Process.Pid)}) {
		t.Errorf("a live preview reported not alive")
	}
	if previewAlive(previewState{PID: sleeper.Process.Pid, URL: fakePreviewServer(t, sleeper.Process.Pid+1)}) {
		t.Errorf("a reused pid reported alive")
	}
}

// TestLockPreviewExcludes checks that a second lock fails while the first
// is held, which is what keeps two detaches from both starting.
func TestLockPreviewExcludes(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	unlock, err := lockPreview(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockPreview(state); err == nil {
		t.Errorf("second lock succeeded")
	}
	unlock()
	unlock2, err := lockPreview(state)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
}

// TestStopRunPreviews stops previews recorded in a run's scratch directory.
func TestStopRunPreviews(t *testing.T) {
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Skipf("cannot start a sleeper: %v", err)
	}
	go func() { _ = sleeper.Wait() }()
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()

	dir := t.TempDir()
	state := filepath.Join(dir, "bottown-preview-abc.json")
	st := previewState{PID: sleeper.Process.Pid, URL: fakePreviewServer(t, sleeper.Process.Pid)}
	if err := savePreviewState(state, st); err != nil {
		t.Fatal(err)
	}
	// A state file for another run's checkout is not in this directory and
	// is left alone.
	other := filepath.Join(t.TempDir(), "bottown-preview-xyz.json")
	if err := savePreviewState(other, previewState{PID: sleeper.Process.Pid, URL: st.URL}); err != nil {
		t.Fatal(err)
	}

	warn := &lockedBuffer{}
	stopRunPreviews(warn, dir)
	if processAlive(sleeper.Process.Pid) {
		t.Errorf("preview %d still alive; warnings:\n%s", sleeper.Process.Pid, warn.String())
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state file left behind: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("other run's state file removed: %v", err)
	}
}

func TestPreviewStatePath(t *testing.T) {
	if got := previewStatePath("/x/y", "/top"); got != "/x/y" {
		t.Errorf("env path: %q", got)
	}
	a, b := previewStatePath("", "/top/a"), previewStatePath("", "/top/b")
	if a == b || !strings.HasPrefix(filepath.Base(a), "bottown-preview-") {
		t.Errorf("paths: %q %q", a, b)
	}
}
