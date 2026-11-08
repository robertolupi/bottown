package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

const mergeHead = "1111111111111111111111111111111111111111"

// fakeMergeForge is the Forgejo a landing merge talks to: PR !20 from
// issue/7 in a repository whose default branch is main, and who did each
// write.
type fakeMergeForge struct {
	mu       sync.Mutex
	base     string
	head     string
	headRef  string // the PR's head branch; issue/7 when empty
	merged   bool
	closed   bool // issue #7
	reviews  []map[string]any
	timeline []map[string]any
	// mergeStatus, when set, is the status the merge answers with, empty,
	// as Forgejo 15 does for a fast-forward over a moved base.
	mergeStatus int
	writes      []string // "<token> <method> <path>"
	body        string   // the comment's body
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
	case "GET /api/v1/repos/bottown/demo":
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "demo", "default_branch": "main"})
	case "GET /api/v1/repos/bottown/demo/pulls/20":
		state := "open"
		if f.merged {
			state = "closed"
		}
		ref := f.headRef
		if ref == "" {
			ref = "issue/7"
		}
		pr := map[string]any{
			"number": 20, "title": "child", "state": state, "merged": f.merged,
			"user": map[string]string{"login": "claude"},
			"head": map[string]string{"ref": ref, "sha": f.head},
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
	case "GET /api/v1/repos/bottown/demo/issues/20/timeline":
		if r.URL.Query().Get("page") > "1" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_ = json.NewEncoder(w).Encode(f.timeline)
	case "POST /api/v1/repos/bottown/demo/pulls/20/reviews/5/undismissals":
		for _, rv := range f.reviews {
			if rv["id"] == 5 {
				rv["dismissed"] = false
			}
		}
		_, _ = w.Write([]byte("{}"))
	case "POST /api/v1/repos/bottown/demo/pulls/20/merge":
		if f.mergeStatus != 0 {
			w.WriteHeader(f.mergeStatus)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["Do"] != "fast-forward-only" || in["head_commit_id"] != f.head {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"bad merge request"}`))
			return
		}
		// Forgejo's protection counts undismissed official approvals only.
		approved := false
		for _, rv := range f.reviews {
			if rv["state"] == "APPROVED" && rv["dismissed"] != true {
				approved = true
			}
		}
		if !approved {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message":"not enough approvals"}`))
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
	audit, err := OpenAudit(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		Owner: "owner",
		Store: store,
		Audit: audit,
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

// landAudit is the decisions audited as land, in order.
func landAudit(s *Server) []string {
	var out []string
	for _, e := range s.Audit.Query(AuditFilter{}) {
		if e.Op == "land" {
			out = append(out, e.Decision)
		}
	}
	return out
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
		{"base neither default nor milestone", "release", mergeHead, []map[string]any{approval("codex", mergeHead)}, "base is release, not main or a milestone/* branch"},
		{"sha mismatch", "milestone/m", "2222222222222222222222222222222222222222", []map[string]any{approval("codex", mergeHead)}, "head is 1111111, not 2222222"},
		{"not approved", "milestone/m", mergeHead, nil, "not approved at 1111111: needs review"},
		{"not approved on main", "main", mergeHead, nil, "not approved at 1111111: needs review"},
		{"approved at an older head", "milestone/m", mergeHead, []map[string]any{approval("codex", "3333333333333333333333333333333333333333")}, "not approved"},
		{"same-family approval only", "milestone/m", mergeHead, []map[string]any{approval("opus", mergeHead)}, "not approved at 1111111: needs review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMergeForge{base: tc.base, head: mergeHead, reviews: tc.reviews}
			s := mergeServer(t, f)
			_, err := s.opLand(t.Context(), owner, LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: tc.sha})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if errStatus(err) != http.StatusConflict || errCode(err) != "" {
				t.Errorf("status = %d, code %q; want 409 with no code", errStatus(err), errCode(err))
			}
			if len(f.writes) != 0 {
				t.Errorf("writes = %v, want none", f.writes)
			}
			if got := landAudit(s); strings.Join(got, ",") != "deny" {
				t.Errorf("land audit = %v, want one deny", got)
			}
		})
	}
}

func TestMilestoneMergeAsManager(t *testing.T) {
	f := &fakeMergeForge{base: "milestone/m", head: mergeHead, reviews: []map[string]any{approval("codex", mergeHead)}}
	s := mergeServer(t, f)
	in := LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead}
	out, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, in)
	if err != nil {
		t.Fatal(err)
	}
	want := LandOut{Number: 20, Issue: 7, Base: "milestone/m", HeadSHA: mergeHead, MergeCommit: "mmmmmmm", IssueClosed: true}
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
	if got := landAudit(s); strings.Join(got, ",") != "allow" {
		t.Errorf("land audit = %v, want one allow", got)
	}

	// Again: reported as merged, with nothing more written.
	f.writes = nil
	again, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, in)
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

// On the default branch the merge closes nothing itself, even from an
// issue/<n> head: Forgejo applies the PR's closing keywords.
func TestLandDefaultBranchClosesNothing(t *testing.T) {
	f := &fakeMergeForge{base: "main", head: mergeHead, reviews: []map[string]any{approval("codex", mergeHead)}}
	s := mergeServer(t, f)
	in := LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead}
	out, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, in)
	if err != nil {
		t.Fatal(err)
	}
	want := LandOut{Number: 20, Base: "main", HeadSHA: mergeHead, MergeCommit: "mmmmmmm"}
	if out != want {
		t.Errorf("out = %+v, want %+v", out, want)
	}
	if got := strings.Join(f.writes, "\n"); got != "tok_manager POST /api/v1/repos/bottown/demo/pulls/20/merge" {
		t.Errorf("writes:\n%s\nwant only the merge", got)
	}
	if f.closed {
		t.Error("issue #7 was closed")
	}
	f.writes = nil
	again, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyMerged || again.Issue != 0 || len(f.writes) != 0 {
		t.Errorf("again = %+v, writes %v; want already merged, nothing written", again, f.writes)
	}
}

// A milestone merge from a branch that is not issue/<n>, n positive, closes
// nothing, and its repeat reports it merged.
func TestLandMilestoneNonChildClosesNothing(t *testing.T) {
	for _, ref := range []string{"feature/x", "issue/-7", "issue/0", "issue/99999999999999999999", "issue/7a"} {
		t.Run(ref, func(t *testing.T) {
			f := &fakeMergeForge{base: "milestone/m", head: mergeHead, headRef: ref, reviews: []map[string]any{approval("codex", mergeHead)}}
			s := mergeServer(t, f)
			in := LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead}
			out, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, in)
			if err != nil {
				t.Fatal(err)
			}
			want := LandOut{Number: 20, Base: "milestone/m", HeadSHA: mergeHead, MergeCommit: "mmmmmmm"}
			if out != want {
				t.Errorf("out = %+v, want %+v", out, want)
			}
			if got := strings.Join(f.writes, "\n"); got != "tok_manager POST /api/v1/repos/bottown/demo/pulls/20/merge" {
				t.Errorf("writes:\n%s\nwant only the merge", got)
			}
			f.writes = nil
			again, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, in)
			if err != nil {
				t.Fatal(err)
			}
			if !again.AlreadyMerged || again.Issue != 0 || len(f.writes) != 0 {
				t.Errorf("again = %+v, writes %v; want already merged, nothing written", again, f.writes)
			}
		})
	}
}

// An approval at the head that Forgejo dismissed automatically is
// undismissed before the merge, so its protection counts it.
func TestLandUndismissesCarriedApproval(t *testing.T) {
	dismissed := approval("codex", mergeHead)
	dismissed["id"], dismissed["dismissed"] = 5, true
	f := &fakeMergeForge{base: "main", head: mergeHead, reviews: []map[string]any{dismissed},
		timeline: []map[string]any{{"id": 1, "type": "dismiss_review", "review_id": 5, "body": forgejo.AutoDismissMessage}}}
	s := mergeServer(t, f)
	if _, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead}); err != nil {
		t.Fatal(err)
	}
	wantWrites := []string{
		"tok_owner POST /api/v1/repos/bottown/demo/pulls/20/reviews/5/undismissals",
		"tok_manager POST /api/v1/repos/bottown/demo/pulls/20/merge",
	}
	if strings.Join(f.writes, "\n") != strings.Join(wantWrites, "\n") {
		t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(f.writes, "\n"), strings.Join(wantWrites, "\n"))
	}
}

// landGitRoot puts a repository under a GitRoot as bottown/demo.git: main,
// and the PR's head one commit on top of it. moved commits on main after
// the head, so main's tip is no longer an ancestor of it.
func landGitRoot(t *testing.T, moved bool) (root, head string) {
	t.Helper()
	r := newGitRepo(t)
	r.run("init", "-q", "-b", "main", r.dir)
	r.write("a.txt", "base\n")
	r.commit("base")
	r.run("checkout", "-q", "-b", "issue/7")
	r.write("a.txt", "pr\n")
	r.commit("pr")
	head = r.run("rev-parse", "HEAD")
	r.run("checkout", "-q", "main")
	if moved {
		r.write("b.txt", "main\n")
		r.commit("main moves")
	}
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bottown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.gitDir(), filepath.Join(root, "bottown", "demo.git")); err != nil {
		t.Fatal(err)
	}
	return root, head
}

// Forgejo 15 refuses a fast-forward over a moved base with an empty 500;
// re-reading the base tells it from any other failure.
func TestLandBaseMoved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		moved  bool
		status int
	}{
		{"500 over a moved base", true, http.StatusInternalServerError},
		{"409 over a moved base", true, http.StatusConflict},
		{"500, base still contained", false, http.StatusInternalServerError},
		{"409, base still contained", false, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, head := landGitRoot(t, tc.moved)
			f := &fakeMergeForge{base: "main", head: head, reviews: []map[string]any{approval("codex", head)}, mergeStatus: tc.status}
			s := mergeServer(t, f)
			s.GitRoot = root
			_, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: head})
			if err == nil {
				t.Fatal("landed")
			}
			if got := errCode(err) == ErrorCodeBaseMoved; got != tc.moved {
				t.Errorf("err = %v (code %q), base_moved = %v, want %v", err, errCode(err), got, tc.moved)
			}
			if tc.moved {
				if errStatus(err) != http.StatusConflict || !strings.Contains(err.Error(), "main moved to") {
					t.Errorf("err = %v (status %d), want 409 naming the new tip", err, errStatus(err))
				}
			} else if tc.status == http.StatusInternalServerError && errStatus(err) == http.StatusConflict {
				t.Errorf("err = %v: an unexplained 500 is reported as a refusal", err)
			}
			if got := landAudit(s); strings.Join(got, ",") != "deny" {
				t.Errorf("land audit = %v, want one deny", got)
			}
			if f.merged {
				t.Error("PR marked merged")
			}
		})
	}

	// The answer carries the code clients branch on.
	root, head := landGitRoot(t, true)
	f := &fakeMergeForge{base: "main", head: head, reviews: []map[string]any{approval("codex", head)}, mergeStatus: http.StatusInternalServerError}
	s := mergeServer(t, f)
	s.GitRoot = root
	_, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: head})
	w := httptest.NewRecorder()
	writeOp(w, nil, err)
	var body map[string]string
	_ = json.NewDecoder(w.Body).Decode(&body)
	if w.Code != http.StatusConflict || body["code"] != ErrorCodeBaseMoved {
		t.Errorf("answer = %d %v, want 409 with code %s", w.Code, body, ErrorCodeBaseMoved)
	}
}

// A merge whose close failed is finished by the next call.
func TestMilestoneMergeFinishesClose(t *testing.T) {
	f := &fakeMergeForge{base: "milestone/m", head: mergeHead, merged: true}
	s := mergeServer(t, f)
	out, err := s.opLand(t.Context(), secrets.Principal{Actor: "owner"}, LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead})
	if err != nil {
		t.Fatal(err)
	}
	if !out.AlreadyMerged || !out.IssueClosed || !f.closed {
		t.Errorf("out = %+v, closed = %v; want the child closed", out, f.closed)
	}
}

func TestMilestoneMergeActorsRefused(t *testing.T) {
	for _, base := range []string{"milestone/m", "main"} {
		t.Run(base, func(t *testing.T) {
			f := &fakeMergeForge{base: base, head: mergeHead, reviews: []map[string]any{approval("codex", mergeHead)}}
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
				if _, err := s.opLand(t.Context(), p, LandIn{Target: Target{Repo: "demo", Number: 20}, HeadSHA: mergeHead}); errStatus(err) != http.StatusForbidden {
					t.Errorf("%+v: err = %v, want 403", p, err)
				}
			}
			if len(f.writes) != 0 {
				t.Errorf("writes = %v, want none", f.writes)
			}
		})
	}
}
