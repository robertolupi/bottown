// Package server is bottown's HTTP surface inside the container: health,
// the owner's REST endpoints, the MCP endpoint, and the credential-injecting
// proxies to Forgejo's API and git smart-HTTP. Clients authenticate with
// bottown API keys; Forgejo tokens never leave the container.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

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
	Policy     policy.Policy
	Log        *slog.Logger
	// Audit stores policy decisions (audit.go); nil only logs them.
	Audit *AuditLog
	// GitRoot is where Forgejo keeps its repositories, which review
	// carry-over and clean base merges use; "" turns those operations off.
	GitRoot string

	// Browser sign-in (web.go). PublicForgejoURL is what the owner's
	// browser uses; the cookie names are per instance because every
	// instance shares the 127.0.0.1 cookie jar.
	PublicForgejoURL string
	ForgejoCookie    string
	UICookie         string
	web              webState

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
	mux.Handle("GET /v1/actors", s.authed(http.HandlerFunc(s.listActors)))
	mux.Handle("POST /v1/reconcile", s.authed(s.ownerOnly(http.HandlerFunc(s.postReconcile))))
	// Runs: the owner manages all; an actor may start runs for itself and
	// end or read its own (runs.go).
	mux.Handle("POST /v1/runs", s.authed(http.HandlerFunc(s.postRun)))
	mux.Handle("GET /v1/runs", s.authed(s.ownerOnly(http.HandlerFunc(s.listRuns))))
	mux.Handle("GET /v1/runs/{id}", s.authed(http.HandlerFunc(s.getRun)))
	mux.Handle("DELETE /v1/runs/{id}", s.authed(http.HandlerFunc(s.endRun)))
	mux.Handle("POST /v1/runs/purge", s.authed(s.ownerOnly(http.HandlerFunc(s.purgeRuns))))
	mux.Handle("GET /v1/audit", s.authed(s.ownerOnly(http.HandlerFunc(s.listAudit))))
	s.forgeRoutes(mux)
	mux.Handle("PUT /v1/repos/{name}", s.authed(s.ownerOnly(http.HandlerFunc(s.putRepo))))
	mcpH := s.ready(s.mcpHandler())
	mux.Handle("/mcp", mcpH)
	// Fallback for harnesses that cannot send headers: the key in the path.
	// Nothing logs request paths, and run keys are short-lived.
	mux.Handle("/mcp/k/{key}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.Header.Set("Authorization", "Bearer "+r.PathValue("key"))
		r2.URL.Path, r2.URL.RawPath = "/mcp", ""
		mcpH.ServeHTTP(w, r2)
	}))
	mux.Handle("/forgejo/api/v1/", s.authed(http.HandlerFunc(s.apiProxy)))
	mux.Handle("/git/", s.authed(http.HandlerFunc(s.gitProxy)))
	mux.Handle("POST /v1/web/session", s.authed(s.ownerOnly(http.HandlerFunc(s.postWebSession))))
	mux.Handle("GET /v1/web/login/{ticket}", s.ready(http.HandlerFunc(s.webLogin)))
	mux.Handle("GET /web/forgejo", s.ready(s.uiAuthed(http.HandlerFunc(s.webForgejo))))
	mux.Handle("GET /{$}", s.ready(s.uiAuthed(http.HandlerFunc(s.uiHome))))
	return mux
}

type ctxKey struct{}

// Who returns the authenticated principal of a request.
func Who(ctx context.Context) secrets.Principal {
	p, _ := ctx.Value(ctxKey{}).(secrets.Principal)
	return p
}

// Actor returns the authenticated actor of a request.
func Actor(ctx context.Context) string { return Who(ctx).Actor }

// Lookup resolves an API key; ended or expired run keys do not resolve.
func (s *Server) Lookup(key string) (secrets.Principal, bool) {
	if !strings.HasPrefix(key, secrets.KeyPrefix) {
		return secrets.Principal{}, false
	}
	p, ok, err := s.Store.Lookup(key, time.Now())
	if err != nil {
		s.Log.Error("key lookup", "err", err)
		return secrets.Principal{}, false
	}
	return p, ok
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

// desired is a snapshot of the current desired state. Taking it never waits
// for a reconcile pass, so no request, however long it stays open (an MCP
// event stream, a large git push), holds up a reconcile, and a reconcile
// holds up no request.
func (s *Server) desired() config.Desired { return s.Rec.Spec() }

func (s *Server) authed(next http.Handler) http.Handler {
	return s.ready(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.Lookup(keyFromRequest(r))
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="bottown"`)
			writeError(w, http.StatusUnauthorized, "missing, unknown, ended or expired bottown API key")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	}))
}

func (s *Server) ownerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := Who(r.Context()); p.Actor != s.Owner || p.Run != nil {
			writeError(w, http.StatusForbidden, "owner only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ActorInfo is one actor as GET /v1/actors lists it.
type ActorInfo struct {
	Name    string `json:"name"`
	Family  string `json:"family"`
	Harness string `json:"harness"`
}

func (s *Server) listActors(w http.ResponseWriter, r *http.Request) {
	out := []ActorInfo{}
	for _, a := range s.desired().Actors {
		out = append(out, ActorInfo{Name: a.Name, Family: a.Family, Harness: a.Harness})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	p := Who(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"actor": p.Actor, "instance": s.Instance, "run": p.Run})
}

// postReconcile runs a reconcile pass now (what `bottown up` does when the
// container was already running) and returns the changes it made. It waits
// for a pass already in progress only while the client does, and answers
// 503 if the client gives up first.
func (s *Server) postReconcile(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Rec.Run(r.Context())
	if errors.Is(err, provision.ErrBusy) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	changes := rep.Changes
	if changes == nil {
		changes = []string{}
	}
	if err != nil {
		s.SetHealth(func(h *Health) { h.Error = err.Error() })
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "changes": changes})
		return
	}
	s.SetHealth(func(h *Health) { h.Error, h.Changes = "", changes })
	for _, c := range changes {
		s.Log.Info("change", "what", c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": changes})
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
	p := Who(r.Context())
	actor := p.Actor
	apiPath, ok := policy.Clean(strings.TrimPrefix(r.URL.Path, "/forgejo/api/v1"))
	if !ok || r.URL.RawPath != "" {
		s.audit(p, "api", r.Method, r.URL.Path, "", "reject", "unclean-path", http.StatusBadRequest)
		writeError(w, http.StatusBadRequest, "path must be clean and unencoded")
		return
	}
	d := s.Policy.Decide(actor, r.Method, apiPath)
	if run := p.Run; d.Allow && run != nil {
		d = runScope(run).DecideAPI(r.Method, apiPath)
	}
	if d.Allow && p.ReadOnly && p.Run == nil && r.Method != "GET" && r.Method != "HEAD" {
		d = policy.Decision{Allow: false, Rule: "read-only-key"}
	}
	if !d.Allow {
		s.audit(p, "api", r.Method, apiPath, apiRepo(apiPath), "deny", d.Rule, http.StatusForbidden)
		writeError(w, http.StatusForbidden, "denied by bottown policy rule "+d.Rule)
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
	s.audit(p, "api", r.Method, apiPath, apiRepo(apiPath), "allow", d.Rule, rec.status)
}

func (s *Server) gitProxy(w http.ResponseWriter, r *http.Request) {
	p := Who(r.Context())
	actor := p.Actor
	gitPath, ok := policy.Clean(strings.TrimPrefix(r.URL.Path, "/git"))
	if !ok || r.URL.RawPath != "" {
		s.audit(p, "git", r.Method, r.URL.Path, "", "reject", "unclean-path", http.StatusBadRequest)
		writeError(w, http.StatusBadRequest, "path must be clean and unencoded")
		return
	}
	if p.ReadOnly && p.Run == nil &&
		(r.URL.Query().Get("service") == "git-receive-pack" || strings.HasSuffix(gitPath, "/git-receive-pack")) {
		s.audit(p, "git", r.Method, gitPath, gitRepo(gitPath), "deny", "read-only-key", http.StatusForbidden)
		writeError(w, http.StatusForbidden, "read-only outside a run: start one with `bottown run start`")
		return
	}
	if run := p.Run; run != nil {
		if d := runScope(run).DecideGit(gitPath, r.URL.Query().Get("service")); !d.Allow {
			s.audit(p, "git", r.Method, gitPath, gitRepo(gitPath), "deny", d.Rule, http.StatusForbidden)
			writeError(w, http.StatusForbidden, "denied by bottown policy rule "+d.Rule)
			return
		}
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
		s.audit(p, "git", r.Method, gitPath, gitRepo(gitPath), "allow", "git-push", rec.status)
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

func runScope(run *secrets.Run) policy.RunScope {
	return policy.RunScope{Org: run.Org, Repo: run.Repo, Role: run.Kind}
}

// audit logs a proxy decision, and stores it unless it is an allowed read:
// the audit trail is of writes and refusals, not of every page view.
func (s *Server) audit(p secrets.Principal, op, method, path, repo, decision, rule string, status int) {
	if decision == "allow" && (method == http.MethodGet || method == http.MethodHead) {
		s.Log.Info("forge", "actor", p.Actor, "run", runID(p), "method", method, "path", path, "decision", decision, "rule", rule, "status", status)
		return
	}
	s.record(AuditEntry{Actor: p.Actor, Run: runID(p), Repo: repo, Op: op, Method: method, Path: path,
		Decision: decision, Rule: rule, Status: status})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
