package runs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTranscriber returns its exports in turn, the last one from then on;
// an empty export is a failure.
type fakeTranscriber struct {
	mu      sync.Mutex
	exports []string
	calls   int
}

func (f *fakeTranscriber) Transcript(_ context.Context, _ Launch, _ time.Time, w io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.exports[min(f.calls, len(f.exports)-1)]
	f.calls++
	if s == "" {
		return errors.New("no session yet")
	}
	_, err := io.WriteString(w, s)
	return err
}

func (f *fakeTranscriber) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// While crush runs, its session is followed into the live transcript, which
// the log's readers see after the log, rendered as the final log will be.
func TestFollowTranscript(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "r-1.log")
	livePath := filepath.Join(dir, "r-1"+LiveTranscriptSuffix)
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	meta := "\n" + `{"meta":{"id":"abc","title":"Fix it","created":"2026-11-03T08:40:00+01:00"}}` + "\n"
	call := `{"id":"m1","role":"assistant","parts":[{"type":"text","text":"Looking."},{"type":"tool_call","name":"bash","input":"{\"command\":\"make check\"}"}]}` + "\n"
	image := `{"id":"m2","role":"tool","parts":[{"type":"tool_result","name":"view","content":"` + strings.Repeat("QUJD", 100) + `"}]}` + "\n"
	ft := &fakeTranscriber{exports: []string{"", meta, meta + call + image}}

	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { FollowTranscript(ctx, ft, Launch{}, time.Now(), livePath, time.Millisecond) })

	// Through a failure (no session yet) to the last export.
	deadline := time.Now().Add(10 * time.Second)
	for ft.count() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	wg.Wait()

	b, err := ReadLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "QUJD") {
		t.Errorf("encoded payload kept: %q", b)
	}
	want := []string{"session started: Fix it", "Looking.", "bash: make check"}
	if got := lineTexts(RenderLog("crush", b)); !reflect.DeepEqual(got, want) {
		t.Errorf("live tail %v, want %v", got, want)
	}

	// Without a live transcript, the log alone.
	if err := os.Remove(livePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("Done.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadLog(logPath); err != nil || string(b) != "Done.\n" {
		t.Errorf("ReadLog = %q, %v", b, err)
	}
	if _, err := ReadLog(filepath.Join(dir, "none.log")); err == nil {
		t.Error("ReadLog of a missing log succeeded")
	}
}

// A failed export leaves the live transcript as it was.
func TestFollowTranscriptKeepsLastOnFailure(t *testing.T) {
	livePath := filepath.Join(t.TempDir(), "r-1"+LiveTranscriptSuffix)
	ft := &fakeTranscriber{exports: []string{"one\n", ""}}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { FollowTranscript(ctx, ft, Launch{}, time.Now(), livePath, time.Millisecond) })
	deadline := time.Now().Add(10 * time.Second)
	for ft.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	wg.Wait()
	if b, err := os.ReadFile(livePath); err != nil || string(b) != "one\n" {
		t.Errorf("live transcript %q, %v", b, err)
	}
}
