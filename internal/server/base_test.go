package server

import (
	"encoding/json"
	"fmt"
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

// An implement run's base is its issue's open PR's base; else, on the
// owner's milestone child, milestone/<slug> once that branch exists; else
// the default branch. The run records it, and its PR goes into it.
func TestRunBase(t *testing.T) {
	child := func(plan string) string {
		return fmt.Sprintf("Do it.\n\n<!-- bottown:milestone v=1 plan=%q key=\"k\" sha=\"0123456789abcdef\" -->", plan)
	}
	epic := "Epic.\n\n<!-- bottown:milestone v=1 plan=\"demo\" role=\"epic\" sha=\"0123456789abcdef\" -->"
	issues := map[int64]struct{ poster, body string }{
		1: {"owner", child("demo")}, // its open PR goes into another base
		2: {"owner", child("demo")}, // milestone/demo exists
		3: {"owner", child("gone")}, // milestone/gone does not
		4: {"owner", epic},          // an epic is no child
		5: {"agy", child("demo")},   // an actor's copied marker
		6: {"owner", "No marker."},
	}
	var mu sync.Mutex
	prBases := map[string]string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var n int64
		switch p := r.Method + " " + r.URL.Path; {
		case p == "GET /api/v1/repos/bottown/demo":
			_, _ = io.WriteString(w, `{"name":"demo","default_branch":"main"}`)
		case p == "GET /api/v1/repos/bottown/demo/pulls":
			_, _ = io.WriteString(w, `[{"number":11,"state":"open","head":{"ref":"issue/1"},"base":{"ref":"milestone/other"}},
				{"number":12,"state":"open","head":{"ref":"feature"},"base":{"ref":"milestone/demo"}}]`)
		case p == "GET /api/v1/repos/bottown/demo/branches/milestone/demo":
			_, _ = io.WriteString(w, `{"name":"milestone/demo","commit":{"id":"abc"}}`)
		case strings.HasPrefix(p, "GET /api/v1/repos/bottown/demo/branches/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"branch not found"}`)
		case func() bool {
			_, err := fmt.Sscanf(p, "GET /api/v1/repos/bottown/demo/issues/%d", &n)
			return err == nil
		}():
			is := issues[n]
			b, _ := json.Marshal(map[string]any{"number": n, "state": "open", "title": "An issue", "body": is.body, "user": map[string]string{"login": is.poster}})
			_, _ = w.Write(b)
		case p == "POST /api/v1/repos/bottown/demo/pulls":
			var in struct{ Head, Base string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			prBases[in.Head] = in.Base
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"number":20,"html_url":"http://forge/bottown/demo/pulls/20"}`)
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
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}}
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
			Org:    "bottown",
			Actors: []config.Actor{{Name: "claude", Family: "anthropic", Harness: "claude"}, {Name: "agy", Family: "google", Harness: "agy"}},
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

	for n, want := range map[int64]string{1: "milestone/other", 2: "milestone/demo", 3: "main", 4: "main", 5: "main", 6: "main"} {
		w := do(ownerKey, "POST", "/v1/runs", fmt.Sprintf(`{"kind":"implement","actor":"claude","repo":"demo","issue":%d}`, n))
		if w.Code != http.StatusCreated {
			t.Fatalf("#%d: POST /v1/runs = %d (%s)", n, w.Code, w.Body.String())
		}
		var created RunResponse
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if created.Base != want {
			t.Errorf("#%d: run base = %q, want %q", n, created.Base, want)
		}
		st, err := store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Runs[created.ID].Base; got != want {
			t.Errorf("#%d: recorded base = %q, want %q", n, got, want)
		}
		w = do(created.Key, "POST", "/v1/forge/pr", `{"title":"Do it"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("#%d: pr create = %d (%s)", n, w.Code, w.Body.String())
		}
		var out PRCreateOut
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		got := prBases[fmt.Sprintf("issue/%d", n)]
		mu.Unlock()
		if got != want || out.Base != want {
			t.Errorf("#%d: PR opened into %q (reported %q), want %q", n, got, out.Base, want)
		}
	}
}
