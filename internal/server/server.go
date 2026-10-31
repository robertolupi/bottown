// Package server is bottown's HTTP surface inside the container: health,
// the owner's REST endpoints, the MCP endpoint, and the credential-injecting
// proxies to Forgejo's API and git smart-HTTP. Clients authenticate with
// bottown API keys; Forgejo tokens never leave the container.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// Server is the bottown HTTP server.
type Server struct {
	Instance   string
	Version    string
	ForgejoURL *url.URL // in-container Forgejo, e.g. http://127.0.0.1:3000
	Owner      string
	Store      *secrets.Store
	Rec        *provision.Reconciler
	Desired    config.Desired
	Policy     policy.Policy
	Log        *slog.Logger

	mu     sync.Mutex
	health Health
}

// Health is the /healthz document.
type Health struct {
	Instance       string   `json:"instance"`
	Version        string   `json:"version"`
	Forgejo        string   `json:"forgejo"` // starting | ready
	ForgejoVersion string   `json:"forgejo_version,omitempty"`
	Reconciled     bool     `json:"reconciled"`
	Error          string   `json:"error,omitempty"`
	Changes        []string `json:"changes"`
}

// SetHealth replaces the health document.
func (s *Server) SetHealth(fn func(*Health)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.health)
}

func (s *Server) healthSnapshot() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.health
	h.Instance, h.Version = s.Instance, s.Version
	if h.Forgejo == "" {
		h.Forgejo = "starting"
	}
	if h.Changes == nil {
		h.Changes = []string{}
	}
	return h
}

// Handler returns the server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.healthSnapshot())
	})
	mux.Handle("GET /v1/whoami", s.authed(http.HandlerFunc(s.whoami)))
	mux.Handle("PUT /v1/repos/{name}", s.authed(s.ownerOnly(http.HandlerFunc(s.putRepo))))
	mux.Handle("/mcp", s.ready(s.mcpHandler()))
	mux.Handle("/forgejo/api/v1/", s.authed(http.HandlerFunc(s.apiProxy)))
	mux.Handle("/git/", s.authed(http.HandlerFunc(s.gitProxy)))
	return mux
}

type ctxKey struct{}

// Actor returns the authenticated actor of a request.
func Actor(ctx context.Context) string {
	a, _ := ctx.Value(ctxKey{}).(string)
	return a
}

// LookupKey resolves an API key to an actor.
func (s *Server) LookupKey(key string) (string, bool) {
	if !strings.HasPrefix(key, secrets.KeyPrefix) {
		return "", false
	}
	actor, ok, err := s.Store.LookupKey(key)
	if err != nil {
		s.Log.Error("key lookup", "err", err)
		return "", false
	}
	return actor, ok
}

// keyFromRequest accepts "Bearer <key>" or HTTP Basic with the key as
// password (what git's credential helper sends).
func keyFromRequest(r *http.Request) string {
	if _, pw, ok := r.BasicAuth(); ok {
		return pw
	}
	h := r.Header.Get("Authorization")
	if k, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(k)
	}
	return ""
}

func (s *Server) ready(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.healthSnapshot().Reconciled {
			writeError(w, http.StatusServiceUnavailable, "bottown is still reconciling; see /healthz")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authed(next http.Handler) http.Handler {
	return s.ready(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := s.LookupKey(keyFromRequest(r))
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="bottown"`)
			writeError(w, http.StatusUnauthorized, "missing or unknown bottown API key")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, actor)))
	}))
}

func (s *Server) ownerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Actor(r.Context()) != s.Owner {
			writeError(w, http.StatusForbidden, "owner only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"actor": Actor(r.Context()), "instance": s.Instance})
}

func (s *Server) putRepo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DefaultBranch string `json:"default_branch"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	name := r.PathValue("name")
	repo, rep, err := s.Rec.EnsureRepo(r.Context(), name, body.DefaultBranch)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	changes := rep.Changes
	if changes == nil {
		changes = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"full_name":      repo.FullName,
		"empty":          repo.Empty,
		"default_branch": repo.DefaultBranch,
		"git_path":       "/git/" + repo.FullName + ".git",
		"changes":        changes,
	})
}

func (s *Server) actorToken(actor string) (string, bool) {
	st, err := s.Store.Read()
	if err != nil {
		s.Log.Error("read secrets", "err", err)
		return "", false
	}
	t, ok := st.Tokens[actor]
	return t.Token, ok && t.Token != ""
}

// stripClientAuth removes every way a client could choose its own Forgejo
// identity, so the only credential Forgejo sees is the one bottown injects.
func stripClientAuth(h http.Header, q url.Values) {
	for _, k := range []string{"Authorization", "Sudo", "X-Gitea-Sudo", "X-Forgejo-Sudo", "Cookie", "X-Webauth-User", "X-Forwarded-User"} {
		h.Del(k)
	}
	for _, k := range []string{"sudo", "token", "access_token"} {
		q.Del(k)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) apiProxy(w http.ResponseWriter, r *http.Request) {
	actor := Actor(r.Context())
	apiPath, ok := policy.Clean(strings.TrimPrefix(r.URL.Path, "/forgejo/api/v1"))
	if !ok || r.URL.RawPath != "" {
		s.audit(actor, r.Method, r.URL.Path, "reject", "unclean-path", http.StatusBadRequest)
		writeError(w, http.StatusBadRequest, "path must be clean and unencoded")
		return
	}
	d := s.Policy.Decide(actor, r.Method, apiPath)
	if !d.Allow {
		s.audit(actor, r.Method, apiPath, "deny", d.Rule, http.StatusForbidden)
		writeError(w, http.StatusForbidden, "denied by bottown policy rule "+d.Rule+"; this write is performed by bottown")
		return
	}
	token, ok := s.actorToken(actor)
	if !ok {
		writeError(w, http.StatusForbidden, "no forge token for actor "+actor)
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.proxy("/api/v1"+apiPath, func(out *http.Request) {
		out.Header.Set("Authorization", "token "+token)
	}).ServeHTTP(rec, r)
	s.audit(actor, r.Method, apiPath, "allow", d.Rule, rec.status)
}

func (s *Server) gitProxy(w http.ResponseWriter, r *http.Request) {
	actor := Actor(r.Context())
	gitPath, ok := policy.Clean(strings.TrimPrefix(r.URL.Path, "/git"))
	if !ok || r.URL.RawPath != "" {
		writeError(w, http.StatusBadRequest, "path must be clean and unencoded")
		return
	}
	token, ok := s.actorToken(actor)
	if !ok {
		writeError(w, http.StatusForbidden, "no forge token for actor "+actor)
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.proxy(gitPath, func(out *http.Request) {
		out.SetBasicAuth(actor, token)
	}).ServeHTTP(rec, r)
	if strings.HasSuffix(gitPath, "/git-receive-pack") {
		s.audit(actor, r.Method, gitPath, "allow", "git-push", rec.status)
	}
}

func (s *Server) proxy(path string, auth func(*http.Request)) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			q := pr.In.URL.Query()
			stripClientAuth(pr.Out.Header, q)
			pr.Out.URL.Scheme = s.ForgejoURL.Scheme
			pr.Out.URL.Host = s.ForgejoURL.Host
			pr.Out.URL.Path = path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = q.Encode()
			pr.Out.Host = s.ForgejoURL.Host
			auth(pr.Out)
		},
		FlushInterval: -1,
	}
}

func (s *Server) audit(actor, method, path, decision, rule string, status int) {
	s.Log.Info("forge", "actor", actor, "method", method, "path", path, "decision", decision, "rule", rule, "status", status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
