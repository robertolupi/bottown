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

// A plan run is started like an implement run on its kickoff issue, may
// open its pull request, and may not file issues, through either the forge
// endpoints or the API proxy; the refusals are audited.
func TestPlanRun(t *testing.T) {
	var mu sync.Mutex
	var prBody map[string]any
	issuesCreated := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch p := r.Method + " " + r.URL.Path; p {
		case "GET /api/v1/repos/bottown/demo":
			_, _ = io.WriteString(w, `{"name":"demo","default_branch":"main"}`)
		case "GET /api/v1/repos/bottown/demo/issues/7":
			_, _ = io.WriteString(w, `{"number":7,"state":"open","title":"Kickoff"}`)
		case "GET /api/v1/repos/bottown/demo/issues/8":
			_, _ = io.WriteString(w, `{"number":8,"state":"open","pull_request":{"merged":false}}`)
		case "POST /api/v1/repos/bottown/demo/pulls":
			_ = json.NewDecoder(r.Body).Decode(&prBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"number":9,"html_url":"http://forge/bottown/demo/pulls/9"}`)
		case "POST /api/v1/repos/bottown/demo/issues":
			issuesCreated++
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"number":10}`)
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
			Actors: []config.Actor{{Name: "claude", Family: "anthropic", Harness: "claude"}},
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

	// A plan run takes an issue, not a pull request.
	if w := do(ownerKey, "POST", "/v1/runs", `{"kind":"plan","actor":"claude","repo":"demo","issue":8}`); w.Code != http.StatusBadRequest {
		t.Errorf("plan run on a PR = %d (%s), want 400", w.Code, w.Body.String())
	}
	w := do(ownerKey, "POST", "/v1/runs", `{"kind":"plan","actor":"claude","repo":"demo","issue":7}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /v1/runs plan = %d (%s), want 201", w.Code, w.Body.String())
	}
	var created RunResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Kind != policy.Plan || created.Issue != 7 || created.Branch != "issue/7" || created.Key == "" {
		t.Fatalf("plan run = %+v, want kind plan on issue/7 with a key", created.Run)
	}
	runKey := created.Key

	if w := do(runKey, "POST", "/v1/forge/pr", `{"title":"Plan the milestone","body":"the plan"}`); w.Code != http.StatusOK {
		t.Fatalf("pr create in a plan run = %d (%s), want 200", w.Code, w.Body.String())
	}
	mu.Lock()
	if prBody["head"] != "issue/7" || prBody["base"] != "main" || !strings.Contains(prBody["body"].(string), "bottown plan run "+created.ID) {
		t.Errorf("created PR = %+v, want issue/7 into main from the plan run", prBody)
	}
	mu.Unlock()

	if w := do(runKey, "POST", "/v1/forge/issue", `{"title":"a child issue"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "plan runs may not create issues") {
		t.Errorf("issue create in a plan run = %d (%s), want 403", w.Code, w.Body.String())
	}
	if w := do(runKey, "POST", "/forgejo/api/v1/repos/bottown/demo/issues", `{"title":"a child issue"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "role-plan-no-issues") {
		t.Errorf("proxied issue create in a plan run = %d (%s), want 403", w.Code, w.Body.String())
	}
	mu.Lock()
	if issuesCreated != 0 {
		t.Errorf("%d issues reached the forge, want none", issuesCreated)
	}
	mu.Unlock()

	w = do(ownerKey, "GET", "/v1/audit?run="+created.ID+"&decision=deny", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/audit = %d (%s)", w.Code, w.Body.String())
	}
	var denials []AuditEntry
	if err := json.Unmarshal(w.Body.Bytes(), &denials); err != nil {
		t.Fatal(err)
	}
	if len(denials) != 2 {
		t.Fatalf("plan run denials = %+v, want 2", denials)
	}
	if e := denials[0]; e.Op != "issue_create" || e.Detail != "role-plan-no-issues" || e.Repo != "bottown/demo" {
		t.Errorf("first denial = %+v, want issue_create role-plan-no-issues in bottown/demo", e)
	}
	if e := denials[1]; e.Op != "api" || e.Rule != "role-plan-no-issues" {
		t.Errorf("second denial = %+v, want the proxied issue create", e)
	}
}
