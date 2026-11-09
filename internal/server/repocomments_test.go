package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// GET /v1/forge/comments reads the repository's comments in one list,
// since a time, and keeps the issues' comments with their issue numbers.
func TestRepoComments(t *testing.T) {
	var queries []url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/bottown/demo/issues/comments" {
			http.NotFound(w, r)
			return
		}
		queries = append(queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":30,"body":"stop","user":{"login":"owner"},"created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-01T11:00:00Z","issue_url":"http://f/bottown/demo/issues/4","pull_request_url":""},
			{"id":31,"body":"review","user":{"login":"claude"},"created_at":"2026-11-01T10:05:00Z","updated_at":"2026-11-01T10:05:00Z","issue_url":"","pull_request_url":"http://f/bottown/demo/pulls/5"}]`))
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	runKey, err := store.NewRun(secrets.Run{
		ID: "r-1", Kind: policy.Implement, Actor: "claude", Org: "bottown", Repo: "demo", Issue: 4,
		Created: now, Expires: now.Add(time.Hour),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Owner: "owner", ForgejoURL: u, Store: store,
		Rec:    &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}},
		Policy: policy.Default("owner"), Log: slog.Default(),
	}
	srv.health.Reconciled = true
	h := srv.Handler()
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+runKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	w := get("/v1/forge/comments?since=2026-11-01T09:00:00Z")
	var out []RepoCommentOut
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(out) != 1 || out[0].Issue != 4 || out[0].ID != 30 || out[0].Author != "owner" || out[0].Body != "stop" ||
		!out[0].Created.Equal(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) || !out[0].Updated.Equal(time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("comments = %+v", out)
	}
	if len(queries) != 2 || queries[0].Get("since") != "2026-11-01T09:00:00Z" {
		t.Errorf("queries = %v", queries)
	}

	if w := get("/v1/forge/comments?since=yesterday"); w.Code != http.StatusBadRequest {
		t.Errorf("a bad since: %d %s", w.Code, w.Body)
	}
	// A run reads only its own repository.
	if w := get("/v1/forge/comments?repo=other"); w.Code == http.StatusOK {
		t.Errorf("another repository: %d %s", w.Code, w.Body)
	}
}
