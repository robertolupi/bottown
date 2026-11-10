package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/webui"
)

// The terminal live view's tests (monitor-ui design §8). They drive the
// model with a fake Live, as tail_test.go drives the tail's model.

var uiNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func testUIModel() *uiModel {
	m := newUIModel("http://127.0.0.1:1/?token=tok", "bottown/bottown", "prod", nil)
	m.now = func() time.Time { return uiNow }
	return m
}

func ptrTime(t time.Time) *time.Time { return &t }

// uiKey is a printable key press.
func uiKey(s string) tea.KeyPressMsg {
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func uiLogLines(n int) []runs.LogLine {
	out := make([]runs.LogLine, n)
	for i := range out {
		out[i] = runs.LogLine{Kind: runs.LineText, Text: fmt.Sprintf("line %d", i+1)}
	}
	return out
}

// uiItem is a live batch run with tail rendered log lines.
func uiItem(id, target, title string, tail int) webui.LiveItem {
	last := uiNow.Add(-4 * time.Second)
	return webui.LiveItem{
		ID: id, Kind: "implement", Target: target, Title: title,
		Actor: "claude", Family: "anthropic", Started: uiNow.Add(-12 * time.Minute),
		Limit: 3600, LastOutput: &last, Tail: uiLogLines(tail),
	}
}

// setLive installs a Live as the fake source and feeds it to the model.
func setLive(m *uiModel, l webui.Live) {
	m.live = func(int) (webui.Live, error) { return l, nil }
	m.Update(m.fetch()())
}

func uiView(m *uiModel) string { return stripANSI(m.View().Content) }

// The even split: one, three and seven items, the remainder going to the
// first panes; the three-line minimum and the scroll with its count.
func TestUISplitPanes(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		n, avail, sel, top            int
		wantLines                     []int
		wantStart, wantCount, wantOff int
	}{
		{name: "one", n: 1, avail: 10, wantLines: []int{10}, wantCount: 1},
		{name: "three, remainder to the first", n: 3, avail: 20, wantLines: []int{7, 7, 6}, wantCount: 3},
		{name: "seven, remainder to the first", n: 7, avail: 31, wantLines: []int{5, 5, 5, 4, 4, 4, 4}, wantCount: 7},
		{name: "scroll", n: 7, avail: 20, sel: 6, top: 0, wantLines: []int{4, 4, 4, 4, 4}, wantStart: 2, wantCount: 5, wantOff: 2},
		{name: "shorter than one pane", n: 3, avail: 3, wantLines: []int{}, wantCount: 0, wantOff: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, count, lines, off, _ := splitPanes(tc.n, tc.avail, tc.sel, tc.top)
			if fmt.Sprint(lines) != fmt.Sprint(tc.wantLines) {
				t.Errorf("lines = %v, want %v", lines, tc.wantLines)
			}
			if start != tc.wantStart || count != tc.wantCount || off != tc.wantOff {
				t.Errorf("start/count/off = %d/%d/%d, want %d/%d/%d", start, count, off, tc.wantStart, tc.wantCount, tc.wantOff)
			}
			for _, n := range lines {
				if n < uiMinPane {
					t.Errorf("pane of %d lines is under the minimum %d", n, uiMinPane)
				}
			}
		})
	}
}

// The model's split at a height, recomputed on resize.
func TestUIPaneSplitAtHeight(t *testing.T) {
	m := testUIModel()
	three := webui.Live{Items: []webui.LiveItem{uiItem("r-1", "#1", "one", 3), uiItem("r-2", "#2", "two", 3), uiItem("r-3", "#3", "three", 3)}}
	setLive(m, three)

	// Height 20: header, summary and the hint take three lines, leaving 17
	// for three panes: 6, 6, 5.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	_, count, lines, off, _ := m.panes()
	if count != 3 || off != 0 || fmt.Sprint(lines) != fmt.Sprint([]int{6, 6, 5}) {
		t.Fatalf("at 20 lines: count=%d off=%d lines=%v", count, off, lines)
	}

	// A taller terminal gives every pane more.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if _, _, lines, _, _ := m.panes(); fmt.Sprint(lines) != fmt.Sprint([]int{13, 12, 12}) {
		t.Fatalf("at 40 lines: lines=%v", lines)
	}
}

func TestUIView(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	live := webui.Live{
		Items: []webui.LiveItem{
			uiItem("r-1", "#42", "Retry webhook deliveries", 3),
			uiItem("r-2", "!57", "Rate-limit the public API", 3),
		},
		Rows: []webui.LiveRow{{
			Key: "flow-53", Kind: "flow", Target: "#53", Title: "Usage dashboard endpoint",
			What: "waiting for actor", Detail: "waiting for agy (busy with #47)",
			Since: ptrTime(uiNow.Add(-6 * time.Minute)),
		}},
		Summary: webui.LiveSummary{
			Running: 2, Waiting: 1,
			Inbox: map[webui.InboxItemKind]int{
				webui.InboxReadyToLand: 1, webui.InboxBrokenRun: 2, webui.InboxConflictsWithBase: 1,
			},
		},
	}
	setLive(m, live)
	v := uiView(m)

	for _, want := range []string{
		"bottown · bottown/bottown · prod",
		"web http://127.0.0.1:1/?token=tok",
		"2 running · 0 quiet · 1 waiting",
		"needs you: 1 ready to land, 2 failed, 1 conflict",
		"#42 Retry webhook deliveries",
		"!57 Rate-limit the public API",
		"line 3",
		"#53 Usage dashboard endpoint",
		"j/k move",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view has no %q:\n%s", want, v)
		}
	}
	// The selected item's rule carries the marker.
	if !strings.Contains(v, "▸ #42") {
		t.Errorf("selected pane's rule has no marker:\n%s", v)
	}
}

// A quiet pane's rule reads "no output Nm".
func TestUIQuietPane(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	it := uiItem("r-1", "!57", "Rate-limit the public API", 3)
	it.Quiet = true
	it.LastOutput = ptrTime(uiNow.Add(-6 * time.Minute))
	setLive(m, webui.Live{Items: []webui.LiveItem{it}, Summary: webui.LiveSummary{Running: 1, Quiet: 1}})
	v := uiView(m)
	if !strings.Contains(v, "no output 6m") {
		t.Fatalf("quiet pane:\n%s", v)
	}
	if !strings.Contains(v, "1 running · 1 quiet · 0 waiting") {
		t.Fatalf("summary:\n%s", v)
	}
}

// When the items do not fit, the panes scroll with the selection and the
// summary says how many are off screen.
func TestUIScrollAndOffscreen(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	items := make([]webui.LiveItem, 7)
	for i := range items {
		items[i] = uiItem(fmt.Sprintf("r-%d", i+1), fmt.Sprintf("#%d", i+1), fmt.Sprintf("issue %d", i+1), 3)
	}
	setLive(m, webui.Live{Items: items, Summary: webui.LiveSummary{Running: 7}})
	if v := uiView(m); !strings.Contains(v, "5 off screen") {
		t.Fatalf("summary should count the off-screen panes:\n%s", v)
	}

	// Moving the selection to the last item scrolls the panes to it.
	for range 6 {
		m.Update(uiKey("j"))
	}
	if m.sel != 6 || m.top != 5 {
		t.Fatalf("after moving to the end: sel=%d top=%d", m.sel, m.top)
	}
	v := uiView(m)
	if !strings.Contains(v, "#7 issue 7") {
		t.Fatalf("the selected item is not on screen:\n%s", v)
	}
	if !strings.Contains(v, "▸ #7") {
		t.Fatalf("the selected rule has no marker:\n%s", v)
	}
}

// An item ending while selected leaves the selection in range; an item
// ending under it follows the selected item by id.
func TestUIItemEndsWhileSelected(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a, b, c := uiItem("r-a", "#1", "a", 3), uiItem("r-b", "#2", "b", 3), uiItem("r-c", "#3", "c", 3)
	setLive(m, webui.Live{Items: []webui.LiveItem{a, b, c}})

	// The selected item (the last) ends: the selection stays in range.
	m.Update(uiKey("j"))
	m.Update(uiKey("j"))
	if m.sel != 2 {
		t.Fatalf("sel = %d, want 2", m.sel)
	}
	setLive(m, webui.Live{Items: []webui.LiveItem{a, b}})
	if m.sel != 1 || m.data.Items[m.sel].ID != "r-b" {
		t.Fatalf("after the selected item ended: sel=%d item=%q", m.sel, m.data.Items[m.sel].ID)
	}

	// An item ending under the selection follows the selected item.
	setLive(m, webui.Live{Items: []webui.LiveItem{a, b, c}})
	m.Update(uiKey("k"))
	m.Update(uiKey("k"))
	if m.data.Items[m.sel].ID != "r-a" {
		t.Fatalf("setup: selected %q, want r-a", m.data.Items[m.sel].ID)
	}
	setLive(m, webui.Live{Items: []webui.LiveItem{a, c}})
	if m.sel != 0 || m.data.Items[m.sel].ID != "r-a" {
		t.Fatalf("the selection did not follow its item: sel=%d item=%q", m.sel, m.data.Items[m.sel].ID)
	}
}

func TestUIKeys(t *testing.T) {
	m := testUIModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	setLive(m, webui.Live{Items: []webui.LiveItem{uiItem("r-1", "#1", "one", 3)}})

	var opened string
	m.open = func(url string) error { opened = url; return nil }
	m.Update(uiKey("o"))
	if opened != m.loginURL {
		t.Errorf("o opened %q, want %q", opened, m.loginURL)
	}

	if _, cmd := m.Update(uiKey("?")); cmd != nil {
		t.Errorf("? returned a command")
	}
	if !m.showKeys || !strings.Contains(uiView(m), "quit and stop the server") {
		t.Fatalf("? should list the keys:\n%s", uiView(m))
	}
	m.Update(uiKey("?"))
	if m.showKeys {
		t.Errorf("? should toggle the keys off")
	}

	if _, cmd := m.Update(uiKey("q")); cmd == nil {
		t.Errorf("q should quit")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q did not end the program")
	}
}

// TestUICommandAndAlias checks `bottown ui` exists with its flags, and that
// `bottown web ui` is an alias carrying the same ones.
func TestUICommandAndAlias(t *testing.T) {
	root := newRoot()
	ui, _, err := root.Find([]string{"ui"})
	if err != nil || ui.Name() != "ui" {
		t.Fatalf("bottown ui: %v", err)
	}
	webUI, _, err := root.Find([]string{"web", "ui"})
	if err != nil || webUI.Name() != "ui" {
		t.Fatalf("bottown web ui: %v", err)
	}
	for _, name := range []string{"port", "print", "once", "repo"} {
		if ui.Flags().Lookup(name) == nil {
			t.Errorf("bottown ui has no --%s", name)
		}
		if webUI.Flags().Lookup(name) == nil {
			t.Errorf("bottown web ui has no --%s", name)
		}
	}
}

// --once renders one frame: the summary, each item's header with its last
// three log lines, then the rows. No ANSI sequences without a terminal.
func TestUIRenderOnce(t *testing.T) {
	m := testUIModel()
	second := uiItem("r-2", "!57", "Rate-limit the public API", 0)
	second.Tail = []runs.LogLine{{Kind: runs.LineText, Text: "b1"}, {Kind: runs.LineText, Text: "b2"}, {Kind: runs.LineText, Text: "b3"}}
	live := webui.Live{
		Items: []webui.LiveItem{
			uiItem("r-1", "#42", "Retry webhook deliveries", 5),
			second,
		},
		Rows:    []webui.LiveRow{{Key: "flow-53", Kind: "flow", Target: "#53", Title: "Usage dashboard endpoint", What: "waiting for actor", Since: ptrTime(uiNow.Add(-6 * time.Minute))}},
		Summary: webui.LiveSummary{Running: 2, Waiting: 1},
	}
	m.data = live

	plain := m.renderOnce(false)
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("--once without a terminal must have no ANSI sequences:\n%q", plain)
	}
	for _, want := range []string{
		"2 running · 0 quiet · 1 waiting",
		"#42 Retry webhook deliveries · implement · claude · 12m/1h · 4s",
		"line 3", "line 4", "line 5",
		"#53 Usage dashboard endpoint",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("--once has no %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "line 1") || strings.Contains(plain, "line 2") {
		t.Errorf("--once should show only the last three lines of a tail:\n%s", plain)
	}
	if color := m.renderOnce(true); !strings.Contains(color, "\x1b") {
		t.Errorf("--once on a terminal should keep the styles")
	}
}
