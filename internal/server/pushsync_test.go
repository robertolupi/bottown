package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// pushRepo is a Forgejo repository right after a push to PR 12's branch:
// issue/1 is at the new commit, refs/pull/12/head still at the old one,
// and main has moved since the old commit branched.
type pushRepo struct {
	root, gitDir         string
	base, old, new, main string
}

func newPushRepo(t *testing.T) pushRepo {
	t.Helper()
	root := t.TempDir()
	gitDir := filepath.Join(root, "bottown", "demo.git")
	work := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(file, content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		git(work, "add", file)
		git(work, "commit", "-q", "-m", file)
		return git(work, "rev-parse", "HEAD")
	}
	git(work, "init", "-q", "-b", "main")
	var r pushRepo
	r.root, r.gitDir = root, gitDir
	r.base = commit("README", "demo\n")
	git(work, "checkout", "-q", "-b", "issue/1")
	r.old = commit("agent.txt", "agent\n")
	r.new = commit("agent.txt", "agent\nfixed\n")
	git(work, "checkout", "-q", "main")
	r.main = commit("main-moves.txt", "main moves\n")
	git(work, "init", "-q", "--bare", gitDir)
	git(work, "push", "-q", gitDir, "main", "issue/1")
	git(gitDir, "update-ref", "refs/pull/12/head", r.old)
	return r
}

func (r pushRepo) syncRef(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("git", "--git-dir", r.gitDir, "update-ref", "refs/pull/12/head", r.new).CombinedOutput(); err != nil {
		t.Fatalf("update-ref: %v: %s", err, out)
	}
}

// TestPRReadRightAfterPush: Forgejo reports the pushed head at once, but
// its PR ref, merge base and diff are still from the older head. pr show
// serves the diff at the head from git, with its merge base, and says
// Forgejo is syncing until the PR ref moves.
func TestPRReadRightAfterPush(t *testing.T) {
	repo := newPushRepo(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, ".diff"):
			// Forgejo's diff of the older head on the older merge base.
			_, _ = w.Write([]byte("diff --git a/main-moves.txt b/main-moves.txt\n+stale\n"))
		case strings.HasSuffix(p, "/reviews"), strings.HasSuffix(p, "/statuses"), strings.Contains(p, "/statuses/"):
			_, _ = w.Write([]byte("[]"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "state": "open", "user": map[string]string{"login": "claude"},
				"merge_base": repo.base, "mergeable": false,
				"head": map[string]string{"ref": "issue/1", "sha": repo.new}, "base": map[string]string{"ref": "main"}})
		}
	}))
	defer upstream.Close()
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
	s := &Server{Owner: "rlupi", Store: store, Log: slog.Default(), GitRoot: repo.root, Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}}}
	read := func() PROut {
		t.Helper()
		pr, err := s.opPR(context.Background(), secrets.Principal{Actor: "claude"}, Target{Repo: "demo", Number: 12}, true)
		if err != nil {
			t.Fatal(err)
		}
		return pr
	}
	pr := read()
	if pr.HeadSHA != repo.new || !pr.Syncing {
		t.Fatalf("head %s, syncing %v; want %s and syncing", pr.HeadSHA, pr.Syncing, repo.new)
	}
	if !strings.Contains(pr.Diff, "+fixed") || strings.Contains(pr.Diff, "main-moves.txt") || strings.Contains(pr.Diff, "stale") {
		t.Fatalf("diff is not the head's:\n%s", pr.Diff)
	}
	if pr.MergeBase != repo.base {
		t.Fatalf("merge base %s, want %s", pr.MergeBase, repo.base)
	}
	repo.syncRef(t)
	if pr := read(); pr.Syncing {
		t.Fatal("still syncing after the PR ref moved to the head")
	}
}

// TestAwaitPushSynced waits for Forgejo to move the PR ref, and gives up
// after its bound.
func TestAwaitPushSynced(t *testing.T) {
	repo := newPushRepo(t)
	s := &Server{GitRoot: repo.root, Log: slog.Default()}
	ctx := context.Background()
	if ok, err := s.awaitPushSynced(ctx, "bottown", "demo", 12, repo.new, 100*time.Millisecond); err != nil || ok {
		t.Fatalf("synced = %v, %v before the PR ref moved", ok, err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(150 * time.Millisecond)
		repo.syncRef(t)
	}()
	ok, err := s.awaitPushSynced(ctx, "bottown", "demo", 12, repo.new, 10*time.Second)
	wg.Wait()
	if err != nil || !ok {
		t.Fatalf("synced = %v, %v; want it once the PR ref moved", ok, err)
	}
	// Without Forgejo's repositories there is nothing to wait for.
	if ok, err := (&Server{}).awaitPushSynced(ctx, "bottown", "demo", 12, repo.new, time.Hour); err != nil || !ok {
		t.Fatalf("no git root: synced = %v, %v", ok, err)
	}
}

// TestReconcileUndismissesApprovalAtHead: Forgejo dismissed an approval at
// the head because it processed the push of that head after the review.
// Reconcile undismisses it; an approval at the head the owner dismissed
// stays dismissed.
func TestReconcileUndismissesApprovalAtHead(t *testing.T) {
	repo := newPushRepo(t)
	repo.syncRef(t)
	var mu sync.Mutex
	var undismissed []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/undismissals"):
			parts := strings.Split(p, "/")
			undismissed = append(undismissed, parts[len(parts)-2])
			_, _ = w.Write([]byte("{}"))
		case strings.HasSuffix(p, "/timeline"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 11, "type": "dismiss_review", "review_id": 1, "body": forgejo.AutoDismissMessage},
				{"id": 12, "type": "dismiss_review", "review_id": 2, "body": "no"},
			})
		case strings.HasSuffix(p, "/reviews"):
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "state": "APPROVED", "user": map[string]string{"login": "agy"}, "commit_id": repo.new, "official": true, "dismissed": true},
				{"id": 2, "state": "APPROVED", "user": map[string]string{"login": "kimi"}, "commit_id": repo.new, "official": true, "dismissed": true},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "state": "open", "user": map[string]string{"login": "claude"},
				"head": map[string]string{"ref": "issue/1", "sha": repo.new}, "base": map[string]string{"ref": "main"}})
		}
	}))
	defer upstream.Close()
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"rlupi": {Token: "tok_owner"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Owner: "rlupi", Store: store, Log: slog.Default(), GitRoot: repo.root, Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}}}
	if _, err := s.opReconcileCarries(context.Background(), secrets.Principal{Actor: "rlupi"}, Target{Repo: "demo", Number: 12}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(undismissed, ",") != "1" {
		t.Fatalf("undismissed %v, want only Forgejo's own dismissal", undismissed)
	}
}
