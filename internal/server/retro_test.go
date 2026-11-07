package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// A retro run is started like a discuss run, on any issue, open or
// closed, at the default branch's head; an actor of an implementer's
// family needs allow_same_family, which the run records. It may comment
// but not push or open pull requests.
func TestRetroRun(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch p := r.Method + " " + r.URL.Path; p {
		case "GET /api/v1/repos/bottown/demo":
			_, _ = io.WriteString(w, `{"name":"demo","default_branch":"main"}`)
		case "GET /api/v1/repos/bottown/demo/branches/main":
			_, _ = io.WriteString(w, `{"name":"main","commit":{"id":"abc123"}}`)
		case "GET /api/v1/repos/bottown/demo/issues/7", "GET /api/v1/repos/bottown/demo/issues/8":
			_, _ = io.WriteString(w, `{"number":7,"state":"closed","title":"Done"}`)
		default:
			t.Errorf("unexpected upstream request %s", p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := secrets.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UTC()
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "codex": {Token: "tok_codex"}}
		st.Runs = map[string]secrets.Run{
			// Issue 7 was implemented by claude; codex verified its PR.
			"r-impl": {ID: "r-impl", Kind: policy.Implement, Actor: "claude", Family: "anthropic", Org: "bottown", Repo: "demo", Issue: 7, Branch: "issue/7", Ended: &ended},
			"r-ver":  {ID: "r-ver", Kind: policy.Verify, Actor: "codex", Family: "openai", Org: "bottown", Repo: "demo", PR: 9, Branch: "issue/7", Ended: &ended},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ownerKey, err := store.NewAPIKey("owner", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Owner: "owner", ForgejoURL: u, Store: store, Audit: audit,
		Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{
			Org: "bottown",
			Actors: []config.Actor{
				{Name: "claude", Family: "anthropic", Harness: "claude"},
				{Name: "codex", Family: "openai", Harness: "codex"},
			},
		}},
		Policy: policy.Default("owner"), Log: slog.Default(),
	}
	srv.health.Reconciled = true
	h := srv.Handler()
	do := func(key, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	create := func(body string) (RunResponse, *httptest.ResponseRecorder) {
		t.Helper()
		w := do(ownerKey, "POST", "/v1/runs", body)
		var created RunResponse
		if w.Code == http.StatusCreated {
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
		}
		return created, w
	}

	if _, w := create(`{"kind":"retro","actor":"codex","repo":"demo"}`); w.Code != http.StatusBadRequest {
		t.Errorf("retro run without an issue = %d (%s), want 400", w.Code, w.Body.String())
	}
	// claude implemented #7: its retro of #7 needs the override.
	if _, w := create(`{"kind":"retro","actor":"claude","repo":"demo","issue":7}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "implementer claude of #7") {
		t.Errorf("same-family retro = %d (%s), want 409 naming the implementer", w.Code, w.Body.String())
	}
	same, w := create(`{"kind":"retro","actor":"claude","repo":"demo","issue":7,"allow_same_family":true}`)
	if w.Code != http.StatusCreated || !same.AllowSameFamily {
		t.Errorf("same-family retro with the override = %d (%s), want 201 recording it", w.Code, w.Body.String())
	}
	// claude implemented nothing on #8.
	if other, w := create(`{"kind":"retro","actor":"claude","repo":"demo","issue":8}`); w.Code != http.StatusCreated || other.AllowSameFamily {
		t.Errorf("retro of another issue = %d (%s), want 201 without the override", w.Code, w.Body.String())
	}

	created, w := create(`{"kind":"retro","actor":"codex","repo":"demo","issue":7}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /v1/runs retro = %d (%s), want 201", w.Code, w.Body.String())
	}
	if created.Kind != policy.Retro || created.Issue != 7 || created.SHA != "abc123" || created.Branch != "" || created.AllowSameFamily {
		t.Fatalf("retro run = %+v, want kind retro on #7 at main's head", created.Run)
	}
	if w := do(created.Key, "POST", "/v1/forge/pr", `{"title":"x","body":"y"}`); w.Code != http.StatusForbidden {
		t.Errorf("pr create in a retro run = %d (%s), want 403", w.Code, w.Body.String())
	}
	if w := do(created.Key, "POST", "/forgejo/api/v1/repos/bottown/demo/pulls", `{}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "role-retro") {
		t.Errorf("proxied PR create in a retro run = %d (%s), want 403 role-retro", w.Code, w.Body.String())
	}
}
