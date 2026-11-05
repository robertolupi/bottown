package webui

import (
	"bytes"
	"encoding/json"
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

	// Steps from the branch or the checkout are refused, and nothing runs.
	for _, steps := range [][]string{
		{"echo branch > from-branch.txt"},
		{"echo local > from-local.txt"},
		{"echo main > from-main.txt", "echo branch > from-branch.txt"},
	} {
		if _, err := srv.ExecuteAfterPull(ctx, steps); err == nil || !strings.Contains(err.Error(), "changed") {
			t.Errorf("ExecuteAfterPull(%q) err = %v, want refusal", steps, err)
		}
	}
	for _, f := range []string{"from-branch.txt", "from-local.txt", "from-main.txt"} {
		if fileExists(filepath.Join(checkout, f)) {
			t.Errorf("%s was created by a refused run", f)
		}
	}

	// main's steps, as confirmed, run in the checkout.
	out, err := srv.ExecuteAfterPull(ctx, st.AfterPull)
	if err != nil {
		t.Fatalf("ExecuteAfterPull: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(checkout, "from-main.txt")); err != nil || string(b) != "main\n" {
		t.Errorf("from-main.txt = %q, %v", b, err)
	}
	if !strings.Contains(out, "$ echo main > from-main.txt") {
		t.Errorf("output does not show the step:\n%s", out)
	}

	// main changes after the owner was shown the steps: refused.
	forge.set(mainSHA, "after_pull = [\"echo new > from-new.txt\"]\n")
	if _, err := srv.ExecuteAfterPull(ctx, st.AfterPull); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("stale steps: err = %v, want refusal", err)
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
	if _, err := srv.ExecuteAfterPull(ctx, []string{"make install"}); err == nil || !strings.Contains(err.Error(), "no after-pull steps") {
		t.Errorf("no file: err = %v", err)
	}

	// An invalid file is reported and declares no steps.
	forge.set(mainSHA, "after-pull = [\"make install\"]\n")
	st = srv.LoadRepoSettings(ctx)
	if !st.Found || st.Error == "" || len(st.AfterPull) != 0 {
		t.Fatalf("invalid file: %+v", st)
	}
	if _, err := srv.ExecuteAfterPull(ctx, []string{"make install"}); err == nil {
		t.Errorf("invalid file: steps ran")
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

func TestRepoSettings_AfterPullHandler(t *testing.T) {
	srv, forge, checkout := newSettingsServer(t)
	forge.set(mainSHA, "after_pull = [\"echo one\", \"false\", \"echo three > three.txt\"]\n")

	post := func(body string) (int, ActionResult) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/actions/after-pull", bytes.NewBufferString(body))
		srv.handleActionAfterPull(rec, req)
		var res ActionResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		return rec.Code, res
	}

	if code, _ := post(`{}`); code != http.StatusBadRequest {
		t.Errorf("no steps: status %d", code)
	}
	if code, _ := post(`not json`); code != http.StatusBadRequest {
		t.Errorf("bad JSON: status %d", code)
	}

	// A failing step stops the run, and its output so far is returned.
	code, res := post(`{"steps": ["echo one", "false", "echo three > three.txt"]}`)
	if code != http.StatusInternalServerError || res.Success || !strings.Contains(res.Error, "false") {
		t.Errorf("failing step: %d %+v", code, res)
	}
	if !strings.Contains(res.Output, "one") {
		t.Errorf("output lost: %q", res.Output)
	}
	if fileExists(filepath.Join(checkout, "three.txt")) {
		t.Errorf("a step after the failure ran")
	}

	forge.set(mainSHA, "after_pull = [\"echo ok\"]\n")
	if code, res := post(`{"steps": ["echo ok"]}`); code != http.StatusOK || !res.Success || !strings.Contains(res.Output, "ok") {
		t.Errorf("ok: %d %+v", code, res)
	}
}
