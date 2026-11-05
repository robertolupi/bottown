package reviewtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/runs"
)

// PatchCase is a PR head moved from Old to Head in a repository whose
// base branch is BaseRef, and whether its patch stayed the same.
type PatchCase struct {
	Name    string
	GitDir  string
	BaseRef string
	Old     string
	Head    string
	Same    bool
}

// PatchCases builds, in temporary repositories, the head moves the
// carry-over rule must tell apart: a rebase of the same patch onto a newer
// main and a clean merge of main carry an approval over; any change to the
// PR's own diff, a change to its diff's context included, does not.
func PatchCases(t testing.TB) []PatchCase {
	t.Helper()
	lines := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			b.WriteString(strings.Repeat("x", i%7) + string(rune('a'+i%26)) + "\n")
		}
		return b.String()
	}
	setup := func(name string) *gitRepo {
		r := &gitRepo{t: t, dir: filepath.Join(t.TempDir(), name)}
		r.run("init", "-q", "-b", "main", r.dir)
		r.write("a.txt", lines(30))
		r.write("b.txt", lines(30))
		r.commit("base")
		r.run("checkout", "-q", "-b", "issue/1")
		r.edit("a.txt", 15, "the PR's line")
		r.commit("pr")
		return r
	}
	moveMain := func(r *gitRepo, file string, line int) {
		r.run("checkout", "-q", "main")
		r.edit(file, line, "main's line")
		r.commit("main moves")
		r.run("checkout", "-q", "issue/1")
	}
	var out []PatchCase
	add := func(name string, r *gitRepo, old string, same bool) {
		out = append(out, PatchCase{Name: name, GitDir: filepath.Join(r.dir, ".git"), BaseRef: "refs/heads/main", Old: old, Head: r.head(), Same: same})
	}

	r := setup("rebase")
	old := r.head()
	moveMain(r, "b.txt", 5)
	r.run("rebase", "-q", "main")
	add("rebase onto a newer main", r, old, true)

	r = setup("rebase-same-file")
	old = r.head()
	moveMain(r, "a.txt", 2)
	r.run("rebase", "-q", "main")
	add("rebase onto main that changed the same file", r, old, true)

	r = setup("merge")
	old = r.head()
	moveMain(r, "b.txt", 5)
	r.run("merge", "-q", "--no-edit", "main")
	add("clean merge of main", r, old, true)

	r = setup("amend")
	old = r.head()
	r.edit("a.txt", 16, "one more line")
	r.run("commit", "-q", "-a", "--amend", "--no-edit")
	add("amended diff", r, old, false)

	r = setup("rebase-and-change")
	old = r.head()
	moveMain(r, "b.txt", 5)
	r.run("rebase", "-q", "main")
	r.edit("a.txt", 20, "a follow-up")
	r.commit("follow-up")
	add("rebase with a new commit", r, old, false)

	r = setup("context")
	old = r.head()
	moveMain(r, "a.txt", 12)
	r.run("rebase", "-q", "main")
	add("rebase onto main that changed the diff's context", r, old, false)
	return out
}

type gitRepo struct {
	t   testing.TB
	dir string
}

func (r *gitRepo) run(args ...string) string {
	r.t.Helper()
	dir := r.dir
	if args[0] == "init" {
		dir = ""
	}
	cmd := runs.GitCommand(r.t.Context(), "", nil, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *gitRepo) write(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// edit replaces line n (1-based) of name.
func (r *gitRepo) edit(name string, n int, text string) {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		r.t.Fatal(err)
	}
	ls := strings.Split(string(b), "\n")
	ls[n-1] = text
	r.write(name, strings.Join(ls, "\n"))
}

func (r *gitRepo) commit(msg string) {
	r.t.Helper()
	r.run("add", "-A")
	r.run("commit", "-q", "-m", msg)
}

func (r *gitRepo) head() string { return r.run("rev-parse", "HEAD") }
