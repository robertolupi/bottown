package policy

import "testing"

func TestDecide(t *testing.T) {
	p := Default("rlupi")
	cases := []struct {
		actor, method, path string
		allow               bool
		rule                string
	}{
		{"implementer", "GET", "/repos/bottown/x/issues", true, "default"},
		{"implementer", "POST", "/repos/bottown/x/pulls", true, "default"},
		{"implementer", "POST", "/repos/bottown/x/pulls/3/merge", false, "merge"},
		{"implementer", "POST", "/repos/bottown/x/statuses/abc123", false, "commit-status"},
		{"verifier", "POST", "/repos/bottown/x/pulls/3/reviews", true, "default"},
		{"verifier", "GET", "/repos/bottown/x/branch_protections", true, "default"},
		{"verifier", "POST", "/repos/bottown/x/branch_protections", false, "branch-protection"},
		{"implementer", "DELETE", "/repos/bottown/x/issues/4/labels/7", false, "issue-labels"},
		{"implementer", "POST", "/repos/bottown/x/issues/4/labels", false, "issue-labels"},
		{"implementer", "PATCH", "/repos/bottown/x", false, "repo-settings"},
		{"implementer", "GET", "/repos/bottown/x", true, "default"},
		{"implementer", "GET", "/admin/users", false, "admin"},
		{"implementer", "POST", "/REPOS/bottown/x/PULLS/3/MERGE", false, "merge"},
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
