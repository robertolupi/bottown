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

// An actor's batch runs on different targets start concurrently; a second
// live run on the same issue or pull request head is refused, and so is a
// run that serialises its actor while the actor has a live batch run.
func TestRunConcurrency(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch p := r.Method + " " + r.URL.Path; p {
		case "GET /api/v1/repos/bottown/demo":
			_, _ = io.WriteString(w, `{"name":"demo","default_branch":"main"}`)
		case "GET /api/v1/repos/bottown/demo/issues/7", "GET /api/v1/repos/bottown/demo/issues/8", "GET /api/v1/repos/bottown/demo/issues/9":
			_, _ = io.WriteString(w, `{"number":7,"state":"open","title":"An issue"}`)
		case "GET /api/v1/repos/bottown/demo/pulls/12":
			_, _ = io.WriteString(w, `{"number":12,"state":"open","poster":{"login":"claude"},"head":{"ref":"issue/7","sha":"h1"}}`)
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
	ownerKey, err := store.NewAPIKey("owner", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Owner: "owner", ForgejoURL: u, Store: store,
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

	for _, c := range []struct {
		name, body string
		code       int
		msg        string
		errCode    string // the error code clients branch on, if any
	}{
		{"first implement", `{"kind":"implement","actor":"claude","repo":"demo","issue":7}`, http.StatusCreated, "", ""},
		{"same actor, another issue", `{"kind":"implement","actor":"claude","repo":"demo","issue":8}`, http.StatusCreated, "", ""},
		{"another actor, same issue", `{"kind":"implement","actor":"codex","repo":"demo","issue":7}`, http.StatusConflict, "same target", ""},
		{"serialised actor", `{"kind":"implement","actor":"claude","repo":"demo","issue":9,"serialize_actor":true}`, http.StatusConflict, ActorBusyMessage, ErrorCodeActorBusy},
		{"verify", `{"kind":"verify","actor":"codex","repo":"demo","pr":12}`, http.StatusCreated, "", ""},
		{"second verify at the head", `{"kind":"verify","actor":"codex","repo":"demo","pr":12}`, http.StatusConflict, "same target", ""},
		{"implement while verifying", `{"kind":"implement","actor":"codex","repo":"demo","issue":9}`, http.StatusCreated, "", ""},
	} {
		w := do(c.body)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.msg) {
			t.Errorf("%s = %d (%s), want %d %q", c.name, w.Code, w.Body.String(), c.code, c.msg)
		}
		if w.Code >= 300 {
			var e struct{ Code string }
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Code != c.errCode {
				t.Errorf("%s: error code = %q (%v), want %q", c.name, e.Code, err, c.errCode)
			}
		}
	}
}
