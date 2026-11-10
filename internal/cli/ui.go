package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/webui"
)

// `bottown ui` (monitor-ui design §8): the local web UI server and, on a
// terminal, the live view of what is running now. The terminal view calls
// the server's Live in-process, so the two cannot disagree. `bottown web
// ui` is an alias of this command.

// uiSettings holds `bottown ui`'s flags.
type uiSettings struct {
	port  int
	print bool
	once  bool
	repo  string
}

func addUIFlags(cmd *cobra.Command, s *uiSettings) {
	cmd.Flags().IntVar(&s.port, "port", 0, "port to listen on (default: random free port)")
	cmd.Flags().BoolVar(&s.print, "print", false, "print the login URL and serve until interrupted, instead of the live view (a non-terminal output does this too)")
	cmd.Flags().BoolVar(&s.once, "once", false, "print one frame as plain text and exit without serving")
	cmd.Flags().StringVar(&s.repo, "repo", "", "repository name within the instance's org (default: this checkout)")
}

func newUI(g *globals) *cobra.Command {
	var s uiSettings
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Serve this checkout's web UI and, on a terminal, show the live view",
		Long: `Start the local web UI server for this checkout and show what is running
now in the same terminal. On a terminal it takes the alternate screen: a
header with the web address, a summary line (running, quiet, waiting, and
the inbox's counts), one pane per live run or check with its log's tail,
one line per flow or interactive run with no tail, and a key hint. It
redraws every two seconds, calling the server's live view in-process.

Panes split the height evenly, each showing its share of its log's tail;
a pane never has fewer than three log lines, and when the items do not fit
the panes scroll with the selection, the summary saying how many are off
screen. A pane that has written nothing for five minutes turns to the
warning colour and reads "no output Nm".

Keys: j/k and the arrows move the selection, over the panes and then the
rows; Enter shows the selected run's or check's log full screen; o opens
the web UI in this machine's browser, ? lists the keys, q and Ctrl-C quit
and stop the server. A row (a waiting flow, an interactive run) has no
log, and Enter on it does nothing.

The full-screen log has a header with the item's target, kind, actor,
elapsed and last-output age, then its log, rendered as the terminal tail
renders it, each line cut to the width. It follows the end while the item
is live: f turns following off and on, j/k, the arrows, PgUp/PgDn and
g/G scroll (scrolling up stops following, G resumes it), Esc returns to
the panes with the same selection, q quits. When the item ends the view
stays on its log and says how it ended.

The command does not open a browser by default. --print, or an output that
is not a terminal, prints the login URL and serves until interrupted.
--once prints one frame as plain text and exits without serving.

bottown web ui is an alias of bottown ui.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUI(cmd, g, s)
		},
	}
	addUIFlags(cmd, &s)
	return cmd
}

// newWebUIAlias is `bottown web ui`, an alias of `bottown ui`.
func newWebUIAlias(g *globals) *cobra.Command {
	var s uiSettings
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Alias of `bottown ui`",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUI(cmd, g, s)
		},
	}
	addUIFlags(cmd, &s)
	return cmd
}

// runUI starts the server and shows the requested view.
func runUI(cmd *cobra.Command, g *globals, s uiSettings) error {
	top, inst, org, name, key, err := ownerRepo(g, s.repo)
	if err != nil {
		return err
	}
	srv, err := webui.NewServer(webui.Config{
		Instance:       inst,
		RepoTop:        top,
		FullRepo:       org + "/" + name,
		OwnerKey:       key,
		Port:           s.port,
		CLIVersion:     Version,
		HarnessChecker: checkHarnessCurrent,
		GuideChecker:   guideStatus,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	m := newUIModel(srv.LoginURL(), org+"/"+name, inst.Name, func(tail int) (webui.Live, error) {
		return srv.Live(cmd.Context(), tail)
	})
	f, isTerm := terminalOut(cmd)

	// --once: one frame, no server.
	if s.once {
		live, err := srv.Live(cmd.Context(), uiOnceTail)
		if err != nil {
			return err
		}
		m.data = live
		fmt.Fprint(cmd.OutOrStdout(), m.renderOnce(isTerm))
		return nil
	}

	// --print, or no terminal to draw in: the login URL and serve.
	if s.print || !isTerm {
		fmt.Fprintln(cmd.OutOrStdout(), srv.LoginURL())
		fmt.Fprintf(cmd.ErrOrStderr(), "bottown ui listening on %s (press Ctrl+C to stop)\n", srv.BaseURL())
		return srv.Serve(cmd.Context())
	}

	// A terminal: serve in the background and draw the live view.
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	m.live = func(tail int) (webui.Live, error) { return srv.Live(ctx, tail) }
	m.open = openBrowser
	m.logSource = func(id string) (uiLogSource, error) {
		f, err := srv.LogFollower(ctx, id)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	m.runEnd = func(id string) (string, string, bool) { return srv.RunEnd(ctx, id) }
	prog := tea.NewProgram(m, tea.WithOutput(f), tea.WithoutSignalHandler(), tea.WithContext(ctx))
	if _, err := prog.Run(); err != nil {
		return err
	}
	return nil
}

// The terminal live view's constants.
const (
	// uiRefreshEvery is how often the view asks Live again.
	uiRefreshEvery = 2 * time.Second
	// uiMinPane is a pane's least lines: its rule and three log lines.
	uiMinPane = 4
	// uiOnceTail is how many log lines --once shows per item.
	uiOnceTail = 3
	// uiDefaultTail is the tail a pane asks for before it knows the
	// terminal's height.
	uiDefaultTail = 12
)

// uiModel is the terminal live view's state. `live` is the server's Live,
// called in-process; tests replace it with a fake.
type uiModel struct {
	st       *styles
	loginURL string
	repo     string
	instance string

	live func(tail int) (webui.Live, error)
	open func(url string) error
	// logSource opens an item's log for the full-screen view, and runEnd
	// says how a run ended (webui.Server's LogFollower and RunEnd).
	logSource func(id string) (uiLogSource, error)
	runEnd    func(id string) (status, outcome string, ok bool)

	now    func() time.Time
	width  int
	height int

	data webui.Live
	err  error

	sel      int
	selKey   string
	top      int
	showKeys bool
	quitting bool

	logv   *uiLogView // the full-screen log, when one is shown
	logGen int        // the openings of the log view so far
}

func newUIModel(loginURL, repo, instance string, live func(int) (webui.Live, error)) *uiModel {
	return &uiModel{
		st:       newStyles(),
		loginURL: loginURL,
		repo:     repo,
		instance: instance,
		live:     live,
		open:     func(string) error { return nil },
		now:      time.Now,
	}
}

// Message types. liveMsg carries a Live computed by a command, tickMsg
// schedules the next one.
type (
	uiLiveMsg struct {
		live webui.Live
		err  error
	}
	uiTickMsg struct{}
)

func (m *uiModel) Init() tea.Cmd { return m.fetch() }

// fetch returns a command that calls Live without touching the model: the
// tail it asks for is computed now, on the program's goroutine.
func (m *uiModel) fetch() tea.Cmd {
	tail, live := m.tailRequest(), m.live
	return func() tea.Msg {
		if live == nil {
			return uiLiveMsg{}
		}
		l, err := live(tail)
		return uiLiveMsg{live: l, err: err}
	}
}

func (m *uiModel) tick() tea.Cmd {
	if m.quitting {
		return nil
	}
	return tea.Tick(uiRefreshEvery, func(time.Time) tea.Msg { return uiTickMsg{} })
}

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.logv != nil {
			m.clampLog()
		}
		return m, nil
	case uiLiveMsg:
		m.data, m.err = msg.live, msg.err
		m.clampSelection()
		m.logLive(msg.err)
		return m, m.tick()
	case uiTickMsg:
		return m, m.fetch()
	case uiLogMsg:
		return m, m.logMsg(msg)
	case uiLogTickMsg:
		if m.logv == nil || m.logv.gen != msg.gen {
			return m, nil
		}
		return m, m.pollLog()
	case tea.KeyPressMsg:
		if m.logv != nil {
			return m, m.logKey(msg)
		}
		return m, m.key(msg)
	}
	return m, nil
}

// key handles one key press in the panes; nothing here changes a run.
// Enter shows the selected item's log full screen; a row has none.
func (m *uiModel) key(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return tea.Quit
	case "enter":
		if m.sel < len(m.data.Items) {
			return m.openLog(m.data.Items[m.sel])
		}
	case "?":
		m.showKeys = !m.showKeys
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "o":
		if m.open != nil && m.loginURL != "" {
			_ = m.open(m.loginURL)
		}
	}
	return nil
}

// The selection runs over the items, then the rows: selectable indexes
// 0..selectable()-1, an item's id or a row's key naming each.
func (m *uiModel) selectable() int { return len(m.data.Items) + len(m.data.Rows) }

func (m *uiModel) keyAt(i int) string {
	if i < len(m.data.Items) {
		return m.data.Items[i].ID
	}
	return m.data.Rows[i-len(m.data.Items)].Key
}

// paneSel is the selection the panes scroll to: a selected row keeps the
// last panes on screen.
func (m *uiModel) paneSel() int { return min(m.sel, max(len(m.data.Items)-1, 0)) }

// move changes the selection by d, keeping it in range, and scrolls the
// panes so it stays on screen.
func (m *uiModel) move(d int) {
	n := m.selectable()
	if n == 0 {
		return
	}
	m.sel += d
	m.sel = min(max(m.sel, 0), n-1)
	m.selKey = m.keyAt(m.sel)
	m.panes()
}

// clampSelection brings the selection back in range after Live changes the
// items and rows, following the selected one by id when it is still there.
func (m *uiModel) clampSelection() {
	n := m.selectable()
	if n == 0 {
		m.sel, m.selKey, m.top = 0, "", 0
		return
	}
	if m.selKey != "" {
		for i := range n {
			if m.keyAt(i) == m.selKey {
				m.sel = i
				break
			}
		}
	}
	m.sel = min(max(m.sel, 0), n-1)
	m.selKey = m.keyAt(m.sel)
}

func (m *uiModel) View() tea.View {
	content := ""
	if m.logv != nil {
		content = m.renderLog()
	} else {
		content = m.render()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// splitPanes divides avail lines among n items, oldest first. When every
// item fits at its minimum, the lines are even and the remainder goes to
// the first panes. Otherwise panes keep their minimum (uiMinPane) and
// scroll: start is the first on-screen item, count how many fit, kept so
// sel is visible. maxTail is the most log lines any on-screen pane shows;
// offscreen is how many items are not shown.
func splitPanes(n, avail, sel, top int) (start, count int, lines []int, offscreen, maxTail int) {
	if n == 0 {
		return 0, 0, nil, 0, 0
	}
	if avail >= n*uiMinPane {
		base, rem := avail/n, avail%n
		lines = make([]int, n)
		maxTail = base - 1
		for i := range lines {
			lines[i] = base
			if i < rem {
				lines[i]++
				maxTail = base
			}
		}
		return 0, n, lines, 0, maxTail
	}
	count = avail / uiMinPane
	if count > 0 {
		start = min(max(top, 0), n-count)
		if sel < start {
			start = sel
		}
		if sel >= start+count {
			start = sel - count + 1
		}
	}
	lines = make([]int, count)
	for i := range lines {
		lines[i] = uiMinPane
	}
	return start, count, lines, n - count, uiMinPane - 1
}

// fixedLines is the height taken by the lines around the panes.
func (m *uiModel) fixedLines() int {
	n := 2 // header and summary
	n += len(m.data.Rows)
	n++ // the key hint
	if m.err != nil {
		n++
	}
	return n
}

// panes computes the split for the current height and stores the scrolled
// position, so the selection stays visible.
func (m *uiModel) panes() (start, count int, lines []int, offscreen, maxTail int) {
	start, count, lines, offscreen, maxTail = splitPanes(len(m.data.Items), max(m.bodyHeight(), 0), m.paneSel(), m.top)
	m.top = start
	return start, count, lines, offscreen, maxTail
}

func (m *uiModel) bodyHeight() int { return m.height - m.fixedLines() }

// tailRequest is how many log lines the tallest pane needs, computed
// without changing the model.
func (m *uiModel) tailRequest() int {
	if m.height <= 0 || len(m.data.Items) == 0 {
		return uiDefaultTail
	}
	_, _, _, _, maxTail := splitPanes(len(m.data.Items), max(m.bodyHeight(), 0), m.paneSel(), m.top)
	return max(maxTail, uiOnceTail)
}

func (m *uiModel) render() string {
	start, count, lines, offscreen, _ := m.panes()
	var b strings.Builder
	line := func(s string) {
		b.WriteString(m.cut(s))
		b.WriteString("\n")
	}
	line(m.headerLine())
	line(m.summaryLine(offscreen))
	if m.showKeys {
		for _, k := range m.keyLines() {
			line(k)
		}
		return b.String()
	}
	if m.err != nil {
		line(m.st.Danger.Render("error: " + m.err.Error()))
	}
	for i := 0; i < count; i++ {
		it := m.data.Items[start+i]
		prefix := "  "
		if start+i == m.sel {
			prefix = m.st.Info.Render("▸") + " "
		}
		line(m.paneRule(it, prefix, m.now()))
		for _, l := range m.paneLogs(it, lines[i]-1) {
			line(l)
		}
	}
	for i, r := range m.data.Rows {
		prefix := "  "
		if len(m.data.Items)+i == m.sel {
			prefix = m.st.Info.Render("▸") + " "
		}
		line(prefix + m.rowLine(r))
	}
	line(m.st.Dim.Render(m.keyHint()))
	return b.String()
}

// renderOnce is --once's frame: the summary, each item's header with its
// last three log lines, then the rows, as plain text. color keeps the
// styles; a non-terminal output has none.
func (m *uiModel) renderOnce(color bool) string {
	var b strings.Builder
	fmt.Fprintln(&b, m.summaryLine(0))
	if m.err != nil {
		fmt.Fprintln(&b, "error: "+m.err.Error())
	}
	for _, it := range m.data.Items {
		fmt.Fprintln(&b, m.itemHeader(it))
		for _, l := range m.paneLogs(it, uiOnceTail) {
			fmt.Fprintln(&b, l)
		}
	}
	for _, r := range m.data.Rows {
		fmt.Fprintln(&b, m.rowLine(r))
	}
	out := b.String()
	if !color {
		out = stripANSI(out)
	}
	return out
}

// cut truncates a line to the terminal's width.
func (m *uiModel) cut(s string) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Truncate(s, m.width, "…")
}

func (m *uiModel) headerLine() string {
	st := m.st
	left := st.Bold.Render("bottown") + st.Dim.Render(" · ") + m.repo + st.Dim.Render(" · ") + m.instance
	return left + "   " + st.Dim.Render("web ") + m.loginURL
}

func (m *uiModel) summaryLine(offscreen int) string {
	s := m.data.Summary
	line := fmt.Sprintf("%d running · %d quiet · %d waiting", s.Running, s.Quiet, s.Waiting)
	if offscreen > 0 {
		line += m.st.Warning.Render(fmt.Sprintf("   %d off screen", offscreen))
	}
	if inbox := m.inboxSummary(); inbox != "" {
		line += "    " + m.st.Dim.Render("needs you: ") + inbox
	}
	return line
}

// inboxSummary is the owner inbox's counts by kind, in a fixed order.
func (m *uiModel) inboxSummary() string {
	s := m.data.Summary
	var parts []string
	for _, k := range []struct {
		kind  webui.InboxItemKind
		label string
	}{
		{webui.InboxReadyToLand, "ready to land"},
		{webui.InboxBrokenRun, "failed"},
		{webui.InboxConflictsWithBase, "conflict"},
		{webui.InboxStoppedFlow, "stopped"},
		{webui.InboxDiscussAnswer, "discuss"},
		{webui.InboxActorFiledIssue, "actor-filed"},
	} {
		if n := s.Inbox[k.kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k.label))
		}
	}
	if len(parts) == 0 && s.InboxError != "" {
		return m.st.Warning.Render("inbox unavailable")
	}
	return strings.Join(parts, ", ")
}

// paneRule is an item's pane header: prefix (the selection marker), the
// item's header, a rule filling the width, and its last-output age, at now.
// A quiet pane's rule is in the warning colour. The full-screen log's
// header is the same rule.
func (m *uiModel) paneRule(it webui.LiveItem, prefix string, now time.Time) string {
	left := prefix + m.itemLine(it, now)
	age := m.ageString(it, now)
	var rule string
	if m.width <= 0 {
		rule = left + m.st.Dim.Render(" · ") + age
	} else {
		seg := " " + age + " ──"
		fill := m.width - lipgloss.Width(left) - lipgloss.Width(seg)
		if fill < 1 {
			rule = ansi.Truncate(left+" "+age, m.width, "…")
		} else {
			rule = left + strings.Repeat("─", fill) + seg
		}
	}
	if it.Quiet {
		rule = m.st.Warning.Render(rule)
	}
	return rule
}

// itemHeader is an item's header without the rule, for --once.
func (m *uiModel) itemHeader(it webui.LiveItem) string {
	now := m.now()
	return m.itemLine(it, now) + " · " + m.ageString(it, now)
}

// itemLine is an item's header text: target, title, kind and round, actor,
// and elapsed against the limit.
func (m *uiModel) itemLine(it webui.LiveItem, now time.Time) string {
	st := m.st
	var b strings.Builder
	b.WriteString(st.Bold.Render(it.Target))
	if it.Title != "" {
		b.WriteString(" " + it.Title)
	}
	b.WriteString(st.Dim.Render(" · "))
	b.WriteString(m.itemKind(it))
	if it.Actor != "" {
		b.WriteString(st.Dim.Render(" · ") + it.Actor)
	}
	b.WriteString(st.Dim.Render(" · " + itemElapsed(it, now)))
	return b.String()
}

func (m *uiModel) itemKind(it webui.LiveItem) string {
	if it.Kind == "check" || it.Kind == "e2e" {
		s := "checks " + it.Kind
		if it.SHA != "" {
			s += " " + shortSHA(it.SHA)
		}
		return s
	}
	s := it.Kind
	if it.Flow != nil && it.Flow.Round > 0 {
		s += fmt.Sprintf(" %d/%d", it.Flow.Round, it.Flow.MaxRounds)
	}
	return s
}

func itemElapsed(it webui.LiveItem, now time.Time) string {
	el := ageShort(now.Sub(it.Started))
	if it.Limit > 0 {
		el += "/" + ageShort(time.Duration(it.Limit)*time.Second)
	}
	return el
}

// ageString is the time since the item last wrote, or "no output Nm" when
// it is quiet.
func (m *uiModel) ageString(it webui.LiveItem, now time.Time) string {
	from := it.Started
	if it.LastOutput != nil {
		from = *it.LastOutput
	}
	d := now.Sub(from)
	if it.Quiet {
		return "no output " + ageShort(d)
	}
	return ageShort(d)
}

// ageShort is a duration in one unit: 4s, 6m, 2h.
func ageShort(d time.Duration) string {
	d = max(d, 0).Truncate(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// paneLogs renders the last n lines of an item's tail as the terminal tail
// renders them, each cut to the width.
func (m *uiModel) paneLogs(it webui.LiveItem, n int) []string {
	if n <= 0 {
		return nil
	}
	tail := it.Tail
	if len(tail) == 0 {
		return []string{m.st.Dim.Render("  (no output yet)")}
	}
	if len(tail) > n {
		tail = tail[len(tail)-n:]
	}
	out := make([]string, 0, len(tail))
	for _, l := range tail {
		out = append(out, m.cut(m.st.logLine(l, false, "  ")))
	}
	return out
}

// rowLine is one line for a flow or interactive run with no tail.
func (m *uiModel) rowLine(r webui.LiveRow) string {
	st := m.st
	head := r.Target
	if r.Title != "" {
		head += " " + r.Title
	}
	parts := []string{st.Bold.Render(head)}
	if r.Flow != nil && r.Flow.Round > 0 {
		parts = append(parts, fmt.Sprintf("flow %d/%d", r.Flow.Round, r.Flow.MaxRounds))
	}
	if r.Kind == "interactive" {
		if r.Detail != "" {
			parts = append(parts, r.Detail)
		}
		if r.What != "" {
			parts = append(parts, r.What)
		}
	} else {
		if r.What != "" {
			parts = append(parts, r.What)
		}
		if r.Detail != "" {
			parts = append(parts, r.Detail)
		}
	}
	if r.Since != nil {
		parts = append(parts, ageShort(m.now().Sub(*r.Since)))
	}
	return strings.Join(parts, st.Dim.Render(" · "))
}

// keyHint is the panes' last line. A row has no log, so with one selected
// it says so in place of Enter.
func (m *uiModel) keyHint() string {
	enter := "enter log"
	if m.sel >= len(m.data.Items) && m.selectable() > 0 {
		enter = "no log for this row"
	}
	return "j/k move · " + enter + " · o open browser · ? keys · q quit"
}

func (m *uiModel) keyLines() []string {
	return []string{
		"j / k, ↑ / ↓   move the selection",
		"enter          show the selected run's log full screen, following it:",
		"                 f follow on/off, j/k ↑/↓ pgup/pgdn g/G scroll, esc back",
		"o              open the web UI in this machine's browser",
		"?              hide these keys",
		"q, ctrl-c      quit and stop the server",
	}
}
