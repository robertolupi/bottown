package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// A critical discuss run comes from another model family than its issue's
// author: an actor of the author's family needs allow_same_family, which
// the run records. Its comments are signed with its family, and with the
// override when it was needed. Only discuss runs can be critical.
func TestCriticalDiscussRun(t *testing.T) {
	var mu sync.Mutex
	var posted []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch p := r.Method + " " + r.URL.Path; p {
		case "GET /api/v1/repos/bottown/demo":
			_, _ = io.WriteString(w, `{"name":"demo","default_branch":"main"}`)
		case "GET /api/v1/repos/bottown/demo/branches/main":
			_, _ = io.WriteString(w, `{"name":"main","commit":{"id":"abc123"}}`)
		case "GET /api/v1/repos/bottown/demo/issues/7":
			_, _ = io.WriteString(w, `{"number":7,"state":"open","title":"Design","user":{"login":"claude"}}`)
		case "GET /api/v1/repos/bottown/demo/issues/8":
			_, _ = io.WriteString(w, `{"number":8,"state":"open","title":"Owner's","user":{"login":"owner"}}`)
		case "POST /api/v1/repos/bottown/demo/issues/7/comments":
			var in struct{ Body string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			mu.Lock()
			posted = append(posted, in.Body)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":1,"body":"ok","user":{"login":"x"}}`)
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
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "haiku": {Token: "tok_haiku"}, "codex": {Token: "tok_codex"}}
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
				{Name: "haiku", Family: "anthropic", Harness: "claude"},
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

	if _, w := create(`{"kind":"verify","actor":"codex","repo":"demo","pr":7,"critical":true}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "only discuss runs can be critical") {
		t.Errorf("critical verify = %d (%s), want 400", w.Code, w.Body.String())
	}
	// claude wrote #7: haiku is of its family, and needs the override.
	if _, w := create(`{"kind":"discuss","actor":"haiku","repo":"demo","issue":7,"critical":true}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "#7's author claude are both anthropic") {
		t.Errorf("same-family critical discuss = %d (%s), want 409 naming the author", w.Code, w.Body.String())
	}
	// A plain discuss has no family rule.
	if plain, w := create(`{"kind":"discuss","actor":"haiku","repo":"demo","issue":7}`); w.Code != http.StatusCreated || plain.Critical || plain.AllowSameFamily {
		t.Errorf("plain same-family discuss = %d (%s), want 201", w.Code, w.Body.String())
	}
	// The owner wrote #8, and is no actor: no family to differ from.
	if own, w := create(`{"kind":"discuss","actor":"claude","repo":"demo","issue":8,"critical":true}`); w.Code != http.StatusCreated || !own.Critical || own.AllowSameFamily || own.Author != "owner" {
		t.Errorf("critical discuss of the owner's issue = %d (%s) %+v", w.Code, w.Body.String(), own.Run)
	}

	cross, w := create(`{"kind":"discuss","actor":"codex","repo":"demo","issue":7,"critical":true}`)
	if w.Code != http.StatusCreated || !cross.Critical || cross.AllowSameFamily || cross.Author != "claude" || cross.Kind != policy.Discuss {
		t.Fatalf("cross-family critical discuss = %d (%s) %+v", w.Code, w.Body.String(), cross.Run)
	}
	same, w := create(`{"kind":"discuss","actor":"haiku","repo":"demo","issue":7,"critical":true,"allow_same_family":true}`)
	if w.Code != http.StatusCreated || !same.Critical || !same.AllowSameFamily {
		t.Fatalf("same-family critical discuss with the override = %d (%s), want 201 recording it", w.Code, w.Body.String())
	}

	if w := do(cross.Key, "POST", "/v1/forge/comment", `{"body":"Do not build it."}`); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("critical comment = %d (%s)", w.Code, w.Body.String())
	}
	if w := do(same.Key, "POST", "/v1/forge/comment", `{"body":"Rework it."}`); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("same-family critical comment = %d (%s)", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(posted) != 2 {
		t.Fatalf("posted %d comments, want 2: %q", len(posted), posted)
	}
	if want := "Do not build it.\n\n_bottown critical discuss run " + cross.ID + " (codex, openai)_"; posted[0] != want {
		t.Errorf("critical comment = %q, want %q", posted[0], want)
	}
	if !strings.HasPrefix(posted[1], "> **Same-family critique** (anthropic critiquing claude's issue), allowed by owner override.\n\nRework it.") ||
		!strings.HasSuffix(posted[1], "(haiku, anthropic)_") {
		t.Errorf("same-family critical comment = %q", posted[1])
	}
}
