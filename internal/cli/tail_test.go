package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/robertolupi/bottown/internal/runs"
)

// testTail is a tail model with a fixed clock and the scrollback it
// prints, with the styling stripped.
func testTail() (*tailModel, *[]string) {
	m := newTailModel(newStyles())
	start := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return start.Add(3*time.Minute + 12*time.Second) }
	var printed []string
	m.println = func(s string) tea.Cmd {
		printed = append(printed, strings.Split(stripANSI(s), "\n")...)
		return nil
	}
	m.Update(attachMsg{ID: "r-1", Kind: "implement", Target: "#12", Actor: "claude", Family: "anthropic", Started: start, Limit: time.Hour})
	return m, &printed
}

// tailView is the view's text with the styling removed.
func tailView(m *tailModel) string {
	return stripANSI(m.View().Content)
}

// update delivers msg, and the printedMsg of any print it starts.
func update(m *tailModel, msg tea.Msg) {
	m.Update(msg)
	if m.printing {
		m.Update(printedMsg{})
	}
}

var (
	textLine    = runs.LogLine{Kind: runs.LineText, Text: "Reading the code.", Detail: "Reading the code.\nThen the tests."}
	runningLine = runs.LogLine{Kind: runs.LineTool, Tool: "Bash", ToolClass: runs.ToolShell, State: "running", Text: "Bash: go test ./..."}
	statusLine  = runs.LogLine{Kind: runs.LineStatus, Text: "session started"}
)

func TestTailPrintsFinalLinesAndKeepsRunningCallsLive(t *testing.T) {
	m, printed := testTail()
	update(m, linesMsg{lines: []runs.LogLine{statusLine, textLine, runningLine}})
	// Final lines go to the scrollback whole; the assistant message with
	// its detail, every line indented.
	if want := []string{"  session started", "  Reading the code.", "  Then the tests."}; strings.Join(*printed, "|") != strings.Join(want, "|") {
		t.Fatalf("scrollback = %q, want %q", *printed, want)
	}
	view := tailView(m)
	if !strings.Contains(view, "run r-1 · implement #12 · claude (anthropic) · 3m12s of 1h00m") {
		t.Fatalf("view has no run header:\n%s", view)
	}
	if !strings.Contains(view, "Bash go test ./... · running") || strings.Contains(view, "Reading") {
		t.Fatalf("view should keep only the running call:\n%s", view)
	}

	// The call completes: it is printed once, and the view keeps the new
	// last line, which may still change.
	done := runningLine
	done.State = "completed"
	update(m, linesMsg{lines: []runs.LogLine{statusLine, textLine, done, statusLine}})
	if got := (*printed)[3:]; len(got) != 1 || got[0] != "  Bash go test ./... · completed" {
		t.Fatalf("after the result: %q", got)
	}
	if view := tailView(m); strings.Contains(view, "Bash") || !strings.Contains(view, "session started") {
		t.Fatalf("view after the result:\n%s", view)
	}

	// The run ends: the rest is printed, and the view leaves the run.
	update(m, linesMsg{lines: []runs.LogLine{statusLine, textLine, done, statusLine}, ended: true})
	if got := (*printed)[4:]; len(got) != 1 || got[0] != "  session started" {
		t.Fatalf("at the end: %q", got)
	}
	if view := tailView(m); strings.Contains(view, "run r-1") {
		t.Fatalf("view still shows the ended run:\n%s", view)
	}
	// Lines of no run (a late follower) are dropped.
	update(m, linesMsg{lines: []runs.LogLine{statusLine, statusLine, statusLine, statusLine, statusLine, statusLine}})
	if len(*printed) != 5 {
		t.Fatalf("printed lines of no run: %q", *printed)
	}
}

// A flow's post-implement checks show like a run, with their commands in
// the header and their output as plain lines.
func TestTailFollowsChecks(t *testing.T) {
	m, printed := testTail()
	start := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	m.Update(attachMsg{ID: "flow-12-1a2b3c4d5e.e2e", Target: "!30 at 1a2b3c4d5e", Check: "make e2e", Started: start, Limit: time.Hour})
	plain := func(s string) runs.LogLine { return runs.LogLine{Kind: runs.LinePlain, Text: s} }
	update(m, linesMsg{lines: []runs.LogLine{plain("==> [e2e] make e2e"), plain("=== RUN   TestE2E")}})
	if want := []string{"  ==> [e2e] make e2e"}; strings.Join(*printed, "|") != strings.Join(want, "|") {
		t.Fatalf("scrollback = %q, want %q", *printed, want)
	}
	view := tailView(m)
	if !strings.Contains(view, "checks flow-12-1a2b3c4d5e.e2e · !30 at 1a2b3c4d5e · make e2e · 3m12s of 1h00m") || !strings.Contains(view, "=== RUN   TestE2E") {
		t.Fatalf("view:\n%s", view)
	}
}

func TestTailPrintsInOrderOneAtATime(t *testing.T) {
	m, printed := testTail()
	m.Update(printMsg{lines: []string{"run r-1: implement #12"}, note: true})
	if !m.printing {
		t.Fatal("first print not started")
	}
	// While one print is in flight, later lines wait, in order.
	m.Update(printMsg{lines: []string{"  log: r-1.log"}, note: true})
	m.Update(linesMsg{lines: []runs.LogLine{statusLine, textLine}})
	if len(*printed) != 1 {
		t.Fatalf("printed while a print was in flight: %q", *printed)
	}
	m.Update(printedMsg{})
	if want := "run r-1: implement #12|  log: r-1.log|  session started"; strings.Join(*printed, "|") != want {
		t.Fatalf("scrollback = %q, want %s", *printed, want)
	}
	if m.note != "log: r-1.log" {
		t.Fatalf("note = %q, want the last line the launcher printed", m.note)
	}
}

func TestTailDrainPauseAndQuit(t *testing.T) {
	m, _ := testTail()
	m.Update(printMsg{lines: []string{"flow #12: merge?"}})
	done := make(chan struct{})
	m.Update(drainMsg{done: done, pause: true})
	select {
	case <-done:
		t.Fatal("drained before the print finished")
	default:
	}
	m.Update(printedMsg{})
	select {
	case <-done:
	default:
		t.Fatal("not drained after the print finished")
	}
	// Paused, the view is empty, so the prompt below it starts clean.
	if v := tailView(m); v != "" {
		t.Fatalf("paused view = %q", v)
	}
	m.Update(resumeMsg{})
	if tailView(m) == "" {
		t.Fatal("resumed view is empty")
	}
	_, cmd := m.Update(quitMsg{})
	if cmd == nil {
		t.Fatal("quit with nothing to print should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit did not end the program")
	}
}

func TestTailViewBoundsLiveLines(t *testing.T) {
	m, _ := testTail()
	m.width = 30
	lines := []runs.LogLine{}
	for range 12 {
		lines = append(lines, runningLine)
	}
	lines[11].Text = "Bash: " + strings.Repeat("x", 100)
	update(m, linesMsg{lines: lines})
	view := tailView(m)
	if !strings.Contains(view, "… 4 more") || strings.Count(view, "Bash") != maxLiveLines {
		t.Fatalf("view should show the last %d live lines:\n%s", maxLiveLines, view)
	}
	for _, l := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
		if w := lipgloss.Width(l); w > 30 {
			t.Fatalf("line wider than the terminal (%d): %q", w, l)
		}
	}
}

func TestTailLogLine(t *testing.T) {
	st := newStyles()
	for _, tc := range []struct {
		line runs.LogLine
		full bool
		want string
	}{
		{runs.LogLine{Kind: runs.LineTool, Tool: "Read", State: "failed", Text: "Read: a.go"}, false, "Read a.go · failed"},
		{runs.LogLine{Kind: runs.LineTool, Tool: "mcp__bottown__whoami", Text: "mcp__bottown__whoami"}, false, "mcp__bottown__whoami"},
		{textLine, false, "Reading the code."},
		{textLine, true, "Reading the code.\nThen the tests."},
		{runs.LogLine{Kind: runs.LinePlain, Text: "\x1b[31mred\x1b[0m\a bell"}, false, "red bell"},
	} {
		if got := stripANSI(st.logLine(tc.line, tc.full, "")); got != tc.want {
			t.Errorf("logLine(%+v, %v) = %q, want %q", tc.line, tc.full, got, tc.want)
		}
	}
}

func TestLiveTailWritesThroughOnceEnded(t *testing.T) {
	var term bytes.Buffer
	tail := &liveTail{term: &term, exited: make(chan struct{})}
	close(tail.exited)
	if _, err := tail.Write([]byte("flow #12: implement #12 with claude\nMerge? ")); err != nil {
		t.Fatal(err)
	}
	if term.String() != "flow #12: implement #12 with claude\n" {
		t.Fatalf("terminal = %q, want the whole line", term.String())
	}
	tail.Close()
	if term.String() != "flow #12: implement #12 with claude\nMerge? " {
		t.Fatalf("terminal = %q, want the partial line at close", term.String())
	}
}
