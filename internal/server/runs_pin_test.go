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

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// A verify run that names the head it expects is pinned exactly there, or
// refused if the PR has moved: bottown flow's passed checks reach only a
// verifier of the commit they ran at.
func TestVerifyRunExpectedHead(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch p := r.Method + " " + r.URL.Path; p {
		case "GET /api/v1/repos/bottown/demo":
			_, _ = io.WriteString(w, `{"name":"demo","default_branch":"main"}`)
		case "GET /api/v1/repos/bottown/demo/pulls/12":
			_, _ = io.WriteString(w, `{"number":12,"state":"open","poster":{"login":"claude"},"head":{"ref":"issue/9","sha":"h2"}}`)
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
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "codex": {Token: "tok_codex"}}
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
	do := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/runs", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+ownerKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	w := do(`{"kind":"verify","actor":"codex","repo":"demo","pr":12,"sha":"h1"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), HeadMovedMessage) || !strings.Contains(w.Body.String(), "h2") {
		t.Fatalf("verify run at a moved head = %d (%s), want 409 naming the head", w.Code, w.Body.String())
	}
	w = do(`{"kind":"verify","actor":"codex","repo":"demo","pr":12,"sha":"h2"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("verify run at the head = %d (%s), want 201", w.Code, w.Body.String())
	}
	var created RunResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.SHA != "h2" || created.PR != 12 {
		t.Fatalf("verify run = %+v, want pinned at h2", created.Run)
	}
}
