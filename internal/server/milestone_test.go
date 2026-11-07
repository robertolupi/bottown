package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// milestoneForge is a fake Forgejo for the milestone operations: its
// branches are the refs of a real bare repository, which the merge reads.
type milestoneForge struct {
	t      *testing.T
	gitDir string
	work   string

	mu     sync.Mutex
	issues map[int64]map[string]any
	deps   map[int64][]int64
	prs    []map[string]any
	writes []string // "METHOD path by <token>"
}

func (f *milestoneForge) git(dir string, args ...string) string {
	f.t.Helper()
	if dir == f.gitDir {
		args = append([]string{"--git-dir", f.gitDir}, args...)
		dir = ""
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit commits file on branch in the work tree and pushes it.
func (f *milestoneForge) commit(branch, file, content string) string {
	f.t.Helper()
	f.git(f.work, "checkout", "-q", branch)
	if err := os.WriteFile(filepath.Join(f.work, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.work, "add", file)
	f.git(f.work, "commit", "-q", "-m", file)
	f.git(f.work, "push", "-q", f.gitDir, branch)
	return f.git(f.work, "rev-parse", "HEAD")
}

func (f *milestoneForge) ref(branch string) (string, bool) {
	out, err := exec.Command("git", "--git-dir", f.gitDir, "rev-parse", "--verify", "-q", "refs/heads/"+branch).Output()
	return strings.TrimSpace(string(out)), err == nil
}

func (f *milestoneForge) issue(n int64, poster, state, body string) {
	f.issues[n] = map[string]any{"number": n, "title": fmt.Sprintf("Issue %d", n), "state": state, "body": body, "user": map[string]string{"login": poster}}
}

func (f *milestoneForge) setState(n int64, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues[n]["state"] = state
}

func (f *milestoneForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	const repo = "/api/v1/repos/bottown/demo"
	p, page := r.URL.Path, r.URL.Query().Get("page")
	if r.Method != "GET" {
		f.writes = append(f.writes, r.Method+" "+strings.TrimPrefix(p, repo)+" by "+strings.TrimPrefix(r.Header.Get("Authorization"), "token "))
	}
	var n int64
	switch {
	case r.Method == "GET" && p == repo:
		write(map[string]any{"name": "demo", "default_branch": "main"})
	case r.Method == "GET" && p == repo+"/issues":
		list := []any{}
		if page == "" || page == "1" {
			for i := int64(1); i <= 20; i++ {
				if is, ok := f.issues[i]; ok {
					list = append(list, is)
				}
			}
		}
		write(list)
	case r.Method == "GET" && func() bool { _, err := fmt.Sscanf(p, repo+"/issues/%d/dependencies", &n); return err == nil }():
		list := []any{}
		if page == "" || page == "1" {
			for _, d := range f.deps[n] {
				list = append(list, f.issues[d])
			}
		}
		write(list)
	case r.Method == "GET" && strings.HasPrefix(p, repo+"/branches/"):
		name := strings.TrimPrefix(p, repo+"/branches/")
		sha, ok := f.ref(name)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			write(map[string]string{"message": "branch not found"})
			return
		}
		write(map[string]any{"name": name, "commit": map[string]string{"id": sha}})
	case r.Method == "POST" && p == repo+"/branches":
		var in struct {
			New string `json:"new_branch_name"`
			Old string `json:"old_branch_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		sha, _ := f.ref(in.Old)
		f.git(f.gitDir, "update-ref", "refs/heads/"+in.New, sha)
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"name": in.New, "commit": map[string]string{"id": sha}})
	case r.Method == "GET" && p == repo+"/pulls":
		list := []any{}
		if page == "" || page == "1" {
			for _, pr := range f.prs {
				list = append(list, pr)
			}
		}
		write(list)
	case r.Method == "POST" && p == repo+"/pulls":
		var in struct{ Head, Base, Title, Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		pr := map[string]any{"number": int64(30 + len(f.prs)), "state": "open", "title": in.Title, "body": in.Body,
			"user":     map[string]string{"login": strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "token "), "tok_")},
			"html_url": fmt.Sprintf("http://forge/bottown/demo/pulls/%d", 30+len(f.prs)),
			"head":     map[string]string{"ref": in.Head}, "base": map[string]string{"ref": in.Base}}
		f.prs = append(f.prs, pr)
		w.WriteHeader(http.StatusCreated)
		write(pr)
	case r.Method == "PATCH" && func() bool { _, err := fmt.Sscanf(p, repo+"/pulls/%d", &n); return err == nil }():
		var in struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, pr := range f.prs {
			if pr["number"] == n {
				pr["body"] = in.Body
				write(pr)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		f.t.Errorf("unexpected upstream request %s %s", r.Method, p)
		w.WriteHeader(http.StatusNotFound)
	}
}

func marker(plan, role string) string {
	return fmt.Sprintf("Body.\n\n<!-- bottown:milestone v=1 plan=%q %s sha=\"0123456789abcdef\" -->", plan, role)
}

// newMilestoneServer is a server over a fake Forgejo holding plan demo:
// epic #7 blocked by its active children #3 (closed) and #4 (open), and by
// the owner's #5 with no marker; #6 is a child whose key the plan dropped
// (open, no longer blocking the epic); #8 an actor's copy of a child
// marker, made to block the epic.
func newMilestoneServer(t *testing.T) (*milestoneForge, func(key, method, path, body string) *httptest.ResponseRecorder, string, string) {
	t.Helper()
	root := t.TempDir()
	f := &milestoneForge{t: t, gitDir: filepath.Join(root, "bottown", "demo.git"), work: t.TempDir(),
		issues: map[int64]map[string]any{}, deps: map[int64][]int64{}}
	f.git(f.work, "init", "-q", "-b", "main")
	f.git(f.work, "commit", "-q", "--allow-empty", "-m", "start")
	f.git(f.work, "init", "-q", "--bare", f.gitDir)
	f.git(f.work, "push", "-q", f.gitDir, "main")
	f.issue(3, "owner", "closed", marker("demo", `key="a"`))
	f.issue(4, "owner", "open", marker("demo", `key="b"`))
	f.issue(5, "owner", "open", "Owner's own.")
	f.issue(6, "owner", "open", marker("demo", `key="gone"`))
	f.issue(7, "owner", "open", marker("demo", `role="epic"`))
	f.issue(8, "agy", "open", marker("demo", `key="c"`))
	f.issue(9, "owner", "open", marker("other", `role="epic"`))
	f.deps[7] = []int64{3, 4, 5, 8}
	upstream := httptest.NewServer(f)
	t.Cleanup(upstream.Close)
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
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "owner": {Token: "tok_owner"}, "manager": {Token: "tok_manager"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ownerKey, err := store.NewAPIKey("owner", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	actorKey, err := store.NewAPIKey("claude", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Owner: "owner", ForgejoURL: u, Store: store, GitRoot: root,
		Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{
			Org:     "bottown",
			Actors:  []config.Actor{{Name: "claude", Family: "anthropic", Harness: "claude"}},
			Manager: config.Account{Name: "manager"},
		}},
		Policy: policy.Default("owner"), Log: slog.Default(),
	}
	// The push lands as Forgejo would take it: refused unless it fast-forwards.
	srv.milestonePushFn = func(_ context.Context, gitDir, branch, commit string) error {
		old, _ := f.ref(branch)
		f.git(gitDir, "merge-base", "--is-ancestor", old, commit)
		f.git(gitDir, "update-ref", "refs/heads/"+branch, commit, old)
		f.mu.Lock()
		f.writes = append(f.writes, "push "+branch+" by manager")
		f.mu.Unlock()
		return nil
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
	return f, do, ownerKey, actorKey
}

func TestMilestoneOpsOwnerOnly(t *testing.T) {
	f, do, _, actorKey := newMilestoneServer(t)
	for _, op := range []string{"branch", "merge-base", "pr"} {
		w := do(actorKey, "POST", "/v1/forge/milestone/"+op, `{"repo":"demo","slug":"demo","sha":"abc"}`)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s by an actor: %d (%s), want 403", op, w.Code, w.Body.String())
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("writes %v", f.writes)
	}
}

// The branch is created once, from the default branch, as the manager;
// again, it is reported. A plan with no epic gets none.
func TestMilestoneBranch(t *testing.T) {
	f, do, ownerKey, _ := newMilestoneServer(t)
	main, _ := f.ref("main")
	if w := do(ownerKey, "POST", "/v1/forge/milestone/branch", `{"repo":"demo","slug":"nope"}`); w.Code != http.StatusNotFound {
		t.Errorf("no epic: %d (%s), want 404", w.Code, w.Body.String())
	}
	for i, wantCreated := range []bool{true, false} {
		w := do(ownerKey, "POST", "/v1/forge/milestone/branch", `{"repo":"demo","slug":"demo"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: %d (%s)", i, w.Code, w.Body.String())
		}
		var out MilestoneBranchOut
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out != (MilestoneBranchOut{Branch: "milestone/demo", SHA: main, Epic: 7, Created: wantCreated}) {
			t.Errorf("call %d: %+v", i, out)
		}
	}
	if want := []string{"POST /branches by tok_manager"}; strings.Join(f.writes, "|") != strings.Join(want, "|") {
		t.Errorf("writes %v, want %v", f.writes, want)
	}
}

func TestMilestoneMergeBase(t *testing.T) {
	f, do, ownerKey, _ := newMilestoneServer(t)
	if w := do(ownerKey, "POST", "/v1/forge/milestone/branch", `{"repo":"demo","slug":"demo"}`); w.Code != http.StatusOK {
		t.Fatalf("branch: %d (%s)", w.Code, w.Body.String())
	}
	f.git(f.work, "fetch", "-q", f.gitDir, "milestone/demo:milestone/demo")
	f.commit("milestone/demo", "a.txt", "child a\n")
	f.writes = nil
	mergeBase := func() *httptest.ResponseRecorder {
		return do(ownerKey, "POST", "/v1/forge/milestone/merge-base", `{"repo":"demo","slug":"demo"}`)
	}

	// #4 is open; the dropped #6, the owner's #5 and the actor's #8 are not
	// active children.
	w := mergeBase()
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "#4 Issue 4") ||
		strings.Contains(w.Body.String(), "#5") || strings.Contains(w.Body.String(), "#6") || strings.Contains(w.Body.String(), "#8") {
		t.Fatalf("open child: %d (%s), want 409 naming #4 alone", w.Code, w.Body.String())
	}
	f.setState(4, "closed")

	// Up to date: nothing to merge.
	head, _ := f.ref("milestone/demo")
	w = mergeBase()
	var out MilestoneMergeOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil {
		t.Fatalf("up to date: %d (%s)", w.Code, w.Body.String())
	}
	if out.Merged || out.HeadSHA != head || out.Epic != 7 || out.Base != "main" {
		t.Errorf("up to date: %+v", out)
	}

	// main moves: merged as the manager.
	main := f.commit("main", "m.txt", "main\n")
	w = mergeBase()
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil {
		t.Fatalf("merge: %d (%s)", w.Code, w.Body.String())
	}
	now, _ := f.ref("milestone/demo")
	if !out.Merged || out.HeadSHA != now || out.BaseSHA != main || now == head {
		t.Errorf("merge: %+v; branch at %s", out, now)
	}
	if parents := f.git(f.gitDir, "log", "-1", "--format=%P %an", now); parents != head+" "+main+" manager" {
		t.Errorf("merge commit: %q", parents)
	}

	// A conflict leaves the branch alone.
	f.git(f.work, "fetch", "-q", f.gitDir, "milestone/demo:milestone/demo")
	f.commit("milestone/demo", "x.txt", "branch\n")
	f.commit("main", "x.txt", "main\n")
	before, _ := f.ref("milestone/demo")
	f.writes = nil
	w = mergeBase()
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "x.txt") || !strings.Contains(w.Body.String(), "unchanged") {
		t.Errorf("conflict: %d (%s)", w.Code, w.Body.String())
	}
	if after, _ := f.ref("milestone/demo"); after != before || len(f.writes) != 0 {
		t.Errorf("conflict moved the branch %s -> %s; writes %v", before, after, f.writes)
	}
}

// The final PR is opened once as the manager, then updated; its
// description lists the merged active children and closes the epic.
func TestMilestonePR(t *testing.T) {
	f, do, ownerKey, _ := newMilestoneServer(t)
	f.setState(4, "closed")
	if w := do(ownerKey, "POST", "/v1/forge/milestone/branch", `{"repo":"demo","slug":"demo"}`); w.Code != http.StatusOK {
		t.Fatalf("branch: %d (%s)", w.Code, w.Body.String())
	}
	head, _ := f.ref("milestone/demo")
	f.prs = append(f.prs, map[string]any{"number": int64(21), "state": "closed", "merged": true,
		"head": map[string]string{"ref": "issue/3"}, "base": map[string]string{"ref": "milestone/demo"}})
	f.writes = nil
	if w := do(ownerKey, "POST", "/v1/forge/milestone/pr", fmt.Sprintf(`{"repo":"demo","slug":"demo","sha":%q}`, "0000000")); w.Code != http.StatusConflict {
		t.Errorf("stale sha: %d (%s), want 409", w.Code, w.Body.String())
	}
	for i, wantCreated := range []bool{true, false} {
		w := do(ownerKey, "POST", "/v1/forge/milestone/pr", fmt.Sprintf(`{"repo":"demo","slug":"demo","sha":%q,"checks":"make e2e passed (%d)"}`, head, i))
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: %d (%s)", i, w.Code, w.Body.String())
		}
		var out MilestonePROut
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Number != 31 || out.Created != wantCreated {
			t.Errorf("call %d: %+v", i, out)
		}
	}
	if want := "POST /pulls by tok_manager|PATCH /pulls/31 by tok_manager"; strings.Join(f.writes, "|") != want {
		t.Errorf("writes %v, want %s", f.writes, want)
	}
	pr := f.prs[1]
	body := pr["body"].(string)
	if pr["user"].(map[string]string)["login"] != "manager" || pr["title"] != "Issue 7" {
		t.Errorf("PR by %v titled %v", pr["user"], pr["title"])
	}
	for _, s := range []string{"make e2e passed (1)", "- #3 — Issue 3: !21\n", "- #4 — Issue 4: closed with no PR merged into `milestone/demo`\n", "Closes #7"} {
		if !strings.Contains(body, s) {
			t.Errorf("body lacks %q:\n%s", s, body)
		}
	}
	if strings.Contains(body, "#6") || strings.Contains(body, "#8") {
		t.Errorf("body lists issues that are not active children:\n%s", body)
	}
}
