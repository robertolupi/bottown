package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/webui"
)

// The full-screen log's tests (monitor-ui design §8), driving the model
// with a fake log source as ui_test.go drives it with a fake Live.

// fakeLog is a log source whose lines the test sets.
type fakeLog struct{ lines []runs.LogLine }

func (f *fakeLog) Poll() ([]runs.LogLine, bool) {
	return append([]runs.LogLine(nil), f.lines...), true
}

var uiEnter = tea.KeyPressMsg{Code: tea.KeyEnter}

// openLogOn opens the selected item's log, reading src.
func openLogOn(t *testing.T, m *uiModel, src *fakeLog) {
	t.Helper()
	m.logSource = func(string) (uiLogSource, error) { return src, nil }
	_, cmd := m.Update(uiEnter)
	if cmd == nil || m.logv == nil {
		t.Fatal("enter did not open the log")
	}
	m.Update(cmd())
}

// pollLogOnce is the follow tick firing and its poll coming back; it
// returns the command the poll's result schedules.
func pollLogOnce(t *testing.T, m *uiModel) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(uiLogTickMsg{id: m.logv.item.ID})
	if cmd == nil {
		t.Fatal("the tick did not poll")
	}
	_, next := m.Update(cmd())
	return next
}

// viewRows is the view's screen rows.
func viewRows(m *uiModel) []string {
	return strings.Split(strings.TrimSuffix(uiView(m), "\n"), "\n")
}

// hasRow says the view has a row that is exactly s, after its indent.
func hasRow(m *uiModel, s string) bool {
	for _, r := range viewRows(m) {
		if strings.TrimSpace(r) == s {
			return true
		}
	}
	return false
}

func oneItemLive(n int) webui.Live {
	return webui.Live{Items: []webui.LiveItem{uiItem("r-1", "#42", "Retry webhook deliveries", n)}}
}

// A growing log is followed: the end stays on screen.
func TestUILogFollowsGrowingLog(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	setLive(m, oneItemLive(3))
	src := &fakeLog{lines: uiLogLines(30)}
	openLogOn(t, m, src)

	rows := viewRows(m)
	if len(rows) != 10 {
		t.Fatalf("view has %d rows, want the height 10:\n%s", len(rows), uiView(m))
	}
	// The header: target, kind, actor, elapsed and last-output age.
	for _, want := range []string{"#42 Retry webhook deliveries", "implement", "claude", "12m/1h", "4s"} {
		if !strings.Contains(rows[0], want) {
			t.Errorf("header has no %q: %q", want, rows[0])
		}
	}
	// Eight rows of log: lines 23 to 30.
	if !hasRow(m, "line 30") || !hasRow(m, "line 23") || hasRow(m, "line 22") {
		t.Fatalf("not at the end:\n%s", uiView(m))
	}
	if !strings.Contains(rows[9], "following") || !strings.Contains(rows[9], "lines 23–30 of 30") {
		t.Fatalf("status line: %q", rows[9])
	}

	src.lines = uiLogLines(35)
	if next := pollLogOnce(t, m); next == nil {
		t.Fatal("a live log's poll did not schedule the next")
	}
	if !hasRow(m, "line 35") || hasRow(m, "line 27") {
		t.Fatalf("the grown log is not followed:\n%s", uiView(m))
	}
}

// Scrolling up stops following; f resumes it; g and G go to the ends.
func TestUILogScrollStopsFollowing(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	setLive(m, oneItemLive(3))
	src := &fakeLog{lines: uiLogLines(30)}
	openLogOn(t, m, src)

	m.Update(uiKey("k"))
	if m.logv.follow {
		t.Fatal("scrolling up did not stop following")
	}
	if !hasRow(m, "line 22") || hasRow(m, "line 30") {
		t.Fatalf("k did not scroll up a line:\n%s", uiView(m))
	}
	if !strings.Contains(uiView(m), "paused") {
		t.Fatalf("the status does not say it is paused:\n%s", uiView(m))
	}
	// The log grows while paused: the view stays where it is.
	src.lines = uiLogLines(40)
	pollLogOnce(t, m)
	if !hasRow(m, "line 22") || hasRow(m, "line 40") {
		t.Fatalf("a paused view moved:\n%s", uiView(m))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if !hasRow(m, "line 14") || hasRow(m, "line 22") {
		t.Fatalf("pgup did not scroll a page:\n%s", uiView(m))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !hasRow(m, "line 22") || hasRow(m, "line 14") {
		t.Fatalf("down did not scroll a line:\n%s", uiView(m))
	}

	m.Update(uiKey("f"))
	if !m.logv.follow || !hasRow(m, "line 40") {
		t.Fatalf("f did not resume following:\n%s", uiView(m))
	}
	m.Update(uiKey("f"))
	if m.logv.follow {
		t.Fatal("f did not toggle following off")
	}

	m.Update(uiKey("g"))
	if !hasRow(m, "line 1") || m.logv.follow {
		t.Fatalf("g did not go to the top:\n%s", uiView(m))
	}
	m.Update(uiKey("G"))
	if !hasRow(m, "line 40") || !m.logv.follow {
		t.Fatalf("G did not go to the end and follow:\n%s", uiView(m))
	}
}

// A running tool call is replaced by its completed line, in place.
func TestUILogToolCallCompletes(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	setLive(m, oneItemLive(3))
	call := runs.LogLine{Kind: runs.LineTool, Tool: "Bash", ToolClass: runs.ToolShell, State: "running", Text: "Bash: make check"}
	src := &fakeLog{lines: []runs.LogLine{{Kind: runs.LineText, Text: "Running checks"}, call}}
	openLogOn(t, m, src)
	if !hasRow(m, "Bash make check · running") {
		t.Fatalf("no running call:\n%s", uiView(m))
	}
	call.State = "completed"
	src.lines[1] = call
	pollLogOnce(t, m)
	if !hasRow(m, "Bash make check · completed") || strings.Contains(uiView(m), "· running") {
		t.Fatalf("the call's line was not replaced:\n%s", uiView(m))
	}
	if len(m.logv.rows) != 2 {
		t.Fatalf("rows = %q, want the two lines", m.logv.rows)
	}
}

// Esc returns to the panes with the same selection; Live refreshing under
// the log view keeps it too.
func TestUILogEscRestoresSelection(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	live := webui.Live{
		Items: []webui.LiveItem{uiItem("r-1", "#42", "Retry", 3), uiItem("r-2", "!57", "Rate-limit", 3)},
		Rows:  []webui.LiveRow{{Key: "flow-53", Kind: "flow", Target: "#53", What: "waiting for actor"}},
	}
	setLive(m, live)
	m.Update(uiKey("j"))
	openLogOn(t, m, &fakeLog{lines: uiLogLines(3)})
	if m.logv.item.ID != "r-2" || !strings.Contains(viewRows(m)[0], "!57 Rate-limit") {
		t.Fatalf("opened %q:\n%s", m.logv.item.ID, uiView(m))
	}
	// j scrolls the log, not the selection.
	m.Update(uiKey("j"))
	// An item starting before the selected one moves it down a place.
	live.Items = append([]webui.LiveItem{uiItem("r-0", "#40", "Earlier", 3)}, live.Items...)
	setLive(m, live)

	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.logv != nil {
		t.Fatal("esc did not leave the log")
	}
	if m.data.Items[m.sel].ID != "r-2" || !strings.Contains(uiView(m), "▸ !57") {
		t.Fatalf("selection after esc: %d %q\n%s", m.sel, m.keyAt(m.sel), uiView(m))
	}
	// A poll that comes back after esc is dropped.
	if _, cmd := m.Update(uiLogMsg{id: "r-2", changed: true}); cmd != nil || m.logv != nil {
		t.Fatal("a late poll reopened the log")
	}
}

// A run that ends while followed: the view stays on its log, polls a last
// time, says how it ended, and stops polling; its elapsed time stops.
func TestUILogRunEndsWhileFollowed(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	setLive(m, oneItemLive(3))
	src := &fakeLog{lines: uiLogLines(5)}
	openLogOn(t, m, src)
	var asked string
	m.runEnd = func(id string) (string, string, bool) {
		asked = id
		return "succeeded", "pr_opened", true
	}

	setLive(m, webui.Live{})
	if m.logv == nil || !m.logv.ended {
		t.Fatal("the view did not see the run end")
	}
	src.lines = uiLogLines(6) // the transcript appended at the end
	m.now = func() time.Time { return uiNow.Add(5 * time.Minute) }
	if next := pollLogOnce(t, m); next != nil {
		t.Fatal("the final poll scheduled another")
	}
	if asked != "r-1" {
		t.Fatalf("asked how %q ended", asked)
	}
	v := uiView(m)
	if !hasRow(m, "line 6") || !strings.Contains(v, "run ended: succeeded (pr opened)") {
		t.Fatalf("the ended view:\n%s", v)
	}
	if !strings.Contains(viewRows(m)[0], "12m/1h") {
		t.Fatalf("the header's elapsed kept counting: %q", viewRows(m)[0])
	}
	// A run the server cannot say about still says it ended.
	m.logv.status = ""
	if !strings.Contains(uiView(m), "run ended") {
		t.Fatalf("no end:\n%s", uiView(m))
	}
}

// A run that ends while a poll waits for its tick is polled once more by
// that tick; one that ends with a poll in flight, by the poll's result.
func TestUILogEndsWithPollInFlight(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	setLive(m, oneItemLive(3))
	m.logSource = func(string) (uiLogSource, error) { return &fakeLog{lines: uiLogLines(2)}, nil }
	m.runEnd = func(string) (string, string, bool) { return "failed", "", true }
	_, open := m.Update(uiEnter)
	opened := open() // the first poll, not yet delivered
	setLive(m, webui.Live{})
	_, final := m.Update(opened)
	if final == nil {
		t.Fatal("no final poll after the run ended")
	}
	if _, next := m.Update(final()); next != nil {
		t.Fatal("the final poll scheduled another")
	}
	if !strings.Contains(uiView(m), "run ended: failed") {
		t.Fatalf("the ended view:\n%s", uiView(m))
	}
}

// A very long line is cut to the width, not wrapped into the next row.
func TestUILogLongLineCut(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
	setLive(m, oneItemLive(3))
	long := runs.LogLine{Kind: runs.LineText, Text: strings.Repeat("x", 200) + "\tEND"}
	openLogOn(t, m, &fakeLog{lines: []runs.LogLine{long, {Kind: runs.LineText, Text: "after"}}})
	rows := viewRows(m)
	if len(rows) != 8 {
		t.Fatalf("view has %d rows, want 8:\n%s", len(rows), uiView(m))
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w > 40 {
			t.Errorf("row %d is %d wide: %q", i, w, r)
		}
	}
	if !strings.HasSuffix(rows[1], "…") || strings.Contains(uiView(m), "END") {
		t.Fatalf("the long line was not cut: %q", rows[1])
	}
	if rows[2] != "after" {
		t.Fatalf("the next row is %q, want the next line", rows[2])
	}
}

// A row has no log: Enter on it does nothing, and the hint says so.
func TestUILogRowHasNoLog(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	setLive(m, webui.Live{
		Items: []webui.LiveItem{uiItem("r-1", "#42", "Retry", 3)},
		Rows:  []webui.LiveRow{{Key: "r-9", Kind: "interactive", Target: "#61", What: "interactive", Detail: "implement · claude"}},
	})
	if strings.Contains(uiView(m), "no log") {
		t.Fatalf("an item's hint says no log:\n%s", uiView(m))
	}
	m.Update(uiKey("j"))
	v := uiView(m)
	if !strings.Contains(v, "▸ #61") || !strings.Contains(v, "no log for this row") {
		t.Fatalf("the selected row:\n%s", v)
	}
	if _, cmd := m.Update(uiEnter); cmd != nil || m.logv != nil {
		t.Fatal("enter on a row opened a log")
	}
	// j stops at the last row.
	m.Update(uiKey("j"))
	if m.sel != 1 {
		t.Fatalf("sel = %d, want 1", m.sel)
	}
}

// The source failing to open says so in place of the log.
func TestUILogOpenError(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	setLive(m, oneItemLive(3))
	m.logSource = func(string) (uiLogSource, error) { return nil, fmt.Errorf("invalid run ID") }
	_, cmd := m.Update(uiEnter)
	if _, next := m.Update(cmd()); next != nil {
		t.Fatal("a failed open scheduled a poll")
	}
	if !strings.Contains(uiView(m), "error: invalid run ID") {
		t.Fatalf("no error:\n%s", uiView(m))
	}
	if _, cmd := m.Update(uiKey("q")); cmd == nil {
		t.Fatal("q does not quit from the log")
	}
}
