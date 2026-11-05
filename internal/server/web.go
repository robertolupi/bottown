package server

import (
	"encoding/json"
	"errors"
	"html/template"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// Browser sign-in without copying credentials. The host CLI (authenticated
// with the owner's API key) asks for a one-time login link; opening it sets
// a cookie and redirects. Cookies are host-scoped, not port-scoped, so a
// cookie set by bottown on 127.0.0.1:<sp> is sent to Forgejo on
// 127.0.0.1:<fp>.

const (
	ticketTTL    = 60 * time.Second
	uiSessionTTL = 12 * time.Hour
)

type ticket struct {
	target  string // "forgejo" or "ui"
	next    string // path to land on after sign-in
	expires time.Time
}

type webState struct {
	mu         sync.Mutex
	tickets    map[string]ticket
	uiSessions map[string]time.Time // session id -> expiry; owner only
}

func (w *webState) newTicket(target, next string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tickets == nil {
		w.tickets = map[string]ticket{}
	}
	now := time.Now()
	for k, t := range w.tickets {
		if now.After(t.expires) {
			delete(w.tickets, k)
		}
	}
	id := secrets.RandomString(24)
	w.tickets[id] = ticket{target: target, next: next, expires: now.Add(ticketTTL)}
	return id
}

// redeem consumes a ticket; each works once.
func (w *webState) redeem(id string) (ticket, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	t, ok := w.tickets[id]
	delete(w.tickets, id)
	return t, ok && time.Now().Before(t.expires)
}

func (w *webState) newUISession() (string, time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.uiSessions == nil {
		w.uiSessions = map[string]time.Time{}
	}
	// Sessions nobody checks again would otherwise stay for good.
	now := time.Now()
	maps.DeleteFunc(w.uiSessions, func(_ string, exp time.Time) bool { return now.After(exp) })
	id, exp := secrets.RandomString(24), now.Add(uiSessionTTL)
	w.uiSessions[id] = exp
	return id, exp
}

func (w *webState) validUISession(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	exp, ok := w.uiSessions[id]
	if ok && time.Now().After(exp) {
		delete(w.uiSessions, id)
		return false
	}
	return ok
}

// WebSessionRequest is the body of POST /v1/web/session.
type WebSessionRequest struct {
	Target        string `json:"target"`         // forgejo | ui
	Next          string `json:"next,omitempty"` // e.g. /bottown/demo; local paths only
	ResetPassword bool   `json:"reset_password"`
}

// WebSessionResponse is its result.
type WebSessionResponse struct {
	LoginPath   string `json:"login_path"`             // one-time, relative to the server URL
	NewPassword string `json:"new_password,omitempty"` // set when the password was reset
}

func (s *Server) postWebSession(w http.ResponseWriter, r *http.Request) {
	var req WebSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Next == "" {
		req.Next = "/"
	}
	// Only same-origin paths, so a login link cannot redirect elsewhere.
	if !strings.HasPrefix(req.Next, "/") || strings.HasPrefix(req.Next, "//") || strings.Contains(req.Next, "\\") {
		writeError(w, http.StatusBadRequest, "next must be a local path")
		return
	}
	var resp WebSessionResponse
	switch req.Target {
	case "forgejo":
		// Sign in now, so a stale password is reported to the CLI rather
		// than to the browser.
		_, pw, err := s.Rec.OwnerWebSession(r.Context(), s.ForgejoCookie, req.ResetPassword)
		if errors.Is(err, provision.ErrOwnerPassword) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		resp.NewPassword = pw
	case "ui":
	default:
		writeError(w, http.StatusBadRequest, `target must be "forgejo" or "ui"`)
		return
	}
	resp.LoginPath = "/v1/web/login/" + s.web.newTicket(req.Target, req.Next)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) webLogin(w http.ResponseWriter, r *http.Request) {
	t, ok := s.web.redeem(r.PathValue("ticket"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "login link expired or already used; run `bottown web` again")
		return
	}
	s.Log.Info("web login", "actor", s.Owner, "target", t.target)
	switch t.target {
	case "forgejo":
		s.redirectToForgejo(w, r, t.next)
	case "ui":
		id, exp := s.web.newUISession()
		http.SetCookie(w, &http.Cookie{
			Name: s.UICookie, Value: id, Path: "/", Expires: exp,
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, t.next, http.StatusSeeOther)
	}
}

// redirectToForgejo signs the owner in to Forgejo and hands the browser the
// session cookie.
func (s *Server) redirectToForgejo(w http.ResponseWriter, r *http.Request, next string) {
	c, _, err := s.Rec.OwnerWebSession(r.Context(), s.ForgejoCookie, false)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: c.Name, Value: c.Value, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.PublicForgejoURL+next, http.StatusSeeOther)
}

func (s *Server) uiAuthed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.UICookie)
		if err != nil || !s.web.validUISession(c.Value) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("Not signed in. Run: bottown -i " + s.Instance + " web ui\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) webForgejo(w http.ResponseWriter, r *http.Request) { s.redirectToForgejo(w, r, "/") }

// uiHome is a placeholder status page until the real web UI exists.
func (s *Server) uiHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = homeTmpl.Execute(w, map[string]any{"H": s.healthSnapshot(), "Org": s.desired().Org, "Owner": s.Owner})
}

var homeTmpl = template.Must(template.New("home").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>bottown {{.H.Instance}}</title>
<style>
:root{--bg:#fff;--fg:#1b1b1f;--muted:#666;--line:#e3e3e8;--accent:#2f6fde}
@media (prefers-color-scheme:dark){:root{--bg:#141418;--fg:#ececf1;--muted:#9a9aa6;--line:#2c2c34;--accent:#7aa5ff}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif}
main{max-width:720px;margin:0 auto;padding:32px 16px}
h1{font-size:22px;margin:0 0 4px} p.sub{color:var(--muted);margin:0 0 24px}
dl{display:grid;grid-template-columns:max-content 1fr;gap:6px 16px;margin:0 0 24px}
dt{color:var(--muted)} a{color:var(--accent)}
.btn{display:inline-block;padding:8px 14px;border:1px solid var(--line);border-radius:8px;text-decoration:none}
ul{padding-left:20px;color:var(--muted)}
</style></head><body><main>
<h1>bottown · {{.H.Instance}}</h1>
<p class="sub">Signed in as {{.Owner}}. The full web UI is not built yet; this page shows instance status.</p>
<dl>
<dt>bottown</dt><dd>{{.H.Version}}</dd>
<dt>Forgejo</dt><dd>{{.H.ForgejoVersion}} ({{.H.Forgejo}})</dd>
<dt>Provisioned</dt><dd>{{if .H.Reconciled}}yes{{else}}no{{end}}{{if .H.Error}} · {{.H.Error}}{{end}}</dd>
<dt>Org</dt><dd>{{.Org}}</dd>
</dl>
<p><a class="btn" href="/web/forgejo">Open Forgejo as {{.Owner}}</a></p>
{{if .H.Changes}}<h2 style="font-size:16px">Changes at last start</h2><ul>{{range .H.Changes}}<li>{{.}}</li>{{end}}</ul>{{end}}
</main></body></html>
`))
