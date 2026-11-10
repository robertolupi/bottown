package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

// liveFixture is a checkout with everything Live tells apart: a flow's
// run (#51), a flow's checks (#52), a standalone discuss run with no log
// (#44), a landing's checks (#15, whose lock the landing holds without a
// status file), a terminal-started flow between runs waiting for its actor
// (#60), an interactive run (#70), a thread that ended ten minutes ago
// (#80) and one that ended two hours ago (#81).
type liveFixture struct {
	s       *Server
	runsDir string
	now     time.Time
	logAt   time.Time // r-impl's log's mtime
	since   time.Time // #60's step's start
}

func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	fix := defaultRunsFixture()
	for _, n := range []int64{15, 44, 51, 52, 60, 70, 80, 81} {
		fix.issues = append(fix.issues, &sdk.Issue{Index: n, Title: fmt.Sprintf("Issue %d", n), State: sdk.StateOpen})
	}
	fix.prs = []*sdk.PullRequest{{Index: 16, Title: "PR 16", State: sdk.StateOpen, Head: &sdk.PRBranchInfo{Ref: "issue/15", Sha: "abcdef0123"}}}
	ended := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	fix.runs = []secrets.Run{
		{ID: "r-impl", Kind: "implement", Actor: "claude", Family: "anthropic", Harness: "claude", Spec: "opus", Repo: "bottown", Issue: 51,
			Created: now.Add(-30 * time.Minute), Expires: now.Add(30 * time.Minute)},
		{ID: "r-disc", Kind: "discuss", Actor: "kimi", Family: "moonshot", Harness: "kimi", Repo: "bottown", Issue: 44,
			Created: now.Add(-20 * time.Minute), Expires: now.Add(40 * time.Minute)},
		{ID: "r-inter", Kind: "implement", Actor: "agy", Family: "google", Harness: "agy", Repo: "bottown", Issue: 70, Interactive: true,
			Created: now.Add(-9 * time.Minute), Expires: now.Add(time.Hour)},
		{ID: "r-60", Kind: "implement", Actor: "claude", Repo: "bottown", Issue: 60, Status: "succeeded",
			Created: now.Add(-40 * time.Minute), Expires: now.Add(20 * time.Minute), Ended: ended(8 * time.Minute)},
		{ID: "r-80", Kind: "implement", Actor: "claude", Repo: "bottown", Issue: 80, Status: "succeeded",
			Created: now.Add(-50 * time.Minute), Expires: now.Add(10 * time.Minute), Ended: ended(10 * time.Minute)},
		{ID: "r-81", Kind: "implement", Actor: "claude", Repo: "bottown", Issue: 81, Status: "failed",
			Created: now.Add(-3 * time.Hour), Expires: now.Add(-2 * time.Hour), Ended: ended(2 * time.Hour)},
	}
	fix.denials = []Denial{{Time: now, Run: "r-impl", Op: "git", Rule: "run-push"}}
	_, port := newRunsFixtureServer(t, fix)

	top := t.TempDir()
	dir := filepath.Join(top, ".bottown", "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// r-impl's log: 30 assistant lines, last written four minutes ago.
	var log strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&log, "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"line %d\"}]}}\n", i)
	}
	logPath := filepath.Join(dir, "r-impl.log")
	if err := os.WriteFile(logPath, []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	logAt := now.Add(-4 * time.Minute)
	if err := os.Chtimes(logPath, logAt, logAt); err != nil {
		t.Fatal(err)
	}
	hold := func(issue int64, st *runs.FlowStatusView) {
		t.Helper()
		release, err := runs.HoldFlow(dir, "bottown", issue)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		if st != nil {
			if err := runs.WriteFlowStatus(dir, "bottown", issue, *st); err != nil {
				t.Fatal(err)
			}
		}
	}
	since := now.Add(-6 * time.Minute)
	hold(51, &runs.FlowStatusView{Issue: 51, Implement: "claude", Verify: "kimi", Rounds: 3, Round: 2, Step: runs.StepImplementing, Since: now.Add(-30 * time.Minute)})
	hold(52, &runs.FlowStatusView{Issue: 52, Implement: "claude", Verify: "kimi", Rounds: 3, Round: 1, Step: runs.StepChecks, Since: now.Add(-5 * time.Minute)})
	hold(60, &runs.FlowStatusView{Issue: 60, Implement: "agy", Verify: "claude", Rounds: 3, Round: 1, Step: runs.StepWaitingActor,
		Detail: "agy has another live batch run (r-x)", Since: since})
	hold(15, nil) // a landing
	check := func(rec runs.CheckRecord, log string) {
		t.Helper()
		if log != "" {
			if err := os.WriteFile(filepath.Join(dir, rec.ID+".log"), []byte(log), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		release, err := runs.HoldCheck(dir, rec)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	check(runs.CheckRecord{ID: "flow-52-1a2b3c4d5e.e2e", Group: "e2e", Repo: "bottown", Issue: 52, PR: 53, SHA: "1a2b3c4d5e6f", Commands: []string{"make e2e"},
		Started: now.Add(-5 * time.Minute), Expires: now.Add(25 * time.Minute)}, "=== RUN   TestE2E\n")
	check(runs.CheckRecord{ID: "flow-15-abcdef0123.check", Group: "check", Repo: "bottown", Issue: 15, PR: 16, SHA: "abcdef0123",
		Commands: []string{"make check"}, Started: now.Add(-2 * time.Minute)}, "")

	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  top,
		FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &liveFixture{s: s, runsDir: dir, now: now, logAt: logAt, since: since}
}

func liveItem(t *testing.T, l Live, id string) LiveItem {
	t.Helper()
	for _, it := range l.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %s in %+v", id, l.Items)
	return LiveItem{}
}

func TestLive(t *testing.T) {
	f := newLiveFixture(t)
	ctx := context.Background()
	l, err := f.s.live(ctx, 0, f.now)
	if err != nil {
		t.Fatal(err)
	}

	// Items: every batch run and check group, oldest first.
	var ids []string
	for _, it := range l.Items {
		ids = append(ids, it.ID)
	}
	if want := "r-impl r-disc flow-52-1a2b3c4d5e.e2e flow-15-abcdef0123.check"; strings.Join(ids, " ") != want {
		t.Fatalf("items = %v, want %s", ids, want)
	}

	// A flow's run: its flow from the held lock and status file.
	impl := liveItem(t, l, "r-impl")
	if impl.Kind != "implement" || impl.Target != "#51" || impl.Issue != 51 || impl.Title != "Issue 51" || !strings.HasSuffix(impl.IssueURL, "/bottown/bottown/issues/51") ||
		impl.Actor != "claude" || impl.Family != "anthropic" || impl.Spec != "opus" || impl.Limit != 3600 || impl.Denials != 1 ||
		impl.LastOutput == nil || !impl.LastOutput.Equal(f.logAt) || impl.Quiet {
		t.Fatalf("flow run = %+v", impl)
	}
	if impl.Flow == nil || *impl.Flow != (LiveFlow{Issue: 51, Round: 2, MaxRounds: 3, Step: runs.StepImplementing}) {
		t.Fatalf("flow run's flow = %+v", impl.Flow)
	}
	// The default tail is the last 12 rendered lines.
	if len(impl.Tail) != liveTail || impl.Tail[0].Text != "line 19" || impl.Tail[liveTail-1].Text != "line 30" {
		t.Fatalf("flow run's tail = %+v", impl.Tail)
	}

	// A standalone run belongs to no flow, and with no log counts from its
	// start: twenty minutes, quiet.
	disc := liveItem(t, l, "r-disc")
	if disc.Flow != nil || disc.Kind != "discuss" || disc.LastOutput != nil || !disc.Quiet || len(disc.Tail) != 0 || disc.Tail == nil {
		t.Fatalf("standalone run = %+v", disc)
	}

	// A flow's checks: the check's issue's flow.
	e2e := liveItem(t, l, "flow-52-1a2b3c4d5e.e2e")
	if e2e.Kind != "e2e" || e2e.Target != "#52" || e2e.PR != 53 || e2e.SHA != "1a2b3c4d5e6f" || e2e.Title != "Issue 52" || e2e.Limit != 1800 ||
		e2e.Quiet || len(e2e.Tail) != 1 || e2e.Flow == nil || *e2e.Flow != (LiveFlow{Issue: 52, Round: 1, MaxRounds: 3, Step: runs.StepChecks}) {
		t.Fatalf("flow's checks = %+v (flow %+v)", e2e, e2e.Flow)
	}

	// A landing's checks: its lock is held with no status file, so the
	// flow has no round, and the step is the checks'. No log yet: two
	// minutes from its start, not quiet.
	land := liveItem(t, l, "flow-15-abcdef0123.check")
	if land.Kind != "check" || land.Target != "#15" || land.PR != 16 || land.Limit != 0 || land.Quiet || land.LastOutput != nil ||
		land.Flow == nil || *land.Flow != (LiveFlow{Issue: 15, Step: runs.StepChecks}) {
		t.Fatalf("landing's checks = %+v (flow %+v)", land, land.Flow)
	}

	// Rows: the terminal-started flow between runs, with its status, then
	// the interactive run. Flows with a live item are not rows.
	if len(l.Rows) != 2 {
		t.Fatalf("rows = %+v", l.Rows)
	}
	flowRow, inter := l.Rows[0], l.Rows[1]
	if flowRow.Key != "flow-60" || flowRow.Kind != "flow" || flowRow.Target != "#60" || flowRow.Issue != 60 || flowRow.Title != "Issue 60" ||
		flowRow.What != runs.StepWaitingActor || flowRow.Detail != "agy has another live batch run (r-x)" ||
		flowRow.Since == nil || !flowRow.Since.Equal(f.since) || flowRow.Flow == nil || flowRow.Flow.Round != 1 || flowRow.Flow.MaxRounds != 3 {
		t.Fatalf("flow row = %+v", flowRow)
	}
	if inter.Key != "r-inter" || inter.Kind != "interactive" || inter.ID != "r-inter" || inter.Target != "#70" || inter.Title != "Issue 70" ||
		inter.What != "interactive" || inter.Detail != "implement · agy" || inter.Since == nil || !inter.Since.Equal(f.now.Add(-9*time.Minute)) {
		t.Fatalf("interactive row = %+v", inter)
	}

	// Ended: #80's thread, not #81's (two hours ago) nor #60's (its flow
	// holds its lock).
	if len(l.Ended) != 1 {
		t.Fatalf("ended = %+v", l.Ended)
	}
	if e := l.Ended[0]; e.Key != "flow:issue:80" || e.Kind != "flow" || e.Issue != 80 || e.RunID != "r-80" || e.Target != "#80" ||
		e.Title != "Issue 80" || !e.Ended.Equal(f.now.Add(-10*time.Minute)) {
		t.Fatalf("ended thread = %+v", e)
	}

	// The summary counts what Live holds, and the inbox by kind.
	inbox, err := f.s.ComputeInbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantInbox := map[InboxItemKind]int{}
	for _, it := range inbox {
		wantInbox[it.Kind]++
	}
	sum := l.Summary
	if sum.Running != 4 || sum.Quiet != 1 || sum.Waiting != 1 || sum.Interactive != 1 || sum.InboxError != "" || fmt.Sprint(sum.Inbox) != fmt.Sprint(wantInbox) {
		t.Fatalf("summary = %+v, want inbox %v", sum, wantInbox)
	}
	if len(wantInbox) == 0 {
		t.Fatal("the fixture's inbox is empty: the counts are not exercised")
	}
}

// quiet is now − last_output ≥ quietAfter: a second before the threshold
// an item is not quiet, at it it is.
func TestLiveQuietThreshold(t *testing.T) {
	f := newLiveFixture(t)
	ctx := context.Background()
	before, err := f.s.live(ctx, 0, f.logAt.Add(quietAfter-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	at, err := f.s.live(ctx, 0, f.logAt.Add(quietAfter))
	if err != nil {
		t.Fatal(err)
	}
	if liveItem(t, before, "r-impl").Quiet || !liveItem(t, at, "r-impl").Quiet {
		t.Fatalf("quiet a second before the threshold = %v, at it = %v", liveItem(t, before, "r-impl").Quiet, liveItem(t, at, "r-impl").Quiet)
	}
	if at.Summary.Quiet != before.Summary.Quiet+1 {
		t.Fatalf("quiet counts: %d before, %d at", before.Summary.Quiet, at.Summary.Quiet)
	}

	started := f.now.Add(-30 * time.Minute)
	if isQuiet(nil, started, started.Add(quietAfter-time.Second)) || !isQuiet(nil, started, started.Add(quietAfter)) {
		t.Fatal("a log-less item counts from its start")
	}
}

// GET /api/live serves Live, with ?tail=N lines per item.
func TestLiveHandlerTail(t *testing.T) {
	f := newLiveFixture(t)
	get := func(query string) (int, Live) {
		t.Helper()
		w := httptest.NewRecorder()
		f.s.handleGetLive(w, httptest.NewRequest(http.MethodGet, "/api/live"+query, nil))
		var l Live
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, l
	}
	code, l := get("?tail=3")
	if code != http.StatusOK {
		t.Fatalf("tail=3: %d", code)
	}
	impl := liveItem(t, l, "r-impl")
	if len(impl.Tail) != 3 || impl.Tail[0].Text != "line 28" || impl.Tail[2].Text != "line 30" {
		t.Fatalf("tail=3 = %+v", impl.Tail)
	}
	if _, l = get("?tail=100"); len(liveItem(t, l, "r-impl").Tail) != 30 {
		t.Fatalf("tail=100 = %d lines, want the log's 30", len(liveItem(t, l, "r-impl").Tail))
	}
	if _, l = get(""); len(liveItem(t, l, "r-impl").Tail) != liveTail {
		t.Fatalf("no tail = %d lines, want %d", len(liveItem(t, l, "r-impl").Tail), liveTail)
	}
	for _, bad := range []string{"?tail=0", "?tail=-1", "?tail=x"} {
		if code, _ := get(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
}
