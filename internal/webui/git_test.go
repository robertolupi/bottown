package webui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v failed: %v\nOutput: %s", dir, args, err, out)
	}
	return string(out)
}

func setupThrowawayRepos(t *testing.T) (mainCheckout, originDir, forgeDir string) {
	t.Helper()
	tmp := t.TempDir()

	originDir = filepath.Join(tmp, "origin.git")
	forgeDir = filepath.Join(tmp, "forge.git")
	mainCheckout = filepath.Join(tmp, "main")

	// 1. Create bare repos for origin and forge
	if err := os.MkdirAll(originDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmp, "init", "--bare", "-b", "main", originDir)

	if err := os.MkdirAll(forgeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmp, "init", "--bare", "-b", "main", forgeDir)

	// 2. Setup main checkout
	if err := os.MkdirAll(mainCheckout, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainCheckout, "init", "-b", "main")
	runGit(t, mainCheckout, "config", "user.name", "Tester")
	runGit(t, mainCheckout, "config", "user.email", "tester@example.com")
	runGit(t, mainCheckout, "remote", "add", "origin", originDir)
	runGit(t, mainCheckout, "remote", "add", "bottown", forgeDir)

	// Initial commit with AGENTS.md
	if err := os.WriteFile(filepath.Join(mainCheckout, "AGENTS.md"), []byte("# Agents"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainCheckout, "add", "AGENTS.md")
	runGit(t, mainCheckout, "commit", "-m", "initial commit")

	// Push main to both remotes
	runGit(t, mainCheckout, "push", "origin", "main")
	runGit(t, mainCheckout, "push", "bottown", "main")

	return mainCheckout, originDir, forgeDir
}

func TestGitState_UpstreamAndActions(t *testing.T) {
	mainCheckout, originDir, forgeDir := setupThrowawayRepos(t)

	mockHarnessCalled := false
	cfg := Config{
		Instance: config.Instance{
			Name: "prod",
		},
		RepoTop:    mainCheckout,
		FullRepo:   "bottown/bottown",
		OwnerKey:   "",
		CLIVersion: "dev",
		HarnessChecker: func(home string) []HarnessCommandInfo {
			mockHarnessCalled = true
			return []HarnessCommandInfo{
				{Harness: "claude", Installed: true, Current: true},
			}
		},
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	ctx := context.Background()

	// 1. Initial status: Up to date
	st, err := srv.ComputeStatus(ctx, true)
	if err != nil {
		t.Fatalf("ComputeStatus: %v", err)
	}
	if !st.Upstream.HasUpstream {
		t.Errorf("expected HasUpstream to be true")
	}
	if st.Upstream.AheadCount != 0 {
		t.Errorf("expected AheadCount=0, got %d", st.Upstream.AheadCount)
	}
	if st.Upstream.BehindCount != 0 {
		t.Errorf("expected BehindCount=0, got %d", st.Upstream.BehindCount)
	}
	if !st.Health.AgentInstructions.Found || st.Health.AgentInstructions.Filename != "AGENTS.md" {
		t.Errorf("expected AGENTS.md to be found, got %+v", st.Health.AgentInstructions)
	}
	if !mockHarnessCalled || len(st.Health.HarnessCommands) != 1 || !st.Health.HarnessCommands[0].Current {
		t.Errorf("expected HarnessChecker to be called and return current harness: %+v", st.Health.HarnessCommands)
	}

	// 2. Add an unpushed commit to main with PR reference
	if err := os.WriteFile(filepath.Join(mainCheckout, "feature.txt"), []byte("new feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainCheckout, "add", "feature.txt")
	runGit(t, mainCheckout, "commit", "-m", "Merge pull request 'add feature' (!42) from issue/40 into main")

	st, err = srv.ComputeStatus(ctx, true)
	if err != nil {
		t.Fatalf("ComputeStatus after commit: %v", err)
	}
	if st.Upstream.AheadCount != 1 {
		t.Fatalf("expected AheadCount=1, got %d", st.Upstream.AheadCount)
	}
	if len(st.Upstream.UnpushedCommits) != 1 {
		t.Fatalf("expected 1 unpushed commit, got %d", len(st.Upstream.UnpushedCommits))
	}
	uc := st.Upstream.UnpushedCommits[0]
	if uc.PRNumber != 42 {
		t.Errorf("expected PRNumber=42, got %d", uc.PRNumber)
	}
	if !strings.Contains(uc.Subject, "add feature") {
		t.Errorf("expected subject to contain 'add feature', got %q", uc.Subject)
	}

	// Test head required: empty head is refused
	if _, err := srv.ExecutePush(ctx, ""); err == nil || !strings.Contains(err.Error(), "head commit SHA is required") {
		t.Fatalf("expected ExecutePush with empty head to be refused, got error: %v", err)
	}

	// Test head mismatch protection: trying to push with a stale head SHA is refused
	staleHead := "1111222233334444555566667777888899990000"
	if _, err := srv.ExecutePush(ctx, staleHead); err == nil || !strings.Contains(err.Error(), "moved to") {
		t.Fatalf("expected ExecutePush with stale head to be refused, got error: %v", err)
	}

	// 3. Test ExecutePush with matching head SHA: push main to origin
	pushOut, err := srv.ExecutePush(ctx, st.Upstream.HeadSHA)
	if err != nil {
		t.Fatalf("ExecutePush: %v, out: %s", err, pushOut)
	}
	// Also push to bottown forge so base is synchronized
	runGit(t, mainCheckout, "push", "bottown", "main")

	st, err = srv.ComputeStatus(ctx, true)
	if err != nil {
		t.Fatalf("ComputeStatus after push: %v", err)
	}
	if st.Upstream.AheadCount != 0 {
		t.Errorf("expected AheadCount=0 after push, got %d", st.Upstream.AheadCount)
	}

	// Test that forced fetch sees new commits pushed to bare origin (Issue 1)
	originClone := filepath.Join(t.TempDir(), "origin-clone")
	runGit(t, t.TempDir(), "clone", originDir, originClone)
	runGit(t, originClone, "config", "user.name", "Upstreamer")
	runGit(t, originClone, "config", "user.email", "up@example.com")
	if err := os.WriteFile(filepath.Join(originClone, "upstream.txt"), []byte("from upstream"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, originClone, "add", "upstream.txt")
	runGit(t, originClone, "commit", "-m", "upstream commit")
	runGit(t, originClone, "push", "origin", "main")

	// Forced status fetch must see the new commit on origin
	st, err = srv.ComputeStatus(ctx, true)
	if err != nil {
		t.Fatalf("ComputeStatus after upstream push: %v", err)
	}
	if st.Upstream.BehindCount != 1 {
		t.Fatalf("expected Upstream.BehindCount=1 after forced fetch, got %d", st.Upstream.BehindCount)
	}

	// 4. Test Behind Forge & ExecutePull
	// Clone a secondary worktree to simulate a commit pushed to forge from elsewhere
	otherClone := filepath.Join(t.TempDir(), "other")
	runGit(t, t.TempDir(), "clone", forgeDir, otherClone)
	runGit(t, otherClone, "config", "user.name", "Other")
	runGit(t, otherClone, "config", "user.email", "other@example.com")
	if err := os.WriteFile(filepath.Join(otherClone, "remote.txt"), []byte("from forge"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, otherClone, "add", "remote.txt")
	runGit(t, otherClone, "commit", "-m", "commit from forge")
	runGit(t, otherClone, "push", "origin", "main")

	// Main checkout fetches forge and should see BehindCount=1
	st, err = srv.ComputeStatus(ctx, true)
	if err != nil {
		t.Fatalf("ComputeStatus after remote push: %v", err)
	}
	if st.Forge.BehindCount != 1 {
		t.Fatalf("expected Forge.BehindCount=1, got %d", st.Forge.BehindCount)
	}

	// ExecutePull
	pullOut, err := srv.ExecutePull(ctx)
	if err != nil {
		t.Fatalf("ExecutePull: %v, out: %s", err, pullOut)
	}

	st, err = srv.ComputeStatus(ctx, false)
	if err != nil {
		t.Fatalf("ComputeStatus after pull: %v", err)
	}
	if st.Forge.BehindCount != 0 {
		t.Errorf("expected Forge.BehindCount=0 after pull, got %d", st.Forge.BehindCount)
	}
}

func TestGitState_ActorWorktrees(t *testing.T) {
	mainCheckout, _, _ := setupThrowawayRepos(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	mockServerCalled := false
	mockServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/runs" {
			mockServerCalled = true
			repo := r.URL.Query().Get("repo")
			state := r.URL.Query().Get("state")
			if repo != "bottown" {
				t.Errorf("expected repo query 'bottown', got %q", repo)
			}
			if state != "live" {
				t.Errorf("expected state query 'live', got %q", state)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "r-98765", "actor": "claude"},
				// An ephemeral run of the same actor: it must not be shown
				// as the permanent worktree's run.
				{"id": "r-11fe1234", "actor": "claude"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	mockServer.Listener = listener
	mockServer.Start()
	defer mockServer.Close()

	cfg := Config{
		Instance: config.Instance{
			Name:       "prod",
			ServerPort: port,
		},
		RepoTop:  mainCheckout,
		FullRepo: "bottown/bottown",
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Enable worktreeConfig extension so --worktree config can be set
	runGit(t, mainCheckout, "config", "extensions.worktreeConfig", "true")

	// Create an actor worktree
	actorPath := filepath.Join(t.TempDir(), "claude")
	runGit(t, mainCheckout, "worktree", "add", actorPath, "-b", "issue/10")
	runGit(t, actorPath, "config", "--worktree", "bottown.actor", "claude")
	runGit(t, actorPath, "config", "--worktree", "bottown.run", "r-98765")

	wts := srv.computeWorktrees(context.Background())
	if !mockServerCalled {
		t.Fatalf("expected /v1/runs mock server to be queried")
	}
	if len(wts) != 1 {
		t.Fatalf("expected 1 actor worktree, got %d", len(wts))
	}
	wt := wts[0]
	if wt.Actor != "claude" {
		t.Errorf("expected actor claude, got %s", wt.Actor)
	}
	if wt.LastRun != "r-98765" {
		t.Errorf("expected last run r-98765, got %s", wt.LastRun)
	}
	if wt.ActiveRun != "r-98765" {
		t.Errorf("expected active run r-98765, got %s", wt.ActiveRun)
	}
	if wt.IsDirty {
		t.Errorf("expected clean worktree, got dirty")
	}

	// Make it dirty by creating an uncommitted file
	if err := os.WriteFile(filepath.Join(actorPath, "scratch.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	wts = srv.computeWorktrees(context.Background())
	if len(wts) != 1 || !wts[0].IsDirty {
		t.Errorf("expected worktree to be dirty after untracked file")
	}

	// A launched run's worktree under .bottown/runs, in its own repository
	// like the runs mirror, is listed as ephemeral; one whose worktree is
	// gone, or that belongs to another repository, is not.
	runsDir := filepath.Join(mainCheckout, ".bottown", "runs")
	ephDir := filepath.Join(runsDir, "r-11fe1234")
	runGit(t, filepath.Dir(mainCheckout), "clone", "-q", mainCheckout, ephDir)
	runGit(t, ephDir, "checkout", "-q", "-b", "issue/71")
	for _, r := range []runs.Record{
		{ID: "r-11fe1234", Kind: "implement", Actor: "claude", Repo: "bottown/bottown", Issue: 71, Status: runs.StatusSucceeded, Worktree: ephDir},
		{ID: "r-90ee", Kind: "verify", Actor: "kimi", Repo: "bottown/bottown", PR: 3, Status: runs.StatusSucceeded, Worktree: filepath.Join(runsDir, "r-90ee")},
		{ID: "r-07ae", Kind: "implement", Actor: "agy", Repo: "bottown/other", Issue: 1, Status: runs.StatusSucceeded, Worktree: ephDir},
	} {
		if err := (&runs.Workspace{RunsDir: runsDir}).Save(r); err != nil {
			t.Fatal(err)
		}
	}
	wts = srv.computeWorktrees(context.Background())
	if len(wts) != 2 {
		t.Fatalf("expected the actor worktree and one ephemeral run worktree, got %+v", wts)
	}
	if wts[0].Ephemeral || wts[0].ActiveRun != "r-98765" {
		t.Errorf("permanent worktree changed: %+v", wts[0])
	}
	eph := wts[1]
	if !eph.Ephemeral || eph.Actor != "claude" || eph.Path != ephDir || eph.Head != "issue/71" ||
		eph.Kind != "implement" || eph.Target != "#71" || eph.ActiveRun != "r-11fe1234" || eph.LastRun != "r-11fe1234" {
		t.Errorf("unexpected ephemeral worktree: %+v", eph)
	}
}
