package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

// logPager is the run page's log as it polls GET /api/runs/{id}/log: it
// loads the whole log, then asks for the lines after the last stable
// prefix it was given, keeps that prefix and replaces the rest.
type logPager struct {
	lines  []runs.LogLine
	stable int
	sum    string
	loaded bool
}

func (p *logPager) query() url.Values {
	q := url.Values{}
	if p.loaded {
		q.Set("after", strconv.Itoa(p.stable))
		q.Set("sum", p.sum)
	}
	return q
}

func (p *logPager) apply(q url.Values, page logPage) {
	if q.Has("after") && !page.Reset {
		p.lines = append(slices.Clone(p.lines[:p.stable]), page.Lines...)
	} else {
		p.lines = slices.Clone(page.Lines)
	}
	p.stable, p.sum, p.loaded = page.Stable, page.StableSum, true
}

// sameLines compares lines as the endpoint serves them.
func sameLines(t *testing.T, got, want []runs.LogLine) bool {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return string(g) == string(w)
}

// poll answers the pager's next request for lines and checks that its
// lines are then the whole log, and the stable prefix is want.
func (p *logPager) poll(t *testing.T, lines []runs.LogLine, ended bool, wantStable int) logPage {
	t.Helper()
	q := p.query()
	page, err := pageLog("r-1", lines, ended, q)
	if err != nil {
		t.Fatalf("pageLog(%v): %v", q, err)
	}
	p.apply(q, page)
	if !sameLines(t, p.lines, lines) {
		t.Fatalf("after %v: page has %+v, log is %+v", q, p.lines, lines)
	}
	if page.Stable != wantStable || page.StableSum != linesSum(lines[:wantStable]) {
		t.Fatalf("after %v: stable %d (%s), want %d", q, page.Stable, page.StableSum, wantStable)
	}
	return page
}

func text(s string) runs.LogLine { return runs.LogLine{Kind: runs.LineText, Text: s} }

func call(id, state string) runs.LogLine {
	return runs.LogLine{Kind: runs.LineTool, Tool: "bash", State: state, Text: "bash: " + id}
}

func TestPageLogGrowingLog(t *testing.T) {
	var p logPager
	p.poll(t, []runs.LogLine{text("a")}, false, 0)
	p.poll(t, []runs.LogLine{text("a"), text("b")}, false, 1)
	page := p.poll(t, []runs.LogLine{text("a"), text("b"), text("c")}, false, 2)
	if page.Reset || len(page.Lines) != 2 {
		t.Fatalf("increment = %+v, want b and c", page)
	}
	// The last line of a live log can still grow.
	p.poll(t, []runs.LogLine{text("a"), text("b"), text("c d")}, false, 2)
}

func TestPageLogToolCallCompletes(t *testing.T) {
	var p logPager
	p.poll(t, []runs.LogLine{text("a"), call("t1", "running")}, false, 1)
	p.poll(t, []runs.LogLine{text("a"), call("t1", "completed"), text("b")}, false, 2)
}

// A line after a running call is final for runs.LineTracker, but not a
// position the page may keep: the prefix stays at the call until it
// completes.
func TestPageLogUnfinishedCallThenTextThenCompletion(t *testing.T) {
	var p logPager
	p.poll(t, []runs.LogLine{text("a"), call("t1", "running"), text("b")}, false, 1)
	p.poll(t, []runs.LogLine{text("a"), call("t1", "running"), text("b"), text("c")}, false, 1)
	page := p.poll(t, []runs.LogLine{text("a"), call("t1", "completed"), text("b"), text("c")}, false, 3)
	if page.Reset {
		t.Fatalf("completion reset the page: %+v", page)
	}
	p.poll(t, []runs.LogLine{text("a"), call("t1", "completed"), text("b"), text("c")}, true, 4)
}

func TestPageLogAfterTheEnd(t *testing.T) {
	lines := []runs.LogLine{text("a"), call("t1", "failed"), text("b")}
	var p logPager
	p.poll(t, lines, true, 3) // an ended run's log is stable as a whole
	page := p.poll(t, lines, true, 3)
	if page.Reset || len(page.Lines) != 0 {
		t.Fatalf("after the end = %+v, want no lines", page)
	}
}

func TestPageLogResets(t *testing.T) {
	lines := []runs.LogLine{text("a"), text("b")}
	sum := linesSum(lines[:1])
	for name, q := range map[string]url.Values{
		"past the end": {"after": {"3"}, "sum": {linesSum(lines)}},
		"no sum":       {"after": {"1"}},
		"another sum":  {"after": {"1"}, "sum": {linesSum([]runs.LogLine{text("x")})}},
	} {
		page, err := pageLog("r-1", lines, false, q)
		if err != nil || !page.Reset || !sameLines(t, page.Lines, lines) || page.Stable != 1 || page.StableSum != sum {
			t.Errorf("%s: %+v, %v; want a reset with the whole log", name, page, err)
		}
	}
	for _, bad := range []string{"-1", "x"} {
		if _, err := pageLog("r-1", lines, false, url.Values{"after": {bad}, "sum": {sum}}); err == nil {
			t.Errorf("after=%s accepted", bad)
		}
	}
	// No cursor: the whole log, and tail still trims it.
	if page, _ := pageLog("r-1", lines, false, url.Values{"tail": {"1"}}); page.Reset || !sameLines(t, page.Lines, lines[1:]) {
		t.Errorf("tail=1: %+v", page)
	}
	if page, _ := pageLog("r-1", nil, false, url.Values{}); page.Lines == nil || page.Stable != 0 {
		t.Errorf("no log: %+v", page)
	}
}

// logServer is a web UI server over a fixture forge, whose runs are fix's,
// with its runs directory.
func logServer(t *testing.T, fix *runsFixture) (*Server, string) {
	t.Helper()
	_, port := newRunsFixtureServer(t, fix)
	top := t.TempDir()
	dir := filepath.Join(top, ".bottown", "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  top,
		FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

// pollHandler is poll through GET /api/runs/{id}/log, whose lines are want.
func (p *logPager) pollHandler(t *testing.T, s *Server, id string, want []runs.LogLine, wantStable int) logPage {
	t.Helper()
	q := p.query()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id+"/log?"+q.Encode(), nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	s.handleGetRunLog(rec, req)
	var page logPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %v: %s", req.URL, rec.Code, err, rec.Body)
	}
	p.apply(q, page)
	if !sameLines(t, p.lines, want) {
		t.Fatalf("after %v: page has %+v, want %+v", q, p.lines, want)
	}
	if page.Stable != wantStable || page.StableSum != linesSum(want[:wantStable]) {
		t.Fatalf("after %v: stable %d, want %d", q, page.Stable, wantStable)
	}
	return page
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A crush run is followed through its live transcript, which the launcher
// removes before it appends the exported one to the log. In between, the
// log is crush's stdout alone, and a cursor still in range points into
// other lines: the server answers with a reset.
func TestRunLogTranscriptHandoff(t *testing.T) {
	fix := defaultRunsFixture()
	fix.runs = []secrets.Run{{ID: "r-crush", Actor: "crush", Harness: "crush", Kind: "implement"}}
	s, dir := logServer(t, fix)
	logPath := filepath.Join(dir, "r-crush.log")
	transcriptPath := filepath.Join(dir, "r-crush"+runs.LiveTranscriptSuffix)
	transcript := `{"meta":{"id":"s1","title":"Fix it"}}
{"role":"assistant","parts":[{"type":"text","text":"Looking at the code."}]}
{"role":"assistant","parts":[{"type":"text","text":"Found it."}]}
`
	write(t, logPath, "")
	write(t, transcriptPath, transcript)
	session := []runs.LogLine{
		{Kind: runs.LineStatus, Text: "session started: Fix it"},
		{Kind: runs.LineText, Text: "Looking at the code."},
		{Kind: runs.LineText, Text: "Found it."},
	}
	var p logPager
	p.pollHandler(t, s, "r-crush", session, 2)

	// The transcript removed, crush's stdout in the log: three lines, so
	// after=2 is in range, but they are not the transcript's.
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}
	stdout := "one\ntwo\nthree\n"
	write(t, logPath, stdout)
	stdoutLines := []runs.LogLine{text("one"), text("two"), text("three")}
	if page := p.pollHandler(t, s, "r-crush", stdoutLines, 2); !page.Reset {
		t.Fatalf("the transcript's handoff did not reset: %+v", page)
	}

	// The exported transcript appended.
	write(t, logPath, stdout+transcript)
	final := append(slices.Clone(session),
		runs.LogLine{Kind: runs.LinePlain, Text: "one"},
		runs.LogLine{Kind: runs.LinePlain, Text: "two"},
		runs.LogLine{Kind: runs.LinePlain, Text: "three"})
	if page := p.pollHandler(t, s, "r-crush", final, 5); !page.Reset {
		t.Fatalf("the exported transcript did not reset: %+v", page)
	}

	// The run ends: its whole log is stable, and nothing follows it.
	now := time.Now()
	fix.mu.Lock()
	fix.runs[0].Ended = &now
	fix.mu.Unlock()
	if page := p.pollHandler(t, s, "r-crush", final, 6); page.Reset || len(page.Lines) != 1 {
		t.Fatalf("after the run ended: %+v, want the last line", page)
	}
}

// A check's log is its commands' plain output, and stays live: its last
// line is held back.
func TestRunLogCheck(t *testing.T) {
	s, dir := logServer(t, defaultRunsFixture())
	id := "flow-60-1a2b3c4d5e.check"
	write(t, filepath.Join(dir, id+".log"), "==> [check] make check\n")
	var p logPager
	p.pollHandler(t, s, id, []runs.LogLine{{Kind: runs.LinePlain, Text: "==> [check] make check"}}, 0)
	write(t, filepath.Join(dir, id+".log"), "==> [check] make check\nok\npassed\n")
	want := []runs.LogLine{{Kind: runs.LinePlain, Text: "==> [check] make check"}, {Kind: runs.LinePlain, Text: "ok"}, {Kind: runs.LinePlain, Text: "passed"}}
	p.pollHandler(t, s, id, want, 2)
	p.pollHandler(t, s, id, want, 2)
}
