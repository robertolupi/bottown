package webui

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

const mainSHA = "1111111111111111111111111111111111111111"

// fakeSettingsForge serves the branch and file reads the settings loader
// makes. files maps a ref to the .bottown.toml at that ref; a ref
// without an entry has no file. main's head is mainSHA.
type fakeSettingsForge struct {
	mu    sync.Mutex
	files map[string]string
	refs  []string // refs the raw endpoint was asked for
}

func (f *fakeSettingsForge) set(ref, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if content == "" {
		delete(f.files, ref)
	} else {
		f.files[ref] = content
	}
}

func (f *fakeSettingsForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api := fakeAPI{
		branches: map[string]serverapi.BranchOut{"main": {Name: "main", Commit: mainSHA, Protected: true}},
		files:    f.files, fileRefs: &f.refs,
	}
	if !api.serve(w, r) {
		http.NotFound(w, r)
	}
}

func newSettingsServer(t *testing.T) (*Server, *fakeSettingsForge, string) {
	t.Helper()
	mainCheckout, _, _ := setupThrowawayRepos(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	forge := &fakeSettingsForge{files: map[string]string{}}
	ts := httptest.NewUnstartedServer(forge)
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)

	srv, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: ln.Addr().(*net.TCPAddr).Port},
		RepoTop:  mainCheckout,
		FullRepo: "bottown/demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, forge, mainCheckout
}

func TestRepoSettings_ReadFromMainOnly(t *testing.T) {
	srv, forge, checkout := newSettingsServer(t)
	ctx := t.Context()

	// main declares one step; a PR branch, and the checkout's working tree
	// on that branch, declare another.
	forge.set(mainSHA, "after_pull = [\"echo main > from-main.txt\"]\n")
	forge.set("issue/7", "after_pull = [\"echo branch > from-branch.txt\"]\n")
	runGit(t, checkout, "checkout", "-q", "-b", "issue/7")
	if err := os.WriteFile(filepath.Join(checkout, ".bottown.toml"), []byte("after_pull = [\"echo local > from-local.txt\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := srv.LoadRepoSettings(ctx)
	if st.Error != "" || !st.Checked || !st.Found {
		t.Fatalf("settings = %+v", st)
	}
	if st.Branch != "main" || st.Commit != mainSHA {
		t.Errorf("read at %s %s, want main %s", st.Branch, st.Commit, mainSHA)
	}
	if !slices.Equal(st.AfterPull, []string{"echo main > from-main.txt"}) {
		t.Errorf("after_pull = %q, want main's", st.AfterPull)
	}
	for _, ref := range forge.refs {
		if ref != mainSHA {
			t.Errorf("raw file read at ref %q, want only main's head %s", ref, mainSHA)
		}
	}

	// The health checklist carries it.
	full, err := srv.ComputeStatus(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !full.Health.RepoSettings.Found || !slices.Equal(full.Health.RepoSettings.AfterPull, st.AfterPull) {
		t.Errorf("health repo_settings = %+v", full.Health.RepoSettings)
	}

}
func TestRepoSettings_NoFileAndInvalid(t *testing.T) {
	srv, forge, _ := newSettingsServer(t)
	ctx := t.Context()

	// No file on main: the defaults, even though a branch has one.
	forge.set("issue/7", "after_pull = [\"make install\"]\n")
	st := srv.LoadRepoSettings(ctx)
	if st.Error != "" || !st.Checked || st.Found || len(st.AfterPull) != 0 || st.AfterPull == nil || st.MilestonesDir != "docs/milestones" {
		t.Fatalf("no file: %+v", st)
	}

	// An invalid file is reported and declares no steps.
	forge.set(mainSHA, "after-pull = [\"make install\"]\n")
	st = srv.LoadRepoSettings(ctx)
	if !st.Found || st.Error == "" || len(st.AfterPull) != 0 {
		t.Fatalf("invalid file: %+v", st)
	}

	// So is an invalid milestones_dir, which also withholds the steps.
	forge.set(mainSHA, "after_pull = [\"make install\"]\nmilestones_dir = \"../plans\"\n")
	st = srv.LoadRepoSettings(ctx)
	if !st.Found || !strings.Contains(st.Error, "milestones_dir") || len(st.AfterPull) != 0 || st.MilestonesDir != "" {
		t.Fatalf("invalid milestones_dir: %+v", st)
	}

	forge.set(mainSHA, "milestones_dir = \"plans\"\n")
	if st = srv.LoadRepoSettings(ctx); st.Error != "" || st.MilestonesDir != "plans" {
		t.Fatalf("custom milestones_dir: %+v", st)
	}
}
