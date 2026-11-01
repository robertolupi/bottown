package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
)

func TestPlanBranchGC(t *testing.T) {
	remote := map[string]string{"issue/1": "a1", "issue/2": "a2", "issue/3": "a3", "issue/4": "a4", "main": "m"}
	local := []string{"issue/4", "issue/1", "issue/5"}
	p := planBranchGC("main", remote, local, branchGuards{
		CheckedOut: map[string]string{"issue/2": "/fam/claude"},
		Runs:       map[string]string{"issue/3": "r-1"},
		PRs:        map[string]int64{"issue/4": 9},
	})
	if want := []string{"issue/1"}; !slices.Equal(p.Remote, want) {
		t.Errorf("remote = %v, want %v", p.Remote, want)
	}
	if p.SHAs["issue/1"] != "a1" {
		t.Errorf("lease for issue/1 = %q", p.SHAs["issue/1"])
	}
	if want := []string{"issue/1", "issue/5"}; !slices.Equal(p.Local, want) {
		t.Errorf("local = %v, want %v", p.Local, want)
	}
	// Each kept branch is reported once, even when also local; main is
	// never deleted nor reported.
	want := []keptBranch{
		{"issue/2", "checked out in /fam/claude"},
		{"issue/3", "live run r-1"},
		{"issue/4", "open pull request !9"},
	}
	if !slices.Equal(p.Kept, want) {
		t.Errorf("kept = %v, want %v", p.Kept, want)
	}
}

func TestLiveRunBranches(t *testing.T) {
	now := time.Now()
	ended := now.Add(-time.Minute)
	all := []secrets.Run{
		{ID: "r-live", Org: "bottown", Repo: "demo", Kind: "implement", Issue: 7, Expires: now.Add(time.Hour)},
		{ID: "r-br", Org: "bottown", Repo: "demo", Kind: "verify", Branch: "issue/8", Expires: now.Add(time.Hour)},
		{ID: "r-ended", Org: "bottown", Repo: "demo", Kind: "implement", Issue: 9, Expires: now.Add(time.Hour), Ended: &ended},
		{ID: "r-expired", Org: "bottown", Repo: "demo", Kind: "implement", Issue: 10, Expires: now.Add(-time.Hour)},
		{ID: "r-other", Org: "bottown", Repo: "other", Kind: "implement", Issue: 11, Expires: now.Add(time.Hour)},
		{ID: "r-discuss", Org: "bottown", Repo: "demo", Kind: "discuss", Issue: 12, Expires: now.Add(time.Hour)},
	}
	got := liveRunBranches(all, "bottown", "demo", now)
	want := map[string]string{"issue/7": "r-live", "issue/8": "r-br"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for b, id := range want {
		if got[b] != id {
			t.Errorf("%s = %q, want %q", b, got[b], id)
		}
	}
}

// TestMergedAndCheckedOutBranches builds a remote with merged and unmerged
// branches, and a worktree with one of them checked out.
func TestMergedAndCheckedOutBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(d string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", d, "-c", "user.name=t", "-c", "user.email=t@x", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(d, msg string) {
		t.Helper()
		os.WriteFile(filepath.Join(d, strings.ReplaceAll(msg, "/", "-")), []byte(msg), 0o644)
		run(d, "add", ".")
		run(d, "commit", "-qm", msg)
	}
	bare := filepath.Join(dir, "remote.git")
	top := filepath.Join(dir, "main")
	run(dir, "init", "-q", "--bare", "-b", "main", bare)
	run(dir, "clone", "-q", bare, top)
	commit(top, "init")
	run(top, "push", "-q", "origin", "main")

	for _, b := range []string{"issue/1", "issue/2", "issue/3"} {
		run(top, "switch", "-q", "-c", b, "main")
		commit(top, b)
		run(top, "push", "-q", "origin", b)
	}
	run(top, "switch", "-q", "main")
	run(top, "merge", "-q", "--no-ff", "-m", "merge 1", "issue/1")
	run(top, "merge", "-q", "--no-ff", "-m", "merge 2", "issue/2")
	run(top, "push", "-q", "origin", "main")
	run(top, "branch", "-q", "other", "issue/1") // merged, outside the pattern
	run(top, "fetch", "-q", "origin")
	wt := filepath.Join(dir, "claude")
	run(top, "worktree", "add", "-q", wt, "issue/2")

	into := "refs/remotes/origin/main"
	remote, err := mergedBranches(top, into, "refs/remotes/origin/", "issue/*")
	if err != nil {
		t.Fatal(err)
	}
	if len(remote) != 2 || remote["issue/1"] == "" || remote["issue/2"] == "" {
		t.Errorf("merged remote branches = %v, want issue/1 and issue/2", remote)
	}
	local, err := mergedBranches(top, into, "refs/heads/", "issue/*")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := local["issue/3"]; ok || len(local) != 2 {
		t.Errorf("merged local branches = %v, want issue/1 and issue/2", local)
	}
	co, err := checkedOutBranches(top)
	if err != nil {
		t.Fatal(err)
	}
	if real, _ := filepath.EvalSymlinks(co["issue/2"]); real != mustEval(t, wt) {
		t.Errorf("issue/2 checked out at %q, want %s", co["issue/2"], wt)
	}
	if co["main"] == "" {
		t.Errorf("main not reported as checked out: %v", co)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
