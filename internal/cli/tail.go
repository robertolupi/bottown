package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/runs"
)

// The live tail of batch runs in the terminal (docs/runs.md): a bubbletea
// program in the terminal's normal screen, not the alternate one. Lines
// of the run's log that are final (runs.LineTracker), and every line the
// launcher or the flow writes, go to the terminal's scrollback; the view
// below them keeps a header and the lines still live, the calls running
// with a spinner. Nothing is kept only in the view: when the run ends,
// all its lines are in the scrollback, so a failure stays readable there.

// maxLiveLines bounds the live lines the view shows; the oldest are
// counted instead.
const maxLiveLines = 8

// terminalOut is the command's output if it is a terminal the live tail
// can use: not redirected, and not a dumb terminal.
func terminalOut(cmd *cobra.Command) (*os.File, bool) {
	f, ok := cmd.OutOrStdout().(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) || os.Getenv("TERM") == "dumb" {
		return nil, false
	}
	return f, true
}

// tailRun is the run the tail follows, for its header. A flow's
// post-implement checks are followed like a run: Check is their commands,
// and they have no actor.
type tailRun struct {
	ID, Kind, Target, Actor, Family string
	Check                           string
	Started                         time.Time
	Limit                           time.Duration // the run's time limit, if known
}

// liveTail is the program and the writer the launcher and the flow print
// through while it runs. Writes are split into lines, printed above the
// view; while the terminal is released for a prompt (Pause), and once the
// program has ended, they go to the terminal as they are.
type liveTail struct {
	p      *tea.Program
	term   io.Writer
	exited chan struct{}

	mu     sync.Mutex
	buf    []byte // a partial line written through Write
	paused bool

	follow struct {
		cancel context.CancelFunc
		done   chan struct{}
		f      *runs.LogFollower
	}
}

// newLiveTail starts the tail's program on out, a terminal.
func newLiveTail(out *os.File) *liveTail {
	m := newTailModel(newStyles())
	// Input is read only for ctrl+c: the terminal is in raw mode, so it
	// does not raise SIGINT itself. Sending it keeps the launcher's own
	// handling (main's signal context), as without the tail.
	m.interrupt = func() { _ = syscall.Kill(os.Getpid(), syscall.SIGINT) }
	t := &liveTail{term: out, exited: make(chan struct{})}
	t.p = tea.NewProgram(m, tea.WithOutput(out), tea.WithoutSignalHandler())
	go func() {
		defer close(t.exited)
		if _, err := t.p.Run(); err != nil {
			fmt.Fprintf(out, "warning: live tail: %v\n", err)
		}
	}()
	return t
}

// send delivers msg to the program, unless it has ended.
func (t *liveTail) send(msg tea.Msg) bool {
	select {
	case <-t.exited:
		return false
	default:
	}
	t.p.Send(msg)
	return true
}

// Write prints whole lines above the view, and keeps a partial one until
// its newline, or a Pause or Close, comes.
func (t *liveTail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.paused {
		return t.term.Write(b)
	}
	t.buf = append(t.buf, b...)
	i := strings.LastIndexByte(string(t.buf), '\n')
	if i < 0 {
		return len(b), nil
	}
	lines := strings.Split(string(t.buf[:i]), "\n")
	t.buf = append(t.buf[:0], t.buf[i+1:]...)
	if !t.send(printMsg{lines: lines, note: true}) {
		_, err := io.WriteString(t.term, strings.Join(lines, "\n")+"\n")
		return len(b), err
	}
	return len(b), nil
}

// flushPartial prints a partial line written through Write; t.mu is held.
func (t *liveTail) flushPartial() {
	if len(t.buf) == 0 {
		return
	}
	s := string(t.buf)
	t.buf = t.buf[:0]
	if !t.send(printMsg{lines: []string{s}, note: true}) {
		_, _ = io.WriteString(t.term, s)
	}
}

// drain waits until every line sent so far is printed; with pause, the
// view is then empty until resumeMsg.
func (t *liveTail) drain(pause bool) {
	done := make(chan struct{})
	if !t.send(drainMsg{done: done, pause: pause}) {
		return
	}
	select {
	case <-done:
	case <-t.exited:
		return
	}
	// Send returns once the program takes the message: by then it has
	// rendered the view the drain left.
	t.send(syncMsg{})
}

// setFlow sets the flow's state in the header ("" for none).
func (t *liveTail) setFlow(label string) { t.send(flowMsg(label)) }

// Follow follows run's log at logPath until the returned function is
// called, once the run has ended and its log is complete; that prints the
// lines left. Calling it again does nothing.
func (t *liveTail) Follow(run tailRun, harness, logPath string) (ended func()) {
	t.send(attachMsg(run))
	ctx, cancel := context.WithCancel(context.Background())
	f := &runs.LogFollower{Path: logPath, Harness: harness}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Follow(ctx, runs.FollowLogEvery, func(lines []runs.LogLine) {
			t.send(linesMsg{lines: lines})
		})
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			lines, _ := f.Poll()
			t.send(linesMsg{lines: lines, ended: true})
			t.drain(false)
		})
	}
}

// Pause releases the terminal, for a prompt that reads stdin, until the
// returned function is called: flow.Supervisor.Pause.
func (t *liveTail) Pause() (resume func()) {
	t.mu.Lock()
	t.flushPartial()
	t.mu.Unlock()
	t.drain(true)
	select {
	case <-t.exited:
		return func() {}
	default:
	}
	if err := t.p.ReleaseTerminal(); err != nil {
		fmt.Fprintf(t.term, "warning: live tail: %v\n", err)
	}
	t.mu.Lock()
	t.paused = true
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		t.paused = false
		t.mu.Unlock()
		if err := t.p.RestoreTerminal(); err != nil {
			fmt.Fprintf(t.term, "warning: live tail: %v\n", err)
		}
		t.send(resumeMsg{})
	}
}

// Close prints what is left and ends the program, leaving the terminal
// as it was, with everything printed in its scrollback.
func (t *liveTail) Close() {
	t.mu.Lock()
	t.flushPartial()
	t.mu.Unlock()
	if t.send(quitMsg{}) {
		<-t.exited
	}
}

type (
	printMsg struct {
		lines []string
		note  bool // the launcher's or the flow's own line, shown between runs
	}
	printedMsg struct{}
	linesMsg   struct {
		lines []runs.LogLine
		ended bool
	}
	attachMsg tailRun
	flowMsg   string
	drainMsg  struct {
		done  chan struct{}
		pause bool
	}
	syncMsg   struct{}
	resumeMsg struct{}
	quitMsg   struct{}
)

// tailModel is the tail's state. Lines to print queue up, and one
// tea.Println prints them at a time: bubbletea runs commands
// concurrently, so separate ones could print out of order.
type tailModel struct {
	st        *styles
	spin      spinner.Model
	width     int
	now       func() time.Time
	interrupt func()
	println   func(string) tea.Cmd // tea.Println; tests see the scrollback

	flow    string   // the flow's state, if a flow runs
	note    string   // the last line the launcher or the flow printed
	run     *tailRun // nil between runs
	tracker runs.LineTracker
	live    []runs.LogLine

	queue    []string
	printing bool
	drains   []drainMsg
	paused   bool
	quitting bool
}

func newTailModel(st *styles) *tailModel {
	return &tailModel{
		st:        st,
		spin:      spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(st.Info)),
		now:       time.Now,
		interrupt: func() {},
		println:   func(s string) tea.Cmd { return tea.Println(s) },
	}
}

func (m *tailModel) Init() tea.Cmd { return m.spin.Tick }

func (m *tailModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.interrupt()
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case printMsg:
		m.queue = append(m.queue, msg.lines...)
		if msg.note {
			for i := len(msg.lines) - 1; i >= 0; i-- {
				if s := strings.TrimSpace(msg.lines[i]); s != "" {
					m.note = s
					break
				}
			}
		}
	case printedMsg:
		m.printing = false
	case attachMsg:
		run := tailRun(msg)
		m.run, m.tracker, m.live = &run, runs.LineTracker{}, nil
	case linesMsg:
		if m.run == nil {
			return m, nil
		}
		final, live := m.tracker.Update(msg.lines, msg.ended)
		for _, l := range final {
			m.queue = append(m.queue, m.st.logLine(l, true, "  "))
		}
		m.live = live
		if msg.ended {
			m.run, m.live = nil, nil
		}
	case flowMsg:
		m.flow = string(msg)
	case drainMsg:
		m.drains = append(m.drains, msg)
	case syncMsg:
	case resumeMsg:
		m.paused = false
	case quitMsg:
		m.quitting = true
	}
	return m, m.next()
}

// next prints the queued lines, if nothing is printing, and otherwise
// answers the drains and the quit waiting for the queue to empty.
func (m *tailModel) next() tea.Cmd {
	if m.printing {
		return nil
	}
	if len(m.queue) > 0 {
		s := strings.Join(m.queue, "\n")
		m.queue, m.printing = nil, true
		return tea.Sequence(m.println(s), func() tea.Msg { return printedMsg{} })
	}
	for _, d := range m.drains {
		m.paused = m.paused || d.pause
		close(d.done)
	}
	m.drains = nil
	if m.quitting {
		return tea.Quit
	}
	return nil
}

func (m *tailModel) View() tea.View {
	if m.paused || m.quitting {
		return tea.NewView("")
	}
	var b strings.Builder
	line := func(s string) {
		if m.width > 0 {
			s = ansi.Truncate(s, m.width, "…")
		}
		b.WriteString(s + "\n")
	}
	spin := m.spin.View() + " "
	if m.flow != "" {
		line(spin + m.st.Bold.Render("flow "+m.flow))
		spin = "  "
	}
	if m.run == nil {
		if m.note != "" {
			line(spin + m.st.Dim.Render(m.note))
		}
		return tea.NewView(b.String())
	}
	line(spin + m.runHeader())
	live := m.live
	if n := len(live) - maxLiveLines; n > 0 {
		line(m.st.Dim.Render(fmt.Sprintf("    … %d more", n)))
		live = live[n:]
	}
	for _, l := range live {
		mark := "  "
		if l.Kind == runs.LineTool && l.State == "running" {
			mark = m.spin.View() + " "
		}
		line("  " + mark + m.st.logLine(l, false, ""))
	}
	return tea.NewView(b.String())
}

// runHeader is the followed run: "run r-… · implement #12 · claude
// (anthropic) · 3m12s of 1h00m", or the flow's checks: "checks
// flow-12-….e2e · !30 at 1a2b3c4d5e · make e2e · 3m12s of 1h00m".
func (m *tailModel) runHeader() string {
	r := m.run
	elapsed := clockDuration(m.now().Sub(r.Started))
	if r.Limit > 0 {
		elapsed += " of " + clockDuration(r.Limit)
	}
	dot := m.st.Dim.Render(" · ")
	if r.Check != "" {
		return "checks " + m.st.Info.Bold(true).Render(r.ID) + dot + r.Target + dot +
			r.Check + dot + m.st.Dim.Render(elapsed)
	}
	return "run " + m.st.Info.Bold(true).Render(r.ID) + dot + r.Kind + " " + r.Target + dot +
		r.Actor + " (" + r.Family + ")" + dot + m.st.Dim.Render(elapsed)
}

// clockDuration is d to the second: 12s, 3m12s, 1h05m.
func clockDuration(d time.Duration) string {
	d = max(d, 0).Truncate(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// logLine renders a log line in the web UI's palette (LogLineView.svelte):
// lines by kind, tool names by class, their argument and state muted. full
// prints an assistant message or error whole, for the scrollback; the view
// keeps to its first line. Every line of a multi-line one starts with
// indent.
func (s *styles) logLine(l runs.LogLine, full bool, indent string) string {
	text := l.Text
	if full && l.Detail != "" && (l.Kind == runs.LineText || l.Kind == runs.LineError) {
		text = l.Detail
	}
	text = cleanTerminalText(text)
	var out string
	switch l.Kind {
	case runs.LineTool:
		name, arg := l.Tool, ""
		if name != "" && strings.HasPrefix(text, name) {
			arg = strings.TrimLeft(strings.TrimPrefix(text[len(name):], ":"), " ")
		} else {
			name, arg = text, ""
		}
		out = s.toolName(l.ToolClass).Render(name)
		if arg != "" {
			out += " " + s.Dim.Render(arg)
		}
		switch l.State {
		case "failed":
			out += " " + s.Danger.Render("· failed")
		case "":
		default:
			out += " " + s.Dim.Render("· "+l.State)
		}
	case runs.LineOutput:
		out = s.Dim.Render(text)
	case runs.LineError:
		out = s.Danger.Render(text)
	case runs.LineStatus:
		out = s.Dim.Italic(true).Render(text)
	default:
		out = text
	}
	return indent + strings.ReplaceAll(out, "\n", "\n"+indent)
}

// toolName is the colour of a tool's name by its class, as in the web
// UI's legend, in the terminal's own ANSI colours.
func (s *styles) toolName(class string) lipgloss.Style {
	switch class {
	case runs.ToolShell:
		return s.Success
	case runs.ToolRead:
		return lipgloss.NewStyle().Foreground(lipgloss.Blue) // ANSI blue
	case runs.ToolEdit:
		return s.Warning
	case runs.ToolSearch:
		return s.Info
	case runs.ToolForge:
		return s.Purple
	case runs.ToolBrowser:
		return lipgloss.NewStyle().Foreground(lipgloss.BrightMagenta) // ANSI bright magenta
	default:
		return s.Bold
	}
}

// cleanTerminalText drops what a log line must not send to the terminal:
// escape sequences, and control characters other than newline and tab.
func cleanTerminalText(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
