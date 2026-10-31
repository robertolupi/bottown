package policy

import "testing"

func TestDecide(t *testing.T) {
	p := Default("rlupi")
	cases := []struct {
		actor, method, path string
		allow               bool
		rule                string
	}{
		{"claude", "GET", "/repos/bottown/x/issues", true, "default"},
		{"claude", "POST", "/repos/bottown/x/pulls", true, "default"},
		{"claude", "POST", "/repos/bottown/x/pulls/3/merge", false, "merge"},
		{"claude", "POST", "/repos/bottown/x/statuses/abc123", false, "commit-status"},
		{"codex", "POST", "/repos/bottown/x/pulls/3/reviews", true, "default"},
		{"codex", "GET", "/repos/bottown/x/branch_protections", true, "default"},
		{"codex", "POST", "/repos/bottown/x/branch_protections", false, "branch-protection"},
		{"claude", "DELETE", "/repos/bottown/x/issues/4/labels/7", false, "issue-labels"},
		{"claude", "POST", "/repos/bottown/x/issues/4/labels", false, "issue-labels"},
		{"claude", "PATCH", "/repos/bottown/x", false, "repo-settings"},
		{"claude", "GET", "/repos/bottown/x", true, "default"},
		{"claude", "GET", "/admin/users", false, "admin"},
		{"claude", "POST", "/REPOS/bottown/x/PULLS/3/MERGE", false, "merge"},
		{"rlupi", "POST", "/repos/bottown/x/pulls/3/merge", true, "exempt"},
	}
	for _, c := range cases {
		d := p.Decide(c.actor, c.method, c.path)
		if d.Allow != c.allow || d.Rule != c.rule {
			t.Errorf("%s %s %s = %+v, want allow=%v rule=%s", c.actor, c.method, c.path, d, c.allow, c.rule)
		}
	}
}

func TestClean(t *testing.T) {
	for in, ok := range map[string]bool{
		"/repos/a/b":          true,
		"/repos/a/b/":         true,
		"/repos/a/../b/merge": false,
		"/repos//a/b":         false,
		"/repos/./a":          false,
	} {
		if _, got := Clean(in); got != ok {
			t.Errorf("Clean(%q) ok=%v, want %v", in, got, ok)
		}
	}
}
