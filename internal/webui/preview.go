package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robertolupi/bottown/internal/runs"
)

// liveTailLines is how many rendered log lines a live run carries, as in
// ComputeLiveRuns.
const liveTailLines = 25

// Fixture is one preview scenario: what the API answers in place of a live
// instance. It is read from web/fixtures/<name>.json, strictly, so a field
// that the UI's types do not have is an error rather than silently dropped.
type Fixture struct {
	// Status is GET /api/status. Its inbox, live_runs and active_flows are
	// replaced by the fixture's own, so the views agree with each other.
	Status      UIStatusResponse `json:"status"`
	Inbox       []InboxItem      `json:"inbox"`
	LiveRuns    []LiveRunInfo    `json:"live_runs"`
	ActiveFlows []ActiveFlowInfo `json:"active_flows"`
	History     []HistoryRunInfo `json:"history"`
	Milestones  []MilestoneGroup `json:"milestones"`
	// Logs are the rendered logs by run ID: GET /api/runs/{id}/log, and a
	// live run's log_tail (its last lines) when the run has one here.
	Logs map[string][]runs.LogLine `json:"logs"`
	// Issues are GET /api/issues/{number}, keyed by number.
	Issues map[string]IssueDetails `json:"issues"`
	// Timelines are GET /api/issues/{number}/timeline, keyed by issue
	// number, or by "!N" for a PR without an issue (?pr=N).
	Timelines map[string]IssueTimeline `json:"timelines"`
	// Actions are the answers to POST /api/actions/{name}, by name (pull,
	// push, after-pull, flow, run, stop, gc). An action without one
	// succeeds, doing nothing.
	Actions map[string]ActionResult `json:"actions"`
}

// LoadFixture reads the fixture name from dir.
func LoadFixture(dir, name string) (*Fixture, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("invalid fixture name %q", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no fixture %q in %s (have: %s)", name, dir, strings.Join(FixtureNames(dir), ", "))
	}
	if err != nil {
		return nil, err
	}
	fx, err := decodeFixture(b)
	if err != nil {
		return nil, fmt.Errorf("fixture %s: %w", name, err)
	}
	return fx, nil
}

func decodeFixture(b []byte) (*Fixture, error) {
	var fx Fixture
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fx); err != nil {
		return nil, err
	}
	return &fx, nil
}

// FixtureNames lists the fixtures in dir, sorted.
func FixtureNames(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var names []string
	for _, m := range matches {
		names = append(names, strings.TrimSuffix(filepath.Base(m), ".json"))
	}
	sort.Strings(names)
	return names
}

// PreviewConfig configures a PreviewServer.
type PreviewConfig struct {
	Port int
	// UI is the built web UI to serve; nil serves the stub page.
	UI fs.FS
	// FixturesDir holds the fixtures, and Fixture names the one to start
	// with.
	FixturesDir string
	Fixture     string
}

// PreviewServer serves a web UI build with its API answered from a
// fixture: `bottown web preview`. It reaches no instance, needs no
// sign-in, and has the same Host and Origin checks as the web UI.
type PreviewServer struct {
	cfg        PreviewConfig
	listener   net.Listener
	port       int
	httpServer *http.Server

	mu   sync.Mutex // guards name and fx
	name string
	fx   *Fixture
}

// NewPreviewServer loads the fixture and binds to 127.0.0.1.
func NewPreviewServer(cfg PreviewConfig) (*PreviewServer, error) {
	fx, err := LoadFixture(cfg.FixturesDir, cfg.Fixture)
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	s := &PreviewServer{
		cfg:      cfg,
		listener: ln,
		port:     ln.Addr().(*net.TCPAddr).Port,
		name:     cfg.Fixture,
		fx:       fx,
	}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	s.httpServer = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if localRequestAllowed(w, r, s.port) {
				mux.ServeHTTP(w, r)
			}
		}),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	return s, nil
}

// BaseURL returns the local base URL.
func (s *PreviewServer) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// Serve serves until ctx is cancelled or the server is closed.
func (s *PreviewServer) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(s.listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// Close closes the server and its listener.
func (s *PreviewServer) Close() error {
	return s.httpServer.Close()
}

func (s *PreviewServer) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/actions/refresh", s.handleStatus)
	mux.HandleFunc("GET /api/inbox", s.handleInbox)
	mux.HandleFunc("GET /api/milestones", s.handleMilestones)
	mux.HandleFunc("GET /api/runs", s.handleRuns)
	mux.HandleFunc("GET /api/runs/{id}/log", s.handleRunLog)
	mux.HandleFunc("GET /api/issues/{number}", s.handleIssue)
	mux.HandleFunc("GET /api/issues/{number}/timeline", s.handleTimeline)
	mux.HandleFunc("POST /api/actions/{name}", s.handleAction)

	// Preview controls, for a test or a person to change what the API
	// answers while the page is open.
	mux.HandleFunc("GET /api/preview", s.handlePreviewInfo)
	mux.HandleFunc("POST /api/preview/load", s.handlePreviewLoad)
	mux.HandleFunc("PUT /api/preview/fixture/{section}", s.handlePreviewSection)
	mux.HandleFunc("POST /api/preview/logs/{id}", s.handlePreviewAppendLog)

	static := staticHandler(s.cfg.UI)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not in the preview: " + r.Method + " " + r.URL.Path})
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		default:
			static.ServeHTTP(w, r)
		}
	})
}

// snapshot returns what f makes of the current fixture, under s.mu.
func (s *PreviewServer) snapshot(f func(fx *Fixture) any) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return f(s.fx)
}

// liveRuns returns the fixture's live runs, each with the tail of its log
// when the fixture has one.
func liveRuns(fx *Fixture) []LiveRunInfo {
	out := make([]LiveRunInfo, 0, len(fx.LiveRuns))
	for _, r := range fx.LiveRuns {
		if log, ok := fx.Logs[r.ID]; ok {
			r.LogTail = tail(log, liveTailLines)
		}
		if r.LogTail == nil {
			r.LogTail = []runs.LogLine{}
		}
		if r.Denials == nil {
			r.Denials = []Denial{}
		}
		out = append(out, r)
	}
	return out
}

func tail(lines []runs.LogLine, n int) []runs.LogLine {
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return append([]runs.LogLine{}, lines...)
}

func (s *PreviewServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot(func(fx *Fixture) any {
		st := fx.Status
		st.Inbox = orEmpty(fx.Inbox)
		st.LiveRuns = liveRuns(fx)
		st.ActiveFlows = orEmpty(fx.ActiveFlows)
		st.Actors = orEmpty(st.Actors)
		return st
	}))
}

func (s *PreviewServer) handleInbox(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot(func(fx *Fixture) any { return orEmpty(fx.Inbox) }))
}

func (s *PreviewServer) handleMilestones(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshot(func(fx *Fixture) any { return orEmpty(fx.Milestones) }))
}

func (s *PreviewServer) handleRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since time.Time
	if v := q.Get("since"); v != "" {
		since, _ = time.Parse(time.RFC3339, v)
	}
	writeJSON(w, http.StatusOK, s.snapshot(func(fx *Fixture) any {
		live := liveRuns(fx)
		if q.Get("state") == "live" {
			return map[string]any{"live_runs": live, "active_flows": orEmpty(fx.ActiveFlows)}
		}
		history := []HistoryRunInfo{}
		for _, h := range fx.History {
			if (q.Get("actor") != "" && h.Actor != q.Get("actor")) ||
				(q.Get("kind") != "" && h.Kind != q.Get("kind")) ||
				(q.Get("status") != "" && h.Status != q.Get("status")) ||
				(!since.IsZero() && h.Created.Before(since)) {
				continue
			}
			history = append(history, h)
		}
		if q.Get("state") == "ended" {
			return history
		}
		return map[string]any{"live": live, "history": history}
	}))
}

func (s *PreviewServer) handleRunLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validRunID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run ID"})
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	writeJSON(w, http.StatusOK, s.snapshot(func(fx *Fixture) any {
		lines := fx.Logs[id]
		if n > 0 {
			lines = tail(lines, n)
		}
		return map[string]any{"id": id, "lines": orEmpty(lines)}
	}))
}

func (s *PreviewServer) handleIssue(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || n <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid issue number required"})
		return
	}
	s.mu.Lock()
	d, ok := s.fx.Issues[strconv.FormatInt(n, 10)]
	name := s.name
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("issue #%d is not in fixture %s", n, name)})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *PreviewServer) handleTimeline(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	var pr int64
	if v := r.URL.Query().Get("pr"); v != "" {
		pr, _ = strconv.ParseInt(v, 10, 64)
	}
	if err != nil || (n <= 0 && pr <= 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid issue number required"})
		return
	}
	key := strconv.FormatInt(n, 10)
	if n <= 0 {
		key = "!" + strconv.FormatInt(pr, 10)
	}
	s.mu.Lock()
	tl, ok := s.fx.Timelines[key]
	name := s.name
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("timeline %s is not in fixture %s", key, name)})
		return
	}
	tl.Events = orEmpty(tl.Events)
	writeJSON(w, http.StatusOK, tl)
}

func (s *PreviewServer) handleAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	res, ok := s.fx.Actions[name]
	s.mu.Unlock()
	if !ok {
		res = ActionResult{Success: true, Output: fmt.Sprintf("preview: %s not performed", name)}
	}
	code := http.StatusOK
	if !res.Success {
		code = http.StatusInternalServerError
	}
	writeJSON(w, code, res)
}

func (s *PreviewServer) handlePreviewInfo(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	name := s.name
	s.mu.Unlock()
	// pid identifies the server answering here, so a state file naming a
	// pid can tell whether that pid is still this preview.
	writeJSON(w, http.StatusOK, map[string]any{
		"fixture":  name,
		"fixtures": orEmpty(FixtureNames(s.cfg.FixturesDir)),
		"pid":      os.Getpid(),
	})
}

// handlePreviewLoad reloads a fixture from disk, the current one when the
// body names none: it undoes the changes made through the other controls.
func (s *PreviewServer) handlePreviewLoad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fixture string `json:"fixture"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := req.Fixture
	if name == "" {
		name = s.name
	}
	fx, err := LoadFixture(s.cfg.FixturesDir, name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.name, s.fx = name, fx
	writeJSON(w, http.StatusOK, map[string]string{"fixture": name})
}

// handlePreviewSection replaces one top-level section of the fixture
// (status, inbox, live_runs, …) with the request body.
func (s *PreviewServer) handlePreviewSection(w http.ResponseWriter, r *http.Request) {
	section := r.PathValue("section")
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := json.Marshal(s.fx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var sections map[string]json.RawMessage
	_ = json.Unmarshal(cur, &sections)
	if _, ok := sections[section]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no fixture section %q", section)})
		return
	}
	sections[section] = body
	b, _ := json.Marshal(sections)
	fx, err := decodeFixture(b)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("section %s: %v", section, err)})
		return
	}
	s.fx = fx
	writeJSON(w, http.StatusOK, map[string]string{"section": section})
}

// handlePreviewAppendLog appends lines to a run's log, as a live run would
// write them: the run's log and its live tail show them on the next poll.
func (s *PreviewServer) handlePreviewAppendLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validRunID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run ID"})
		return
	}
	var req struct {
		Lines []runs.LogLine `json:"lines"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || len(req.Lines) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"lines": [{"kind": "text", "text": "…"}, …]}`})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fx.Logs == nil {
		s.fx.Logs = map[string][]runs.LogLine{}
	}
	s.fx.Logs[id] = append(s.fx.Logs[id], req.Lines...)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "lines": len(s.fx.Logs[id])})
}
