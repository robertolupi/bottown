package cli

import (
	"github.com/robertolupi/bottown/internal/runs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPlan = "+++\ntitle = \"Web UI\"\nkickoff = 55\ndesign = \"docs/design/web-ui.md\"\n+++\n\n" +
	"## Status endpoint\n\n```issue\nkey = \"status\"\n```\n\nServe /api/status.\n\n" +
	"## Status view\n\n```issue\nkey = \"view\"\ndepends = [\"status\", \"#12\"]\n```\n\nShow it.\n"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runMilestone runs `bottown milestone ARGS` in dir.
func runMilestone(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)
	t.Setenv("BOTTOWN_INSTANCE", "")
	root := newRoot()
	var out, errb strings.Builder
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"milestone"}, args...))
	err = root.Execute()
	return out.String(), errb.String(), err
}

func TestMilestoneCheck(t *testing.T) {
	top := t.TempDir()
	if _, err := runs.Git(top, nil, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(top, ".bottown.toml"), "milestones_dir = \"plans\"\n")
	writeFile(t, filepath.Join(top, "plans", "web-ui.md"), testPlan)
	writeFile(t, filepath.Join(top, "docs", "design", "web-ui.md"), "# Design\n")
	writeFile(t, filepath.Join(top, "plans", "no-design.md"), strings.Replace(testPlan, "web-ui.md", "missing.md", 1))
	writeFile(t, filepath.Join(top, "plans", "broken.md"), strings.Replace(testPlan, "\"status\", ", "\"view\", ", 1))
	sub := filepath.Join(top, "docs")

	// By slug, from a subdirectory: <milestones_dir>/<slug>.md at the root.
	out, _, err := runMilestone(t, sub, "check", "web-ui")
	if err != nil || !strings.Contains(out, "plans/web-ui.md: ok, 2 issue(s)") {
		t.Fatalf("slug: %q, %v", out, err)
	}
	// By path, relative to the working directory.
	out, _, err = runMilestone(t, top, "check", "plans/web-ui.md")
	if err != nil || !strings.Contains(out, "plans/web-ui.md: ok") {
		t.Fatalf("path: %q, %v", out, err)
	}
	if out, _, err = runMilestone(t, filepath.Join(top, "plans"), "check", "web-ui.md"); err != nil {
		t.Fatalf("bare file name: %q, %v", out, err)
	}

	// Inside the checkout, design must exist.
	_, errOut, err := runMilestone(t, top, "check", "no-design")
	if err == nil || !strings.Contains(errOut, `plans/no-design.md:4: front matter: design "docs/design/missing.md" is not in the checkout`) {
		t.Fatalf("missing design: %q, %v", errOut, err)
	}
	// ... and be a file, not a directory.
	writeFile(t, filepath.Join(top, "plans", "dir-design.md"), strings.Replace(testPlan, "docs/design/web-ui.md", "docs/design", 1))
	_, errOut, err = runMilestone(t, top, "check", "dir-design")
	if err == nil || !strings.Contains(errOut, `plans/dir-design.md:4: front matter: design "docs/design" is not a file`) {
		t.Fatalf("directory design: %q, %v", errOut, err)
	}
	// Diagnostics go to stderr, and the command fails.
	_, errOut, err = runMilestone(t, top, "check", "broken")
	if err == nil || !strings.Contains(err.Error(), "plans/broken.md: 1 problem(s)") ||
		!strings.Contains(errOut, "plans/broken.md:15: view: depends on itself") {
		t.Fatalf("broken: %q, %v", errOut, err)
	}
	if _, _, err = runMilestone(t, top, "check", "Not_A_Slug"); err == nil || !strings.Contains(err.Error(), "plan slug") {
		t.Fatalf("bad slug: %v", err)
	}
	if _, _, err = runMilestone(t, top, "check", "absent"); err == nil {
		t.Fatalf("absent plan: no error")
	}

	// An invalid .bottown.toml is reported, not replaced by the default.
	writeFile(t, filepath.Join(top, ".bottown.toml"), "milestones_dir = \"../plans\"\n")
	if _, _, err = runMilestone(t, top, "check", "web-ui"); err == nil || !strings.Contains(err.Error(), "milestones_dir") {
		t.Fatalf("invalid settings: %v", err)
	}

	// Outside a checkout, a path still works, without the design check; a
	// slug does not.
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "no-design.md"), strings.Replace(testPlan, "web-ui.md", "missing.md", 1))
	if out, _, err = runMilestone(t, outside, "check", "./no-design.md"); err != nil {
		t.Fatalf("outside a checkout: %q, %v", out, err)
	}
	if _, _, err = runMilestone(t, outside, "check", "no-design"); err == nil {
		t.Fatalf("slug outside a checkout: no error")
	}
}
