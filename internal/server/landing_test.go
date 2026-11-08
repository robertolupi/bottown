package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/gitmerge"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
	"github.com/robertolupi/bottown/internal/secrets"
)

// landingForge is a fake Forgejo over a real git repository: the API reads
// the PR, its reviews and its timeline, and the git smart-HTTP push updates
// the branch and refs/pull/12/head, as Forgejo's receive does.
type landingForge struct {
	gitDir   string
	baseRef  string // base branch name
	headRef  string // head branch name
	title    string
	reviews  []map[string]any
	timeline []map[string]any
	merged   bool

	mu          sync.Mutex
	undismissed []string
}

// head reads the PR's head from the branch, as Forgejo reports it at once
// (pushsync.go).
func (f *landingForge) head(t *testing.T) string {
	t.Helper()
	out, err := mergeGit(context.Background(), f.gitDir, nil, nil, "rev-parse", "--verify", "refs/heads/"+f.headRef+"^{commit}")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// syncPullRef moves refs/pull/12/head to the branch head after a push, as
// Forgejo does in the background.
func (f *landingForge) syncPullRef() {
	head, err := mergeGit(context.Background(), f.gitDir, nil, nil, "rev-parse", "--verify", "refs/heads/"+f.headRef+"^{commit}")
	if err != nil {
		return
	}
	_, _ = mergeGit(context.Background(), f.gitDir, nil, nil, "update-ref", "refs/pull/12/head", strings.TrimSpace(head))
}

func (f *landingForge) serveAPI(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/undismissals"):
		parts := strings.Split(p, "/")
		f.undismissed = append(f.undismissed, parts[len(parts)-2])
		_, _ = w.Write([]byte("{}"))
	case strings.HasSuffix(p, "/timeline"):
		_ = json.NewEncoder(w).Encode(f.timeline)
	case strings.HasSuffix(p, "/reviews"):
		if page := r.URL.Query().Get("page"); page != "" && page != "1" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_ = json.NewEncoder(w).Encode(f.reviews)
	case strings.HasSuffix(p, "/pulls/12"):
		head, _ := mergeGit(context.Background(), f.gitDir, nil, nil, "rev-parse", "--verify", "refs/heads/"+f.headRef+"^{commit}")
		state := "open"
		if f.merged {
			state = "closed"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 12, "title": f.title, "state": state, "merged": f.merged, "mergeable": true,
			"user": map[string]string{"login": "claude"},
			"head": map[string]string{"ref": f.headRef, "sha": strings.TrimSpace(head)},
			"base": map[string]string{"ref": f.baseRef},
		})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	}
}

// landingServer wires a Server to a landingForge: the API answers JSON, the
// git paths are served by git http-backend over the repository, and the
// server's owner is rlupi.
func landingServer(t *testing.T, f *landingForge) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bottown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.gitDir, filepath.Join(root, "bottown", "demo.git")); err != nil {
		t.Fatal(err)
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".git") {
			backend.ServeHTTP(w, r)
			if strings.HasSuffix(r.URL.Path, "git-receive-pack") {
				f.syncPullRef()
			}
			return
		}
		f.serveAPI(w, r)
	}))
	t.Cleanup(upstream.Close)
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"rlupi": {Token: "tok_owner"}, "claude": {Token: "tok_claude"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{
		Owner:   "rlupi",
		Store:   store,
		Log:     slog.Default(),
		GitRoot: root,
		Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{
			Org: "bottown",
			Actors: []config.Actor{
				{Name: "claude", Family: "anthropic"}, {Name: "agy", Family: "google"}, {Name: "kimi", Family: "moonshot"},
			},
		}},
		Policy: policy.Default("rlupi"),
	}
}

// gitRepoFixture builds a throwaway repository with a worktree.
type gitRepoFixture struct {
	t   *testing.T
	dir string
}

// gitCommand is the test's git: a clean config and fixed identities.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	return cmd
}

func newGitRepo(t *testing.T) *gitRepoFixture {
	return &gitRepoFixture{t: t, dir: filepath.Join(t.TempDir(), "demo")}
}

func (r *gitRepoFixture) run(args ...string) string {
	r.t.Helper()
	cmd := gitCommand(r.t.Context(), r.dir, args...)
	if args[0] == "init" {
		cmd.Dir = ""
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *gitRepoFixture) write(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *gitRepoFixture) commit(msg string) {
	r.t.Helper()
	r.run("add", "-A")
	r.run("commit", "-qm", msg)
}

func (r *gitRepoFixture) gitDir() string { return filepath.Join(r.dir, ".git") }

// configureRepo lets the test push to the branch a worktree has checked
// out, as the shared fixtures do.
func configureRepo(t *testing.T, gitDir string) {
	t.Helper()
	for _, kv := range [][2]string{{"receive.denyCurrentBranch", "ignore"}, {"http.receivepack", "true"}} {
		if _, err := mergeGit(context.Background(), gitDir, nil, nil, "config", kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
}

// landingRepo builds a PR branch behind its base cleanly: main moved in
// another file after the PR's commit, so the trial merge is clean and its
// tree differs from the head's.
func landingRepo(t *testing.T) (gitDir, base, head string) {
	r := newGitRepo(t)
	r.run("init", "-q", "-b", "main", r.dir)
	r.write("a.txt", "line 1\nline 2\nline 3\nline 4\n")
	r.write("b.txt", "line 1\nline 2\nline 3\nline 4\n")
	r.commit("base")
	r.run("checkout", "-q", "-b", "issue/1")
	r.write("a.txt", "line 1\nline 2\nline 3\nline 4\nPR\n")
	r.commit("pr")
	head = r.run("rev-parse", "HEAD")
	r.run("checkout", "-q", "main")
	r.write("b.txt", "line 1\nline 2\nline 3\nline 4\nmain\n")
	r.commit("main moves")
	base = r.run("rev-parse", "HEAD")
	configureRepo(t, r.gitDir())
	return r.gitDir(), base, head
}

// conflictingRepo builds a PR branch whose trial merge with its base
// conflicts in a.txt.
func conflictingRepo(t *testing.T) (gitDir, base, head string) {
	r := newGitRepo(t)
	r.run("init", "-q", "-b", "main", r.dir)
	r.write("a.txt", "base\n")
	r.commit("base")
	r.run("checkout", "-q", "-b", "issue/1")
	r.write("a.txt", "pr\n")
	r.commit("pr")
	head = r.run("rev-parse", "HEAD")
	r.run("checkout", "-q", "main")
	r.write("a.txt", "main\n")
	r.commit("main moves")
	base = r.run("rev-parse", "HEAD")
	configureRepo(t, r.gitDir())
	return r.gitDir(), base, head
}

// filesRepo builds a PR whose delta touches files and renames one.
func filesRepo(t *testing.T) (gitDir, head string) {
	r := newGitRepo(t)
	r.run("init", "-q", "-b", "main", r.dir)
	r.write("a.txt", "a\n")
	r.write("b.txt", "b\n")
	r.write("old.txt", "old\n")
	r.commit("base")
	r.run("checkout", "-q", "-b", "issue/1")
	r.write("a.txt", "a changed\n")
	r.write("c.txt", "c\n")
	r.run("mv", "old.txt", "new.txt")
	r.commit("pr")
	head = r.run("rev-parse", "HEAD")
	configureRepo(t, r.gitDir())
	return r.gitDir(), head
}

// TestPRLandingCommit checks the landing commit's parents and tree on a PR
// behind its base cleanly: first parent the base tip, second the head, and
// the trial merge's tree.
func TestPRLandingCommit(t *testing.T) {
	gitDir, base, head := landingRepo(t)
	f := &landingForge{gitDir: gitDir, baseRef: "main", headRef: "issue/1", title: "the PR",
		reviews: []map[string]any{{"id": 1, "state": "APPROVED", "user": map[string]string{"login": "agy"},
			"commit_id": head, "official": true, "stale": false, "dismissed": false}}}
	s := landingServer(t, f)

	out, err := s.opPRLanding(t.Context(), secrets.Principal{Actor: "rlupi"}, PRLandingIn{Target: Target{Repo: "demo", Number: 12}, HeadSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	if out.BaseSHA != base || out.HeadSHA == "" {
		t.Fatalf("out = %+v, want base %s and a landing commit", out, base)
	}
	parents, err := mergeGit(t.Context(), gitDir, nil, nil, "rev-list", "--parents", "-n", "1", out.HeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(parents); len(got) != 3 || got[1] != base || got[2] != head {
		t.Fatalf("parents = %v, want %s then %s", got, base, head)
	}
	merged, err := gitmerge.MergeTree(t.Context(), gitDir, head, base)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := mergeGit(t.Context(), gitDir, nil, nil, "rev-parse", out.HeadSHA+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(tree) != merged.Tree {
		t.Fatalf("tree = %s, want the trial merge's %s", strings.TrimSpace(tree), merged.Tree)
	}
	if out.Assessment.Decision != review.Approved {
		t.Fatalf("assessment = %+v, want approved", out.Assessment)
	}
	if got := f.head(t); got != out.HeadSHA {
		t.Fatalf("branch head = %s, want the landing commit %s", got, out.HeadSHA)
	}
}

// TestPRLandingHeadContainsBase checks the landing commit when the head
// already contains the base's tip: the head's tree, with the same parents.
func TestPRLandingHeadContainsBase(t *testing.T) {
	gitDir, base, head := landingRepo(t)
	// Merge the base into the head with a real merge commit, so the head
	// contains the base tip.
	trial, err := gitmerge.MergeTree(t.Context(), gitDir, head, base)
	if err != nil {
		t.Fatal(err)
	}
	mergeCommit, err := mergeGit(t.Context(), gitDir, strings.NewReader("merge main\n"), []string{
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	}, "commit-tree", trial.Tree, "-p", head, "-p", base)
	if err != nil {
		t.Fatal(err)
	}
	head = strings.TrimSpace(mergeCommit)
	if _, err := mergeGit(t.Context(), gitDir, nil, nil, "update-ref", "refs/heads/issue/1", head); err != nil {
		t.Fatal(err)
	}
	f := &landingForge{gitDir: gitDir, baseRef: "main", headRef: "issue/1", title: "the PR"}
	s := landingServer(t, f)

	out, err := s.opPRLanding(t.Context(), secrets.Principal{Actor: "rlupi"}, PRLandingIn{Target: Target{Repo: "demo", Number: 12}, HeadSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	parents, err := mergeGit(t.Context(), gitDir, nil, nil, "rev-list", "--parents", "-n", "1", out.HeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(parents); len(got) != 3 || got[1] != base || got[2] != head {
		t.Fatalf("parents = %v, want %s then %s", got, base, head)
	}
	tree, err := mergeGit(t.Context(), gitDir, nil, nil, "rev-parse", out.HeadSHA+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(tree) != trial.Tree {
		t.Fatalf("tree = %s, want the head's (the merged) %s", strings.TrimSpace(tree), trial.Tree)
	}
}

// TestPRLandingCarry runs the landing over the shared patch cases: the
// approval at the old head carries to the landing commit when the patch is
// unchanged, and is lost when the base edited the diff's context.
func TestPRLandingCarry(t *testing.T) {
	for _, tc := range reviewtest.PatchCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			configureRepo(t, tc.GitDir)
			f := &landingForge{gitDir: tc.GitDir, baseRef: "main", headRef: "issue/1", title: "the PR",
				reviews: []map[string]any{{"id": 1, "state": "APPROVED", "user": map[string]string{"login": "agy"},
					"commit_id": tc.Old, "official": true, "stale": true, "dismissed": true}},
				timeline: []map[string]any{{"id": 11, "type": "dismiss_review", "review_id": 1, "body": forgejo.AutoDismissMessage}}}
			s := landingServer(t, f)

			out, err := s.opPRLanding(t.Context(), secrets.Principal{Actor: "rlupi"}, PRLandingIn{Target: Target{Repo: "demo", Number: 12}, HeadSHA: tc.Head})
			if err != nil {
				t.Fatal(err)
			}
			if tc.Same {
				if out.Assessment.Decision != review.Approved || out.Assessment.Carried == nil || out.Assessment.Carried.From != tc.Old {
					t.Fatalf("assessment = %+v, want the approval carried over from %s", out.Assessment, tc.Old)
				}
				if !slices.Equal(f.undismissed, []string{"1"}) {
					t.Fatalf("undismissed = %v, want the carried approval", f.undismissed)
				}
				return
			}
			if out.Assessment.Decision != review.NeedsReview || len(f.undismissed) != 0 {
				t.Fatalf("assessment = %+v, undismissed = %v; want needs review and none", out.Assessment, f.undismissed)
			}
		})
	}
}

// TestPRLandingRefusals checks the 409s: a moved head, a live run and a
// conflict, with nothing pushed.
func TestPRLandingRefusals(t *testing.T) {
	t.Run("moved head", func(t *testing.T) {
		gitDir, _, head := landingRepo(t)
		f := &landingForge{gitDir: gitDir, baseRef: "main", headRef: "issue/1"}
		s := landingServer(t, f)
		_, err := s.opPRLanding(t.Context(), secrets.Principal{Actor: "rlupi"}, PRLandingIn{Target: Target{Repo: "demo", Number: 12}, HeadSHA: strings.Repeat("a", 40)})
		if errStatus(err) != http.StatusConflict || !strings.Contains(err.Error(), "head moved") {
			t.Fatalf("err = %v, want 409 head moved", err)
		}
		if f.head(t) != head {
			t.Fatal("the branch moved")
		}
	})
	t.Run("live run", func(t *testing.T) {
		gitDir, _, head := landingRepo(t)
		f := &landingForge{gitDir: gitDir, baseRef: "main", headRef: "issue/1"}
		s := landingServer(t, f)
		now := time.Now()
		if _, err := s.Store.NewRun(secrets.Run{ID: "r-1", Kind: policy.Verify, Actor: "agy", Org: "bottown", Repo: "demo",
			PR: 12, Created: now, Expires: now.Add(time.Hour)}, false); err != nil {
			t.Fatal(err)
		}
		_, err := s.opPRLanding(t.Context(), secrets.Principal{Actor: "rlupi"}, PRLandingIn{Target: Target{Repo: "demo", Number: 12}, HeadSHA: head})
		if errStatus(err) != http.StatusConflict || !strings.Contains(err.Error(), "live on !12") {
			t.Fatalf("err = %v, want 409 live run", err)
		}
		if f.head(t) != head {
			t.Fatal("the branch moved")
		}
	})
	t.Run("conflict", func(t *testing.T) {
		gitDir, _, head := conflictingRepo(t)
		f := &landingForge{gitDir: gitDir, baseRef: "main", headRef: "issue/1"}
		s := landingServer(t, f)
		_, err := s.opPRLanding(t.Context(), secrets.Principal{Actor: "rlupi"}, PRLandingIn{Target: Target{Repo: "demo", Number: 12}, HeadSHA: head})
		if errStatus(err) != http.StatusConflict || !strings.Contains(err.Error(), "merge conflicts in a.txt") {
			t.Fatalf("err = %v, want 409 naming a.txt", err)
		}
		if f.head(t) != head {
			t.Fatal("the branch moved")
		}
	})
}

// TestPRMergeBaseFiles checks the files the PR's own delta touches, a
// rename counted as both paths.
func TestPRMergeBaseFiles(t *testing.T) {
	gitDir, _ := filesRepo(t)
	f := &landingForge{gitDir: gitDir, baseRef: "main", headRef: "issue/1", title: "the PR"}
	s := landingServer(t, f)
	status, err := s.opPRMergeStatus(t.Context(), secrets.Principal{Actor: "rlupi"}, Target{Repo: "demo", Number: 12})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "c.txt", "new.txt", "old.txt"}
	if !slices.Equal(status.Files, want) {
		t.Fatalf("files = %v, want %v", status.Files, want)
	}
}

// TestPRLandingActorsRefused checks an actor's and a run's key get 403 on
// the route.
func TestPRLandingActorsRefused(t *testing.T) {
	gitDir, _, head := landingRepo(t)
	f := &landingForge{gitDir: gitDir, baseRef: "main", headRef: "issue/1", title: "the PR"}
	s := landingServer(t, f)
	s.health.Reconciled = true
	now := time.Now()
	keys := map[string]string{}
	runKey, err := s.Store.NewRun(secrets.Run{ID: "r-1", Kind: policy.Implement, Actor: "claude", Org: "bottown", Repo: "demo",
		Issue: 1, PR: 12, Branch: "issue/1", Created: now, Expires: now.Add(time.Hour)}, false)
	if err != nil {
		t.Fatal(err)
	}
	keys["run key"] = runKey
	actorKey, err := s.Store.NewAPIKey("claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	keys["actor key"] = actorKey
	h := s.Handler()
	for name, key := range keys {
		req := httptest.NewRequest(http.MethodPost, "/v1/forge/pr/landing", strings.NewReader(`{"repo":"demo","number":12,"head_sha":"`+head+`"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: POST /v1/forge/pr/landing = %d (%s), want 403", name, w.Code, w.Body.String())
		}
	}
	// The operation refuses them too, whatever the route.
	for _, p := range []secrets.Principal{{Actor: "claude"}, {Actor: "rlupi", Run: &secrets.Run{Kind: policy.Implement}}} {
		if _, err := s.opPRLanding(t.Context(), p, PRLandingIn{Target: Target{Repo: "demo", Number: 12}, HeadSHA: head}); errStatus(err) != http.StatusForbidden {
			t.Errorf("%+v: err = %v, want 403", p, err)
		}
	}
	if f.head(t) != head {
		t.Fatal("the branch moved")
	}
}
