package cli

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/webui"
)

// The full-screen log of `bottown ui` (monitor-ui design §8): Enter on a
// selected item shows its log, read from the run's or check's log file by
// a runs.LogFollower and rendered as the terminal tail renders it. It
// follows the end while the item is live, and stays on the log, saying how
// it ended, once it is gone from Live.

// uiLogSource is where the log view reads its lines: a *runs.LogFollower,
// or a fake in tests. Poll is called from one command at a time.
type uiLogSource interface {
	Poll() ([]runs.LogLine, bool)
}

// uiLogView is the full-screen log's state.
type uiLogView struct {
	item webui.LiveItem // as Live last reported it
	src  uiLogSource
	err  error

	lines []runs.LogLine
	rows  []string // lines rendered, one per screen row, before cutting
	top   int      // the first row on screen
	// follow keeps the end on screen as the log grows.
	follow bool

	// ended is set when the item is gone from Live; final once the last
	// poll after that has come back, and the view stops polling.
	ended   bool
	endedAt time.Time
	final   bool
	status  string // a run's status when it ended, if the server says
	outcome string
}

type (
	// uiLogMsg is a poll's result. final is the poll made after the item
	// ended, with how it ended.
	uiLogMsg struct {
		id      string
		src     uiLogSource
		err     error
		lines   []runs.LogLine
		changed bool

		final           bool
		status, outcome string
		endOK           bool
	}
	uiLogTickMsg struct{ id string }
)

// openLog shows item it's log full screen, following it.
func (m *uiModel) openLog(it webui.LiveItem) tea.Cmd {
	m.logv = &uiLogView{item: it, follow: true}
	id, open := it.ID, m.logSource
	return func() tea.Msg {
		if open == nil {
			return uiLogMsg{id: id}
		}
		src, err := open(id)
		if err != nil {
			return uiLogMsg{id: id, err: err}
		}
		lines, changed := src.Poll()
		return uiLogMsg{id: id, src: src, lines: lines, changed: changed}
	}
}

// pollLog returns a command that polls the view's source once; after the
// item ended, it is the final poll, which also asks how it ended.
func (m *uiModel) pollLog() tea.Cmd {
	v := m.logv
	id, src, final, runEnd := v.item.ID, v.src, v.ended, m.runEnd
	return func() tea.Msg {
		msg := uiLogMsg{id: id, src: src, final: final}
		if src != nil {
			msg.lines, msg.changed = src.Poll()
		}
		if final && runEnd != nil {
			msg.status, msg.outcome, msg.endOK = runEnd(id)
		}
		return msg
	}
}

// logMsg takes a poll's result, and schedules the next poll while the item
// is live; once it has ended, the next poll is the final one.
func (m *uiModel) logMsg(msg uiLogMsg) tea.Cmd {
	v := m.logv
	if v == nil || v.item.ID != msg.id {
		return nil // the view was left, or another item's opened since
	}
	v.src, v.err = msg.src, msg.err
	if msg.changed {
		v.lines = msg.lines
		m.renderLogRows()
	}
	if msg.final {
		v.final = true
		v.status, v.outcome = msg.status, msg.outcome
		if !msg.endOK {
			v.status, v.outcome = "", ""
		}
		return nil
	}
	if v.err != nil && !v.ended {
		return nil
	}
	if v.ended {
		return m.pollLog()
	}
	id := msg.id
	return tea.Tick(runs.FollowLogEvery, func(time.Time) tea.Msg { return uiLogTickMsg{id: id} })
}

// logLive updates the followed item from a new Live: its header while it is
// there, and the end once it is gone. The next poll, waiting for its tick,
// is then the final one.
func (m *uiModel) logLive() {
	v := m.logv
	if v == nil || v.ended {
		return
	}
	for _, it := range m.data.Items {
		if it.ID == v.item.ID {
			v.item = it
			return
		}
	}
	v.ended, v.endedAt = true, m.now()
}

// renderLogRows renders the log's lines into screen rows and keeps the
// scroll in range: at the end when following.
func (m *uiModel) renderLogRows() {
	v := m.logv
	v.rows = v.rows[:0]
	for _, l := range v.lines {
		s := strings.ReplaceAll(m.st.logLine(l, true, ""), "\t", "    ")
		v.rows = append(v.rows, strings.Split(s, "\n")...)
	}
	m.clampLog()
}

// logBody is how many rows of log the screen has: all but the header and
// the hint.
func (m *uiModel) logBody() int {
	if m.height <= 0 {
		return max(len(m.logv.rows), 1)
	}
	return max(m.height-2, 1)
}

func (m *uiModel) clampLog() {
	v := m.logv
	end := max(len(v.rows)-m.logBody(), 0)
	if v.follow {
		v.top = end
	}
	v.top = min(max(v.top, 0), end)
}

// scrollLog moves the view by d rows; moving up stops following.
func (m *uiModel) scrollLog(d int) {
	if d < 0 {
		m.logv.follow = false
	}
	m.logv.top += d
	m.clampLog()
}

// logKey handles a key in the log view.
func (m *uiModel) logKey(k tea.KeyPressMsg) tea.Cmd {
	v := m.logv
	switch k.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return tea.Quit
	case "esc":
		m.logv = nil
	case "f":
		v.follow = !v.follow
		m.clampLog()
	case "j", "down":
		m.scrollLog(1)
	case "k", "up":
		m.scrollLog(-1)
	case "pgdown", "space":
		m.scrollLog(m.logBody())
	case "pgup":
		m.scrollLog(-m.logBody())
	case "g", "home":
		m.scrollLog(-len(v.rows))
	case "G", "end":
		v.follow = true
		m.clampLog()
	}
	return nil
}

// renderLog is the full-screen log: the item's header, its rows from top,
// each cut to the width, and a line saying where the view is and how the
// item ended.
func (m *uiModel) renderLog() string {
	v := m.logv
	now := m.now()
	if v.ended {
		now = v.endedAt
	}
	var b strings.Builder
	line := func(s string) {
		b.WriteString(m.cut(s))
		b.WriteString("\n")
	}
	line(m.paneRule(v.item, "", now))
	body := m.logBody()
	switch {
	case v.err != nil:
		line(m.st.Danger.Render("error: " + v.err.Error()))
		body--
	case len(v.rows) == 0:
		line(m.st.Dim.Render("(no output yet)"))
		body--
	}
	shown := 0
	for i := v.top; i < len(v.rows) && shown < body; i++ {
		line(v.rows[i])
		shown++
	}
	if m.height > 0 {
		for ; shown < body; shown++ {
			b.WriteString("\n")
		}
	}
	line(m.logStatus())
	return b.String()
}

// logStatus is the log view's last line: following, paused or how the
// item ended; the rows on screen; the keys.
func (m *uiModel) logStatus() string {
	v, st := m.logv, m.st
	var state string
	switch {
	case v.ended:
		state = m.endedText()
	case v.follow:
		state = st.Info.Render("following")
	default:
		state = st.Warning.Render("paused")
	}
	pos := ""
	if n := len(v.rows); n > 0 {
		last := min(v.top+m.logBody(), n)
		pos = fmt.Sprintf("lines %d–%d of %d", v.top+1, last, n)
	}
	keys := "f follow · j/k pgup/pgdn g/G scroll · esc back · q quit"
	parts := []string{state}
	if pos != "" {
		parts = append(parts, st.Dim.Render(pos))
	}
	parts = append(parts, st.Dim.Render(keys))
	return strings.Join(parts, st.Dim.Render(" · "))
}

// endedText says how the followed item ended: a run's status and outcome
// as the server recorded them, or only that it ended.
func (m *uiModel) endedText() string {
	v, st := m.logv, m.st
	what := "run"
	if v.item.Kind == "check" || v.item.Kind == "e2e" {
		what = "checks"
	}
	if v.status == "" {
		return st.Bold.Render(what + " ended")
	}
	s := what + " ended: " + v.status
	if v.outcome != "" {
		s += " (" + strings.ReplaceAll(v.outcome, "_", " ") + ")"
	}
	if v.status == "succeeded" {
		return st.Success.Render(s)
	}
	return st.Danger.Render(s)
}
