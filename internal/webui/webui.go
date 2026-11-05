// Package webui implements the local, host-side web UI for bottown.
// It serves embedded assets and provides APIs for upstream and repository health.
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

// distFS holds the web UI that `make web` builds into dist/ui. dist/ui is
// not tracked: dist/README.md keeps the embed valid in a checkout that did
// not build it, and the server then serves stubPage instead.
//
//go:embed all:dist
var distFS embed.FS

// stubPage is the web UI of a binary built without `make web`.
const stubPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>bottown</title></head>
<body style="font-family: sans-serif; max-width: 40em; margin: 4em auto;">
<h1>bottown: web UI not built</h1>
<p>This bottown binary was built without the web UI. Build it with
<code>make web</code> (it needs Node and npm), then rebuild the binary:
<code>make install</code> does both.</p>
<p>The API under <code>/api/</code> works without it.</p>
</body>
</html>
`

// uiFS returns the web UI built into dist/ui of dist, or nil when it was
// not built.
func uiFS(dist fs.FS) fs.FS {
	ui, err := fs.Sub(dist, "dist/ui")
	if err != nil {
		panic(fmt.Sprintf("sub distFS: %v", err))
	}
	if _, err := fs.Stat(ui, "index.html"); err != nil {
		return nil
	}
	return ui
}

// Config configures the host-side web UI.
type Config struct {
	Instance       config.Instance
	RepoTop        string
	FullRepo       string
	OwnerKey       string
	Port           int
	CLIVersion     string
	BottownPath    string
	HarnessChecker func(home string) []HarnessCommandInfo
}

// Server is the local HTTP server for the bottown web UI.
type Server struct {
	cfg                 Config
	listener            net.Listener
	port                int
	oneTimeToken        string
	tokenUsed           bool
	sessionToken        string
	cookieName          string
	mu                  sync.Mutex // guards auth state (token/cookie)
	statusMu            sync.Mutex // guards git fetch and status computation
	afterPullMu         sync.Mutex // one run of the after-pull steps at a time
	httpServer          *http.Server
	lastStatusMu        sync.RWMutex
	lastStatus          *UIStatusResponse
	lastFetch           time.Time
	lastSuccessfulFetch time.Time
	lastOriginErr       string
}

// NewServer creates a new web UI server bound to 127.0.0.1.
func NewServer(cfg Config) (*Server, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		ln.Close()
		return nil, fmt.Errorf("expected TCPAddr, got %T", ln.Addr())
	}

	s := &Server{
		cfg:          cfg,
		listener:     ln,
		port:         tcpAddr.Port,
		oneTimeToken: randomString(32),
		sessionToken: randomString(32),
		cookieName:   fmt.Sprintf("bottown_ui_%d", tcpAddr.Port),
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Wrap handlers with Security middleware: Host check, Origin check, CSP, Auth
	handler := s.securityMiddleware(mux)

	s.httpServer = &http.Server{
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	return s, nil
}

// Port returns the bound port.
func (s *Server) Port() int { return s.port }

// BaseURL returns the local base URL (e.g. http://127.0.0.1:54321).
func (s *Server) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// LoginURL returns the one-time URL containing the login token.
func (s *Server) LoginURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.port, s.oneTimeToken)
}

// Serve starts listening and serving until ctx is cancelled or server is closed.
func (s *Server) Serve(ctx context.Context) error {
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

// Close closes the server and listener.
func (s *Server) Close() error {
	if s.httpServer != nil {
		return s.httpServer.Close()
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", s.handleGetStatus)
	mux.HandleFunc("GET /api/inbox", s.handleGetInbox)
	mux.HandleFunc("GET /api/milestones", s.handleGetMilestones)
	mux.HandleFunc("GET /api/runs", s.handleGetRuns)
	mux.HandleFunc("GET /api/runs/{id}/log", s.handleGetRunLog)
	mux.HandleFunc("GET /api/issues/{number}", s.handleGetIssueDetails)
	mux.HandleFunc("GET /api/issues/{number}/timeline", s.handleGetIssueTimeline)
	mux.HandleFunc("POST /api/actions/pull", s.handleActionPull)
	mux.HandleFunc("POST /api/actions/push", s.handleActionPush)
	mux.HandleFunc("POST /api/actions/after-pull", s.handleActionAfterPull)
	mux.HandleFunc("POST /api/actions/refresh", s.handleActionRefresh)
	mux.HandleFunc("POST /api/actions/flow", s.handleActionFlow)
	mux.HandleFunc("POST /api/actions/merge-base", s.handleActionMergeBase)
	mux.HandleFunc("POST /api/actions/run", s.handleActionRun)
	mux.HandleFunc("POST /api/actions/stop", s.handleActionStop)
	mux.HandleFunc("POST /api/actions/gc", s.handleActionGC)

	mux.Handle("GET /", staticHandler(uiFS(distFS)))
}

// staticHandler serves the built web UI in ui, or stubPage when ui is nil.
func staticHandler(ui fs.FS) http.Handler {
	if ui == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(stubPage))
		})
	}
	fileServer := http.FileServer(http.FS(ui))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The assets have fixed names (web/vite.config.ts), so a new binary
		// serves new content at the same URLs: make browsers revalidate.
		w.Header().Set("Cache-Control", "no-cache")
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Try opening the file
		f, err := ui.Open(path)
		if err != nil {
			// SPA fallback: serve index.html for non-existent files (except /api/)
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				r.URL.Path = "/"
				fileServer.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		_ = f.Close()
		fileServer.ServeHTTP(w, r)
	})
}

// securityMiddleware enforces:
// 1. Host header check (against DNS rebinding)
// 2. Strict Content-Security-Policy (no inline scripts)
// 3. Origin header check on state-changing requests (CSRF protection)
// 4. One-time token exchange for HttpOnly SameSite=Strict session cookie
func (s *Server) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1-3. Host check, CSP, Origin check
		if !localRequestAllowed(w, r, s.port) {
			return
		}

		// 4. One-time token exchange
		if token := r.URL.Query().Get("token"); token != "" {
			s.mu.Lock()
			if !s.tokenUsed && token == s.oneTimeToken {
				s.tokenUsed = true
				s.mu.Unlock()

				// Set HttpOnly SameSite=Strict cookie
				http.SetCookie(w, &http.Cookie{
					Name:     s.cookieName,
					Value:    s.sessionToken,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})

				// Redirect to strip token from URL address bar
				cleanURL := *r.URL
				q := cleanURL.Query()
				q.Del("token")
				cleanURL.RawQuery = q.Encode()
				if cleanURL.Path == "" {
					cleanURL.Path = "/"
				}
				http.Redirect(w, r, cleanURL.String(), http.StatusSeeOther)
				return
			}
			s.mu.Unlock()
			http.Error(w, "unauthorized: login token expired or invalid", http.StatusUnauthorized)
			return
		}

		// Verify session cookie
		cookie, err := r.Cookie(s.cookieName)
		if err != nil || cookie.Value != s.sessionToken {
			http.Error(w, "unauthorized: please open the web UI using the URL provided by `bottown web ui`", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// localRequestAllowed enforces, for a server on 127.0.0.1:port, the
// checks that do not depend on a session: the Host header (against DNS
// rebinding), a strict Content-Security-Policy (no inline scripts), and
// the Origin header on state-changing requests (CSRF protection). It
// writes the error and returns false when the request is refused.
func localRequestAllowed(w http.ResponseWriter, r *http.Request, port int) bool {
	allowedHost1 := fmt.Sprintf("127.0.0.1:%d", port)
	allowedHost2 := fmt.Sprintf("localhost:%d", port)

	allowedOrigin1 := fmt.Sprintf("http://127.0.0.1:%d", port)
	allowedOrigin2 := fmt.Sprintf("http://localhost:%d", port)

	host := r.Host
	if !strings.EqualFold(host, allowedHost1) && !strings.EqualFold(host, allowedHost2) {
		http.Error(w, "forbidden: invalid host header", http.StatusForbidden)
		return false
	}

	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none';")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")

	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Also check Referer if Origin is omitted by some clients
			if ref := r.Header.Get("Referer"); ref != "" {
				if u, err := url.Parse(ref); err == nil {
					origin = u.Scheme + "://" + u.Host
				}
			}
		}
		if origin != allowedOrigin1 && origin != allowedOrigin2 {
			http.Error(w, "forbidden: invalid origin header", http.StatusForbidden)
			return false
		}
	}
	return true
}

func (s *Server) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	st, err := s.ComputeStatus(r.Context(), force)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGetInbox(w http.ResponseWriter, r *http.Request) {
	inbox, err := s.ComputeInbox(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if inbox == nil {
		inbox = []InboxItem{}
	}
	writeJSON(w, http.StatusOK, inbox)
}

func (s *Server) handleGetMilestones(w http.ResponseWriter, r *http.Request) {
	groups, err := s.ComputeMilestones(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) handleActionRefresh(w http.ResponseWriter, r *http.Request) {
	st, err := s.ComputeStatus(r.Context(), true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleActionMergeBase(w http.ResponseWriter, r *http.Request) {
	var req MergeBaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{Error: "invalid JSON body: " + err.Error()})
		return
	}
	out, err := s.ExecuteMergeBase(r.Context(), req)
	res := ActionResult{Success: err == nil, Output: out}
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusInternalServerError, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleActionPull(w http.ResponseWriter, r *http.Request) {
	out, err := s.ExecutePull(r.Context())
	res := ActionResult{
		Success: err == nil,
		Output:  out,
	}
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusInternalServerError, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type pushRequest struct {
	Head string `json:"head"`
}

func (s *Server) handleActionPush(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{
			Error: "missing request body",
		})
		return
	}
	var req pushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{
			Error: "invalid JSON body: " + err.Error(),
		})
		return
	}
	head := strings.TrimSpace(req.Head)
	if head == "" {
		writeJSON(w, http.StatusBadRequest, ActionResult{
			Error: "head commit SHA is required",
		})
		return
	}
	out, err := s.ExecutePush(r.Context(), head)
	res := ActionResult{
		Success: err == nil,
		Output:  out,
	}
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusInternalServerError, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func randomString(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Check if file exists
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *Server) handleGetRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "live" {
		live, err := s.ComputeLiveRuns(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if live == nil {
			live = []LiveRunInfo{}
		}
		flows := s.ComputeActiveFlows(r.Context(), live)
		writeJSON(w, http.StatusOK, map[string]any{
			"live_runs":    live,
			"active_flows": flows,
		})
		return
	}

	filter := HistoryFilter{
		Actor:  q.Get("actor"),
		Kind:   q.Get("kind"),
		Status: q.Get("status"),
	}
	if sinceStr := q.Get("since"); sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			filter.Since = t
		}
	}

	history, err := s.ComputeHistoryRuns(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if history == nil {
		history = []HistoryRunInfo{}
	}

	if state == "ended" {
		writeJSON(w, http.StatusOK, history)
		return
	}

	live, _ := s.ComputeLiveRuns(r.Context())
	if live == nil {
		live = []LiveRunInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"live":    live,
		"history": history,
	})
}

var validRunID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func (s *Server) handleGetRunLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || strings.Contains(id, "..") || !validRunID.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run ID"})
		return
	}

	logPath := filepath.Join(s.cfg.RepoTop, ".bottown", "runs", id+".log")
	logBytes, err := runs.ReadLog(logPath)
	if err != nil {
		if r.URL.Query().Get("raw") == "1" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":    id,
			"lines": []runs.LogLine{},
		})
		return
	}
	if r.URL.Query().Get("raw") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(runs.RedactEncoded(logBytes))
		return
	}

	lines := s.renderRunLog(r.Context(), id, logBytes)
	if tailStr := r.URL.Query().Get("tail"); tailStr != "" {
		if n, err := strconv.Atoi(tailStr); err == nil && n > 0 && len(lines) > n {
			lines = lines[len(lines)-n:]
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":    id,
		"lines": lines,
	})
}

// renderRunLog renders run id's log, read as logBytes, for its harness.
func (s *Server) renderRunLog(ctx context.Context, id string, logBytes []byte) []runs.LogLine {
	actorMap, _ := s.fetchActorMap(ctx)
	harness := ""
	var serverRun secrets.Run
	if err := s.callServer(ctx, http.MethodGet, "/v1/runs/"+id, nil, &serverRun); err == nil {
		harness = serverRun.Harness
		if harness == "" && actorMap[serverRun.Actor] != nil {
			harness = actorMap[serverRun.Actor].Harness
		}
	}
	return runs.RenderLog(harness, logBytes)
}

// ComputeRunLog returns run id's rendered log as GET /api/runs/{id}/log
// does, and whether the run has a log in this checkout.
func (s *Server) ComputeRunLog(ctx context.Context, id string) ([]runs.LogLine, bool, error) {
	if !validRunID.MatchString(id) {
		return nil, false, fmt.Errorf("invalid run ID %q", id)
	}
	logBytes, err := runs.ReadLog(filepath.Join(s.cfg.RepoTop, ".bottown", "runs", id+".log"))
	if err != nil {
		return []runs.LogLine{}, false, nil
	}
	return s.renderRunLog(ctx, id, logBytes), true, nil
}

func (s *Server) handleGetIssueDetails(w http.ResponseWriter, r *http.Request) {
	issueNum, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || issueNum <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid issue number required"})
		return
	}
	details, err := s.ComputeIssueDetails(r.Context(), issueNum)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, details)
}

func (s *Server) handleGetIssueTimeline(w http.ResponseWriter, r *http.Request) {
	numStr := r.PathValue("number")
	issueNum, err := strconv.ParseInt(numStr, 10, 64)
	var prFilter int64 = -1
	if prStr := r.URL.Query().Get("pr"); prStr != "" {
		parsed, prErr := strconv.ParseInt(prStr, 10, 64)
		if prErr != nil || parsed < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid pr number required"})
			return
		}
		prFilter = parsed
	}
	if err != nil || (issueNum <= 0 && prFilter <= 0) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid issue number required"})
		return
	}

	timeline, err := s.ComputeFlowTimeline(r.Context(), issueNum, prFilter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, timeline)
}

func (s *Server) handleActionFlow(w http.ResponseWriter, r *http.Request) {
	var req FlowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{Error: "invalid JSON body: " + err.Error()})
		return
	}
	out, err := s.ExecuteFlow(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{Success: true, Output: out})
}

func (s *Server) handleActionRun(w http.ResponseWriter, r *http.Request) {
	var req RunActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{Error: "invalid JSON body: " + err.Error()})
		return
	}
	out, err := s.ExecuteRun(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{Success: true, Output: out})
}

func (s *Server) handleActionStop(w http.ResponseWriter, r *http.Request) {
	var req StopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{Error: "invalid JSON body: " + err.Error()})
		return
	}
	out, err := s.ExecuteStop(r.Context(), req.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ActionResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{Success: true, Output: out})
}

func (s *Server) handleActionGC(w http.ResponseWriter, r *http.Request) {
	var req GCRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	out, err := s.ExecuteGC(r.Context(), req.Failed)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ActionResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{Success: true, Output: out})
}
