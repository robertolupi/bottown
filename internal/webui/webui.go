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
	"strings"
	"sync"
	"time"

	"github.com/robertolupi/bottown/internal/config"
)

//go:embed all:dist
var distFS embed.FS

// Config configures the host-side web UI.
type Config struct {
	Instance       config.Instance
	RepoTop        string
	FullRepo       string
	OwnerKey       string
	Port           int
	CLIVersion     string
	HarnessChecker func(home string) []HarnessCommandInfo
}

// Server is the local HTTP server for the bottown web UI.
type Server struct {
	cfg          Config
	listener     net.Listener
	port         int
	oneTimeToken string
	tokenUsed    bool
	sessionToken string
	cookieName   string
	mu           sync.Mutex // guards auth state (token/cookie)
	statusMu     sync.Mutex // guards git fetch and status computation
	httpServer   *http.Server
	lastFetch    time.Time
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
	mux.HandleFunc("POST /api/actions/pull", s.handleActionPull)
	mux.HandleFunc("POST /api/actions/push", s.handleActionPush)
	mux.HandleFunc("POST /api/actions/refresh", s.handleActionRefresh)

	// Static files
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(fmt.Sprintf("sub distFS: %v", err))
	}
	fileServer := http.FileServer(http.FS(dist))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Try opening the file
		f, err := dist.Open(path)
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
	allowedHost1 := fmt.Sprintf("127.0.0.1:%d", s.port)
	allowedHost2 := fmt.Sprintf("localhost:%d", s.port)

	allowedOrigin1 := fmt.Sprintf("http://127.0.0.1:%d", s.port)
	allowedOrigin2 := fmt.Sprintf("http://localhost:%d", s.port)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Host check
		host := r.Host
		if !strings.EqualFold(host, allowedHost1) && !strings.EqualFold(host, allowedHost2) {
			http.Error(w, "forbidden: invalid host header", http.StatusForbidden)
			return
		}

		// 2. Strict Content-Security-Policy header
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none';")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")

		// 3. Origin check on state-changing methods (POST, PUT, DELETE, PATCH)
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
				return
			}
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

func (s *Server) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.ComputeStatus(r.Context(), true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleActionRefresh(w http.ResponseWriter, r *http.Request) {
	st, err := s.ComputeStatus(r.Context(), true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
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
	var req pushRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	out, err := s.ExecutePush(r.Context(), req.Head)
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
