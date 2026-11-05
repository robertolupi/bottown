package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/runs"
)

// guideFam is a fam: main, hosted on instance dev, with actor worktrees
// claude (harness claude) and agy (harness antigravity).
func guideFam(t *testing.T) (fam string) {
	t.Helper()
	fam = t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", fam)
	main := filepath.Join(fam, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		if _, err := runs.Git(dir, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	git(main, "init", "-q", "-b", "main")
	git(main, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	git(main, "config", "extensions.worktreeConfig", "true")
	git(main, "config", gitInstanceKey, "dev")
	git(main, "remote", "add", "bottown-dev", "http://127.0.0.1:3041/git/bottown/demo.git")
	for _, a := range [][2]string{{"claude", "claude"}, {"agy", "antigravity"}} {
		wt := filepath.Join(fam, a[0])
		git(main, "worktree", "add", "-q", "--detach", wt)
		git(wt, "config", "--worktree", gitActorKey, a[0])
		git(wt, "config", "--worktree", "bottown.harness", a[1])
	}
	return fam
}

func TestFamGuide(t *testing.T) {
	fam := guideFam(t)
	real, _ := filepath.EvalSymlinks(fam)
	d, harness, err := famGuide(filepath.Join(fam, "agy"))
	if err != nil {
		t.Fatal(err)
	}
	if harness != "antigravity" || d.Actor != "agy" || d.Remote != "bottown-dev" || d.Repo != "bottown/demo" || d.Bottown != selfPath() {
		t.Errorf("guide data: %+v, harness %s", d, harness)
	}
	if d.Main != filepath.Join(real, "main") || len(d.Actors) != 2 {
		t.Errorf("layout: %s %+v", d.Main, d.Actors)
	}
	// The owner's checkout finds itself, as no actor's.
	if d, harness, err := famGuide(filepath.Join(fam, "main")); err != nil || d.Actor != "" || harness != "" {
		t.Errorf("main: %+v %q %v", d, harness, err)
	}
	if m, err := famMain(filepath.Join(fam, "main")); err != nil || m != filepath.Join(real, "main") {
		t.Errorf("famMain: %s %v", m, err)
	}
}

func TestRefreshGuides(t *testing.T) {
	fam := guideFam(t)
	var out, warn bytes.Buffer
	refreshGuides(&out, &warn, filepath.Join(fam, "main"))
	if warn.Len() > 0 {
		t.Errorf("warnings: %s", warn.String())
	}
	for _, f := range []string{"claude/CLAUDE.local.md", "agy/.agents/rules/bottown.md"} {
		b, err := os.ReadFile(filepath.Join(fam, f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), runs.GuideMarker) || !strings.Contains(string(b), "actor worktree for agy") || !strings.Contains(string(b), "actor worktree for claude") {
			t.Errorf("%s:\n%s", f, b)
		}
	}
	if s := guideStatus(filepath.Join(fam, "claude")); s != runs.GuideCurrent {
		t.Errorf("claude guide: %s", s)
	}
	if s := guideStatus(filepath.Join(fam, "main")); s != "" {
		t.Errorf("main is no actor worktree, but has guide state %q", s)
	}
	for _, d := range []string{"main", "claude", "agy"} {
		if st, _ := runs.Git(filepath.Join(fam, d), nil, "status", "--porcelain"); st != "" {
			t.Errorf("%s not clean: %q", d, st)
		}
	}
}

func TestCommitAgentsPointer(t *testing.T) {
	for name, old := range map[string]string{"missing": "", "existing": "# Demo\n\nBuild with make.", "trailing newline": "# Demo\n"} {
		t.Run(name, func(t *testing.T) {
			top := t.TempDir()
			t.Setenv("GIT_CEILING_DIRECTORIES", top)
			if _, err := runs.Git(top, nil, "init", "-q", "-b", "main"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_AUTHOR_NAME", "owner")
			t.Setenv("GIT_AUTHOR_EMAIL", "owner@example.com")
			t.Setenv("GIT_COMMITTER_NAME", "owner")
			t.Setenv("GIT_COMMITTER_EMAIL", "owner@example.com")
			if old != "" {
				os.WriteFile(filepath.Join(top, "AGENTS.md"), []byte(old), 0o644)
				runs.Git(top, nil, "add", "AGENTS.md")
				if _, err := runs.Git(top, nil, "commit", "-q", "-m", "agents"); err != nil {
					t.Fatal(err)
				}
			}
			if done, err := commitAgentsPointer(top); err != nil || !done {
				t.Fatalf("commitAgentsPointer: %v %v", done, err)
			}
			b, _ := os.ReadFile(filepath.Join(top, "AGENTS.md"))
			want := agentsPointer
			if old != "" {
				want = strings.TrimRight(old, "\n") + "\n\n" + agentsPointer
			}
			if string(b) != want {
				t.Errorf("AGENTS.md:\n%q\nwant\n%q", b, want)
			}
			if st, _ := runs.Git(top, nil, "status", "--porcelain"); st != "" {
				t.Errorf("not committed: %q", st)
			}
			// Once is enough.
			if done, err := commitAgentsPointer(top); err != nil || done {
				t.Errorf("second commitAgentsPointer: %v %v", done, err)
			}
		})
	}
}
