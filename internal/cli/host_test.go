package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerEmail(t *testing.T) {
	for _, c := range []struct{ env, git, want string }{
		{"", "me@example.com", "me@example.com"},
		{"", "", ""},
		{"", "claude@bottown.local", ""},
		{"", "Kimi@Bottown.Local", ""},
		{"owner@e2e.test", "claude@bottown.local", "owner@e2e.test"},
		{"owner@e2e.test", "me@example.com", "owner@e2e.test"},
	} {
		if got := ownerEmail(c.env, c.git); got != c.want {
			t.Errorf("ownerEmail(%q, %q) = %q, want %q", c.env, c.git, got, c.want)
		}
	}
}

func TestSameImageSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	src := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", src, "-c", "user.name=t", "-c", "user.email=t@x", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	n := 0
	commit := func(file string) string {
		t.Helper()
		n++
		p := filepath.Join(src, file)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(strings.Repeat("x", n)), 0o644)
		git("add", ".")
		git("commit", "-qm", file)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module "+modulePath+"\n"), 0o644)
	base := commit("main.go")
	docs := commit("docs/index.md")
	commit("README.md")
	readmes := commit("docker/README.md")
	prompt := commit("internal/runs/prompts/implement.md")
	code := commit("main.go")

	img := func(sha string) string { return "bottown:" + sha[:12] }
	for _, c := range []struct {
		image, ref string
		want       bool
	}{
		{img(base), base, true},
		{img(base), docs, true},
		{img(base), readmes, true},
		{img(base), prompt, false}, // embedded in the binary
		{img(readmes), code, false},
		{img(code), code, true},
		{img(code), "HEAD", true},
		{"bottown:prod-wt", code, false},
		{"bottown:main", code, false},
		{"other:" + code[:12], code, false},
		{"", code, false},
		{img(base), "nosuchref", false},
	} {
		if got := sameImageSource(src, c.image, c.ref); got != c.want {
			t.Errorf("sameImageSource(%s, %.12s) = %v, want %v", c.image, c.ref, got, c.want)
		}
	}
}
