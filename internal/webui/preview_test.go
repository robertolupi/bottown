package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/robertolupi/bottown/internal/runs"
)

// fixturesDir is the repository's web/fixtures, which `bottown web
// preview` serves.
const fixturesDir = "../../web/fixtures"

// TestPreviewFixturesLoad checks that every checked-in fixture decodes
// strictly into the API's types, so a renamed field shows up here rather
// than as a blank in the preview, and that its references hold.
func TestPreviewFixturesLoad(t *testing.T) {
	names := FixtureNames(fixturesDir)
	for _, want := range []string{"default", "empty", "errors"} {
		if !contains(names, want) {
			t.Errorf("fixture %s missing; have %v", want, names)
		}
	}
	for _, name := range names {
		fx, err := LoadFixture(fixturesDir, name)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		for _, r := range fx.LiveRuns {
			if !r.Interactive && fx.Logs[r.ID] == nil {
				t.Errorf("%s: batch live run %s has no log", name, r.ID)
			}
		}
	}
	fx, err := LoadFixture(fixturesDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.LiveRuns) == 0 || len(fx.History) == 0 || len(fx.Inbox) == 0 || len(fx.Issues) == 0 || len(fx.Milestones) == 0 {
		t.Errorf("the default fixture should exercise every view: %d live, %d history, %d inbox, %d issues, %d milestones",
			len(fx.LiveRuns), len(fx.History), len(fx.Inbox), len(fx.Issues), len(fx.Milestones))
	}
	for _, flow := range fx.ActiveFlows {
		if _, ok := fx.Issues[fmt.Sprint(flow.Issue)]; !ok {
			t.Errorf("default: active flow issue #%d has no details", flow.Issue)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestLoadFixtureErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "typo.json"), []byte(`{"live_run": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFixture(dir, "typo"); err == nil || !strings.Contains(err.Error(), "live_run") {
		t.Errorf("unknown field: %v", err)
	}
	if _, err := LoadFixture(dir, "missing"); err == nil || !strings.Contains(err.Error(), "have: typo") {
		t.Errorf("missing fixture: %v", err)
	}
	for _, bad := range []string{"", "../typo", ".hidden", `a\b`} {
		if _, err := LoadFixture(dir, bad); err == nil || !strings.Contains(err.Error(), "invalid fixture name") {
			t.Errorf("name %q: %v", bad, err)
		}
	}
}

type previewClient struct {
	t    *testing.T
	base string
}

func startPreview(t *testing.T) (*PreviewServer, previewClient) {
	t.Helper()
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>preview</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	srv, err := NewPreviewServer(PreviewConfig{UI: ui, FixturesDir: fixturesDir, Fixture: "default"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return srv, previewClient{t: t, base: srv.BaseURL()}
}

// do sends a request as the page would: Host is the server's, and a
// state-changing request carries its Origin.
func (c previewClient) do(method, path, body string) (int, string) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", c.base)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// get decodes a 200 response into v.
func (c previewClient) get(path string, v any) {
	c.t.Helper()
	code, body := c.do(http.MethodGet, path, "")
	if code != http.StatusOK {
		c.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		c.t.Fatalf("GET %s: decode %q: %v", path, body, err)
	}
}

func TestPreviewRoutes(t *testing.T) {
	_, c := startPreview(t)
	fx, err := LoadFixture(fixturesDir, "default")
	if err != nil {
		t.Fatal(err)
	}

	var st UIStatusResponse
	c.get("/api/status", &st)
	if st.Repo != fx.Status.Repo || len(st.Inbox) != len(fx.Inbox) || len(st.LiveRuns) != len(fx.LiveRuns) || len(st.ActiveFlows) != len(fx.ActiveFlows) {
		t.Errorf("status: repo %q, %d inbox, %d live, %d flows", st.Repo, len(st.Inbox), len(st.LiveRuns), len(st.ActiveFlows))
	}
	// A live run's tail is its fixture log's last lines.
	id := st.LiveRuns[0].ID
	if want := tail(fx.Logs[id], liveTailLines); len(st.LiveRuns[0].LogTail) != len(want) || st.LiveRuns[0].LogTail[len(want)-1] != want[len(want)-1] {
		t.Errorf("log tail of %s: %v", id, st.LiveRuns[0].LogTail)
	}

	var live struct {
		LiveRuns    []LiveRunInfo    `json:"live_runs"`
		ActiveFlows []ActiveFlowInfo `json:"active_flows"`
	}
	c.get("/api/runs?state=live", &live)
	if len(live.LiveRuns) != len(fx.LiveRuns) || len(live.ActiveFlows) != len(fx.ActiveFlows) {
		t.Errorf("live: %+v", live)
	}

	var ended []HistoryRunInfo
	c.get("/api/runs?state=ended", &ended)
	if len(ended) != len(fx.History) {
		t.Errorf("ended: %d runs, want %d", len(ended), len(fx.History))
	}
	c.get("/api/runs?state=ended&actor=codex&status=succeeded", &ended)
	if len(ended) == 0 {
		t.Errorf("filter: no codex runs")
	}
	for _, h := range ended {
		if h.Actor != "codex" || h.Status != "succeeded" {
			t.Errorf("filter let through %s by %s (%s)", h.ID, h.Actor, h.Status)
		}
	}
	var both struct {
		Live    []LiveRunInfo    `json:"live"`
		History []HistoryRunInfo `json:"history"`
	}
	c.get("/api/runs", &both)
	if len(both.Live) != len(fx.LiveRuns) || len(both.History) != len(fx.History) {
		t.Errorf("runs: %d live, %d history", len(both.Live), len(both.History))
	}

	var log struct {
		ID    string         `json:"id"`
		Lines []runs.LogLine `json:"lines"`
	}
	c.get("/api/runs/"+id+"/log?tail=2", &log)
	if log.ID != id || len(log.Lines) != 2 {
		t.Errorf("log tail=2: %+v", log)
	}
	if code, body := c.do(http.MethodGet, "/api/runs/"+id+"/log?raw=1", ""); code != http.StatusOK || body != fx.RawLogs[id] {
		t.Errorf("raw log: %d %q, want fixture's original records", code, body)
	}
	c.get("/api/runs/r-nolog/log", &log)
	if log.Lines == nil || len(log.Lines) != 0 {
		t.Errorf("a run without a log has no lines: %+v", log)
	}

	var issue IssueDetails
	c.get("/api/issues/51", &issue)
	if issue.Issue != 51 || issue.Title == "" {
		t.Errorf("issue 51: %+v", issue)
	}
	var tl IssueTimeline
	c.get("/api/issues/51/timeline?pr=57", &tl)
	if tl.Issue != 51 || len(tl.Events) == 0 {
		t.Errorf("timeline 51: %+v", tl)
	}
	var ms []MilestoneGroup
	c.get("/api/milestones", &ms)
	if len(ms) != len(fx.Milestones) {
		t.Errorf("milestones: %d", len(ms))
	}
	var inbox []InboxItem
	c.get("/api/inbox", &inbox)
	if len(inbox) != len(fx.Inbox) {
		t.Errorf("inbox: %d", len(inbox))
	}

	// Actions do nothing, and succeed unless the fixture says otherwise.
	for _, a := range []string{"pull", "push", "flow", "run", "stop", "after-pull"} {
		if code, body := c.do(http.MethodPost, "/api/actions/"+a, `{}`); code != http.StatusOK || !strings.Contains(body, "not performed") {
			t.Errorf("action %s: %d %s", a, code, body)
		}
	}
	if code, body := c.do(http.MethodPost, "/api/actions/gc", `{}`); code != http.StatusOK || !strings.Contains(body, fx.Actions["gc"].Output) {
		t.Errorf("gc: %d %s", code, body)
	}
	if code, body := c.do(http.MethodPost, "/api/actions/refresh", ``); code != http.StatusOK || !strings.Contains(body, `"live_runs"`) {
		t.Errorf("refresh returns the status: %d %s", code, body)
	}

	// Not in the fixture, or not a route: 404 (400 for a bad number).
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/api/issues/999", http.StatusNotFound},
		{"GET", "/api/issues/999/timeline", http.StatusNotFound},
		{"GET", "/api/issues/0/timeline?pr=999", http.StatusNotFound},
		{"GET", "/api/issues/abc", http.StatusBadRequest},
		{"GET", "/api/nope", http.StatusNotFound},
		{"POST", "/api/nope", http.StatusNotFound},
		{"DELETE", "/api/status", http.StatusNotFound},
		{"GET", "/api/runs/bad.id/log", http.StatusBadRequest},
	} {
		if code, body := c.do(tc.method, tc.path, ""); code != tc.code {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.path, code, body, tc.code)
		}
	}

	// The UI, with the SPA fallback; no sign-in.
	for _, p := range []string{"/", "/history", "/assets/app.js"} {
		if code, body := c.do(http.MethodGet, p, ""); code != http.StatusOK || body == "" {
			t.Errorf("GET %s: %d", p, code)
		}
	}
}

func TestPreviewHostAndOrigin(t *testing.T) {
	srv, c := startPreview(t)
	get := func(host string) int {
		req, _ := http.NewRequest(http.MethodGet, c.base+"/api/status", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(fmt.Sprintf("localhost:%d", srv.port)); code != http.StatusOK {
		t.Errorf("localhost: %d", code)
	}
	for _, host := range []string{"evil.example", fmt.Sprintf("evil.example:%d", srv.port), "127.0.0.1:1"} {
		if code := get(host); code != http.StatusForbidden {
			t.Errorf("host %s: %d, want 403", host, code)
		}
	}

	post := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, c.base+"/api/preview/load", strings.NewReader(`{}`))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(""); code != http.StatusForbidden {
		t.Errorf("no origin: %d", code)
	}
	if code := post("http://evil.example"); code != http.StatusForbidden {
		t.Errorf("foreign origin: %d", code)
	}
	if code := post(c.base); code != http.StatusOK {
		t.Errorf("own origin: %d", code)
	}
}

func TestPreviewControls(t *testing.T) {
	_, c := startPreview(t)
	var info struct {
		Fixture  string   `json:"fixture"`
		Fixtures []string `json:"fixtures"`
		PID      int      `json:"pid"`
	}
	c.get("/api/preview", &info)
	if info.Fixture != "default" || !contains(info.Fixtures, "empty") {
		t.Errorf("info: %+v", info)
	}
	if info.PID != os.Getpid() {
		t.Errorf("info pid %d, want %d", info.PID, os.Getpid())
	}

	var st UIStatusResponse
	c.get("/api/status", &st)
	id, before := st.LiveRuns[0].ID, len(st.LiveRuns[0].LogTail)

	// Appending to a live run's log advances its tail and its log.
	code, body := c.do(http.MethodPost, "/api/preview/logs/"+id, `{"lines": [{"kind": "text", "text": "appended by a test"}]}`)
	if code != http.StatusOK {
		t.Fatalf("append: %d %s", code, body)
	}
	c.get("/api/status", &st)
	got := st.LiveRuns[0].LogTail
	if len(got) != min(before+1, liveTailLines) || got[len(got)-1].Text != "appended by a test" {
		t.Errorf("tail after append: %v", got)
	}
	for _, bad := range []string{`{}`, `{"lines": []}`, `{"line": [{"text": "x"}]}`, `nope`} {
		if code, _ := c.do(http.MethodPost, "/api/preview/logs/"+id, bad); code != http.StatusBadRequest {
			t.Errorf("append %s: %d", bad, code)
		}
	}
	// A run that had no log gets one.
	if code, _ := c.do(http.MethodPost, "/api/preview/logs/r-new", `{"lines": [{"kind": "plain", "text": "x"}]}`); code != http.StatusOK {
		t.Errorf("append to a new log: %d", code)
	}

	// Replacing a section.
	if code, body := c.do(http.MethodPut, "/api/preview/fixture/inbox", `[]`); code != http.StatusOK {
		t.Fatalf("put inbox: %d %s", code, body)
	}
	c.get("/api/status", &st)
	if len(st.Inbox) != 0 {
		t.Errorf("inbox after put: %d", len(st.Inbox))
	}
	if code, _ := c.do(http.MethodPut, "/api/preview/fixture/nope", `[]`); code != http.StatusNotFound {
		t.Errorf("unknown section: %d", code)
	}
	if code, _ := c.do(http.MethodPut, "/api/preview/fixture/inbox", `[{"kind": "x", "bogus": 1}]`); code != http.StatusBadRequest {
		t.Errorf("section with an unknown field: %d", code)
	}

	// Reloading undoes the changes; loading another fixture switches.
	if code, _ := c.do(http.MethodPost, "/api/preview/load", ``); code != http.StatusOK {
		t.Errorf("reload: %d", code)
	}
	c.get("/api/status", &st)
	if len(st.Inbox) == 0 || len(st.LiveRuns[0].LogTail) != before {
		t.Errorf("reload kept changes: %d inbox, %d tail", len(st.Inbox), len(st.LiveRuns[0].LogTail))
	}
	if code, _ := c.do(http.MethodPost, "/api/preview/load", `{"fixture": "empty"}`); code != http.StatusOK {
		t.Errorf("load empty: %d", code)
	}
	c.get("/api/status", &st)
	if len(st.LiveRuns) != 0 || st.LiveRuns == nil || st.Inbox == nil {
		t.Errorf("empty fixture: %+v", st.LiveRuns)
	}
	if code, _ := c.do(http.MethodPost, "/api/preview/load", `{"fixture": "../default"}`); code != http.StatusBadRequest {
		t.Errorf("load a path: %d", code)
	}
	c.get("/api/preview", &info)
	if info.Fixture != "empty" {
		t.Errorf("fixture after a failed load: %s", info.Fixture)
	}
}
