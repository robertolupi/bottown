package runs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	claudeText     = `{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the code."}]}}` + "\n"
	claudeToolUse  = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n"
	claudeResult   = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}` + "\n"
	claudeLastText = `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}` + "\n"
)

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func texts(lines []LogLine) []string {
	out := []string{}
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLogFollowerRerendersOnlyWhenTheLogChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r-1.log")
	f := &LogFollower{Path: path, Harness: "claude"}
	if lines, changed := f.Poll(); changed || len(lines) != 0 {
		t.Fatalf("no log yet: got %v, changed %v", lines, changed)
	}
	appendFile(t, path, claudeText)
	lines, changed := f.Poll()
	if !changed || !equalStrings(texts(lines), []string{"Reading the code."}) {
		t.Fatalf("first read: got %v, changed %v", texts(lines), changed)
	}
	// Unchanged: the same lines, not re-rendered.
	if again, changed := f.Poll(); changed || len(again) != 1 {
		t.Fatalf("unchanged log re-rendered: %v, %v", texts(again), changed)
	}
	appendFile(t, path, claudeToolUse)
	lines, changed = f.Poll()
	if !changed || len(lines) != 2 || lines[1].State != "running" {
		t.Fatalf("after the tool call: %+v, changed %v", lines, changed)
	}
	// The live transcript counts as part of the log.
	live := filepath.Join(filepath.Dir(path), "r-1"+LiveTranscriptSuffix)
	appendFile(t, live, claudeResult)
	lines, changed = f.Poll()
	if !changed || lines[1].State != "completed" {
		t.Fatalf("after the live transcript: %+v, changed %v", lines, changed)
	}
}

func TestLogFollowerFollowCallsBackOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r-1.log")
	appendFile(t, path, claudeText)
	f := &LogFollower{Path: path, Harness: "claude"}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan []LogLine, 10)
	done := make(chan struct{})
	go func() {
		f.Follow(ctx, 5*time.Millisecond, func(l []LogLine) { got <- l })
		close(done)
	}()
	if l := <-got; len(l) != 1 {
		t.Fatalf("first callback: %v", texts(l))
	}
	appendFile(t, path, claudeToolUse)
	if l := <-got; len(l) != 2 {
		t.Fatalf("second callback: %v", texts(l))
	}
	cancel()
	<-done
	if len(got) != 0 {
		t.Fatalf("callback without a change: %v", texts(<-got))
	}
}

func TestLineTrackerHandsOutFinalLinesOnceInOrder(t *testing.T) {
	var tr LineTracker
	render := func(log string) []LogLine { return RenderLog("claude", []byte(log)) }

	// The last line may still change: nothing is final yet.
	final, live := tr.Update(render(claudeText), false)
	if len(final) != 0 || !equalStrings(texts(live), []string{"Reading the code."}) {
		t.Fatalf("one line: final %v, live %v", texts(final), texts(live))
	}
	// The text is final once a line follows it; the call is running.
	log := claudeText + claudeToolUse
	final, live = tr.Update(render(log), false)
	if !equalStrings(texts(final), []string{"Reading the code."}) || !equalStrings(texts(live), []string{"Bash: go test ./..."}) {
		t.Fatalf("text and call: final %v, live %v", texts(final), texts(live))
	}
	// A running call does not hold back the lines after it.
	log += claudeLastText + claudeText
	final, live = tr.Update(render(log), false)
	if !equalStrings(texts(final), []string{"Done."}) || !equalStrings(texts(live), []string{"Bash: go test ./...", "Reading the code."}) {
		t.Fatalf("past a running call: final %v, live %v", texts(final), texts(live))
	}
	// Its result makes the call final, at its place among the live lines.
	log += claudeResult
	final, live = tr.Update(render(log), false)
	if len(final) != 1 || final[0].Text != "Bash: go test ./..." || final[0].State != "completed" || !equalStrings(texts(live), []string{"Reading the code."}) {
		t.Fatalf("completed call: final %+v, live %v", final, texts(live))
	}
	// Nothing new: nothing handed out twice.
	if final, _ := tr.Update(render(log), false); len(final) != 0 {
		t.Fatalf("handed out again: %v", texts(final))
	}
	// When the run ends, every line left is final.
	final, live = tr.Update(render(log+claudeToolUse), true)
	if !equalStrings(texts(final), []string{"Reading the code.", "Bash: go test ./..."}) || len(live) != 0 {
		t.Fatalf("ended: final %v, live %v", texts(final), texts(live))
	}
}
