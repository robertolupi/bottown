package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

const mergeHead = "1111111111111111111111111111111111111111"

// fakeMergeForge is the Forgejo a milestone merge talks to: PR !20 from
// issue/7, and who did each write.
type fakeMergeForge struct {
	mu      sync.Mutex
	base    string
	head    string
	merged  bool
	closed  bool // issue #7
	reviews []map[string]any
	writes  []string // "<token> <method> <path>"
	body    string   // the comment's body
}

func (f *fakeMergeForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, token+" "+r.Method+" "+r.URL.Path)
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/v1/repos/bottown/demo/pulls/20":
		state := "open"
		if f.merged {
			state = "closed"
		}
		pr := map[string]any{
			"number": 20, "title": "child", "state": state, "merged": f.merged,
			"user": map[string]string{"login": "claude"},
			"head": map[string]string{"ref": "issue/7", "sha": f.head},
			"base": map[string]string{"ref": f.base},
		}
		if f.merged {
			pr["merge_commit_sha"] = "mmmmmmm"
		}
		_ = json.NewEncoder(w).Encode(pr)
	case "GET /api/v1/repos/bottown/demo/pulls/20/reviews":
		if r.URL.Query().Get("page") > "1" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_ = json.NewEncoder(w).Encode(f.reviews)
	case "POST /api/v1/repos/bottown/demo/pulls/20/merge":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["Do"] != "merge" || in["head_commit_id"] != f.head {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"bad merge request"}`))
			return
		}
		f.merged = true
	case "GET /api/v1/repos/bottown/demo/issues/7":
		state := "open"
		if f.closed {
			state = "closed"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": state})
	case "PATCH /api/v1/repos/bottown/demo/issues/7":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.closed = in["state"] == "closed"
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": in["state"]})
	case "POST /api/v1/repos/bottown/demo/issues/7/comments":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.body = in["body"]
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "body": in["body"]})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	}
}

func approval(reviewer, commit string) map[string]any {
	return map[string]any{"id": 1, "user": map[string]string{"login": reviewer}, "state": "APPROVED", "commit_id": commit, "official": true}
}

func mergeServer(t *testing.T, f *fakeMergeForge) *Server {
	t.Helper()
	upstream := httptest.NewServer(f)
	t.Cleanup(upstream.Close)
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{
			"owner": {Token: "tok_owner"}, "manager": {Token: "tok_manager"},
			"claude": {Token: "tok_claude"}, "codex": {Token: "tok_codex"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{
		Owner: "owner",
		Store: store,
		Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{
			Org:     "bottown",
			Manager: config.Account{Name: "manager"},
			Actors: []config.Actor{
				{Name: "claude", Family: "anthropic"}, {Name: "opus", Family: "anthropic"}, {Name: "codex", Family: "openai"},
			},
		}},
		Policy: policy.Default("owner"),
		Log:    slog.Default(),
	}
}

func TestMilestoneMergeRefusals(t *testing.T) {
	owner := secrets.Principal{Actor: "owner"}
	for _, tc := range []struct {
		name    string
		base    string
		sha     string
		reviews []map[string]any
		want    string
	}{
		{"base not milestone", "main", mergeHead, []map[string]any{approval("codex", mergeHead)}, "not a milestone/* branch"},
		{"sha mismatch", "milestone/m", "2222222222222222222222222222222222222222", []map[string]any{approval("codex", mergeHead)}, "head is 1111111, not 2222222"},
		{"not approved", "milestone/m", mergeHead, nil, "not approved at 1111111: needs review"},
		{"approved at an older head", "milestone/m", mergeHead, []map[string]any{approval("codex", "3333333333333333333333333333333333333333")}, "not approved"},
		{"same-family approval only", "milestone/m", mergeHead, []map[string]any{approval("opus", mergeHead)}, "not approved at 1111111: needs review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMergeForge{base: tc.base, head: mergeHead, reviews: tc.reviews}
			s := mergeServer(t, f)
			_, err := s.opMilestoneMerge(t.Context(), owner, MilestoneMergeIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: tc.sha})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if errStatus(err) != http.StatusConflict {
				t.Errorf("status = %d, want 409", errStatus(err))
			}
			if len(f.writes) != 0 {
				t.Errorf("writes = %v, want none", f.writes)
			}
		})
	}
}

func TestMilestoneMergeAsManager(t *testing.T) {
	f := &fakeMergeForge{base: "milestone/m", head: mergeHead, reviews: []map[string]any{approval("codex", mergeHead)}}
	s := mergeServer(t, f)
	in := MilestoneMergeIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead}
	out, err := s.opMilestoneMerge(t.Context(), secrets.Principal{Actor: "owner"}, in)
	if err != nil {
		t.Fatal(err)
	}
	want := MilestoneMergeOut{Number: 20, Issue: 7, Base: "milestone/m", HeadSHA: mergeHead, MergeCommit: "mmmmmmm", IssueClosed: true}
	if out != want {
		t.Errorf("out = %+v, want %+v", out, want)
	}
	wantWrites := []string{
		"tok_manager POST /api/v1/repos/bottown/demo/pulls/20/merge",
		"tok_manager PATCH /api/v1/repos/bottown/demo/issues/7",
		"tok_manager POST /api/v1/repos/bottown/demo/issues/7/comments",
	}
	if strings.Join(f.writes, "\n") != strings.Join(wantWrites, "\n") {
		t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(f.writes, "\n"), strings.Join(wantWrites, "\n"))
	}
	if !strings.Contains(f.body, "!20") || !strings.Contains(f.body, "milestone/m") || !strings.Contains(f.body, mergeHead) {
		t.Errorf("comment = %q, want the PR, base and head SHA", f.body)
	}

	// Again: reported as merged, with nothing more written.
	f.writes = nil
	again, err := s.opMilestoneMerge(t.Context(), secrets.Principal{Actor: "owner"}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyMerged || again.IssueClosed || again.MergeCommit != "mmmmmmm" {
		t.Errorf("again = %+v, want already merged, issue left alone", again)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
}

// A merge whose close failed is finished by the next call.
func TestMilestoneMergeFinishesClose(t *testing.T) {
	f := &fakeMergeForge{base: "milestone/m", head: mergeHead, merged: true}
	s := mergeServer(t, f)
	out, err := s.opMilestoneMerge(t.Context(), secrets.Principal{Actor: "owner"}, MilestoneMergeIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead})
	if err != nil {
		t.Fatal(err)
	}
	if !out.AlreadyMerged || !out.IssueClosed || !f.closed {
		t.Errorf("out = %+v, closed = %v; want the child closed", out, f.closed)
	}
}

func TestMilestoneMergeActorsRefused(t *testing.T) {
	f := &fakeMergeForge{base: "milestone/m", head: mergeHead, reviews: []map[string]any{approval("codex", mergeHead)}}
	s := mergeServer(t, f)
	s.health.Reconciled = true
	now := time.Now()
	keys := map[string]string{}
	for i, kind := range []string{policy.Implement, policy.Verify, policy.Plan, "discuss"} {
		key, err := s.Store.NewRun(secrets.Run{
			ID: "r-" + kind, Kind: kind, Actor: "claude", Org: "bottown", Repo: "demo", Issue: int64(7 + i), PR: int64(20 + i),
			Created: now, Expires: now.Add(time.Hour),
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		keys[kind] = key
	}
	actorKey, err := s.Store.NewAPIKey("claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	keys["actor key"] = actorKey
	h := s.Handler()
	for name, key := range keys {
		req := httptest.NewRequest(http.MethodPost, "/v1/forge/pr/merge", strings.NewReader(`{"repo":"demo","number":20,"head_sha":"`+mergeHead+`"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: POST /v1/forge/pr/merge = %d (%s), want 403", name, w.Code, w.Body.String())
		}
	}
	// The operation refuses them too, whatever the route.
	for _, p := range []secrets.Principal{{Actor: "claude"}, {Actor: "owner", Run: &secrets.Run{Kind: policy.Implement}}} {
		if _, err := s.opMilestoneMerge(t.Context(), p, MilestoneMergeIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead}); errStatus(err) != http.StatusForbidden {
			t.Errorf("%+v: err = %v, want 403", p, err)
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
}
