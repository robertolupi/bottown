package policy

import "testing"

func TestRunScopeAPI(t *testing.T) {
	impl := RunScope{Org: "bottown", Repo: "demo", Role: Implement}
	ver := RunScope{Org: "bottown", Repo: "demo", Role: Verify}
	dis := RunScope{Org: "bottown", Repo: "demo", Role: Discuss}
	plan := RunScope{Org: "bottown", Repo: "demo", Role: Plan}
	cases := []struct {
		s            RunScope
		method, path string
		allow        bool
		rule         string
	}{
		{impl, "GET", "/repos/bottown/demo/issues/1", true, "run-read"},
		{impl, "GET", "/repos/bottown/other/issues/1", false, "run-repo"},
		{impl, "POST", "/repos/bottown/other/pulls", false, "run-repo"},
		{impl, "GET", "/user", true, "run-read"},
		{impl, "POST", "/user/repos", false, "run-repo"},
		{impl, "GET", "/repos/bottown", false, "run-repo"},
		{impl, "POST", "/repos/bottown/demo/pulls", true, "run-implement"},
		{impl, "POST", "/repos/bottown/demo/issues/1/comments", true, "run-implement"},
		{impl, "POST", "/repos/bottown/demo/pulls/2/reviews", false, "role-implement-review"},
		{impl, "POST", "/repos/bottown/demo/pulls/2/reviews/5/comments", false, "role-implement-review"},
		{ver, "GET", "/repos/bottown/demo/pulls/2", true, "run-read"},
		{ver, "POST", "/repos/bottown/demo/pulls/2/reviews", false, "review-via-tool"},
		{ver, "POST", "/repos/bottown/demo/issues/2/comments", true, "run-verify-comment"},
		{ver, "PATCH", "/repos/bottown/demo/issues/comments/9", true, "run-verify-comment-edit"},
		{ver, "POST", "/repos/bottown/demo/pulls", false, "role-verify"},
		{ver, "PATCH", "/repos/bottown/demo/pulls/2", false, "role-verify"},
		{ver, "POST", "/repos/bottown/demo/issues", false, "role-verify"},
		{ver, "POST", "/repos/bottown/demo/contents/README", false, "role-verify"},
		{ver, "POST", "/repos/bottown/other/pulls/2/reviews", false, "run-repo"},
		{ver, "POST", "/repos/BOTTOWN/DEMO/issues/2/comments", true, "run-verify-comment"},
		// Discuss runs read and comment, nothing else.
		{dis, "GET", "/repos/bottown/demo/issues/1", true, "run-read"},
		{dis, "POST", "/repos/bottown/demo/issues/1/comments", true, "run-discuss-comment"},
		{dis, "PATCH", "/repos/bottown/demo/issues/comments/9", true, "run-discuss-comment-edit"},
		{dis, "POST", "/repos/bottown/demo/pulls", false, "role-discuss"},
		{dis, "PATCH", "/repos/bottown/demo/issues/1", false, "role-discuss"},
		{dis, "POST", "/repos/bottown/demo/pulls/2/reviews", false, "review-via-tool"},
		{dis, "POST", "/repos/bottown/demo/contents/README", false, "role-discuss"},
		{dis, "POST", "/repos/bottown/other/issues/1/comments", false, "run-repo"},
		// Plan runs are implement runs that may not file issues.
		{plan, "GET", "/repos/bottown/demo/issues/1", true, "run-read"},
		{plan, "POST", "/repos/bottown/demo/issues", false, "role-plan-no-issues"},
		{plan, "POST", "/repos/bottown/demo/issues/1/comments", true, "run-plan"},
		{plan, "PATCH", "/repos/bottown/demo/issues/comments/9", true, "run-plan"},
		{plan, "POST", "/repos/bottown/demo/pulls", true, "run-plan"},
		{plan, "POST", "/repos/bottown/demo/pulls/2/reviews", false, "role-implement-review"},
		{plan, "POST", "/repos/bottown/demo/pulls/2/requested_reviewers", false, "role-implement-review-requests"},
		{plan, "POST", "/repos/bottown/other/pulls", false, "run-repo"},
		{impl, "POST", "/repos/bottown/demo/issues", true, "run-implement"},
	}
	for _, c := range cases {
		d := c.s.DecideAPI(c.method, c.path)
		if d.Allow != c.allow || d.Rule != c.rule {
			t.Errorf("%s %s %s = %+v, want allow=%v rule=%s", c.s.Role, c.method, c.path, d, c.allow, c.rule)
		}
	}
}

func TestRunScopeGit(t *testing.T) {
	impl := RunScope{Org: "bottown", Repo: "demo", Role: Implement}
	ver := RunScope{Org: "bottown", Repo: "demo", Role: Verify}
	dis := RunScope{Org: "bottown", Repo: "demo", Role: Discuss}
	plan := RunScope{Org: "bottown", Repo: "demo", Role: Plan}
	cases := []struct {
		s             RunScope
		path, service string
		allow         bool
	}{
		{plan, "/bottown/demo.git/info/refs", "git-upload-pack", true},
		{plan, "/bottown/demo.git/info/refs", "git-receive-pack", true},
		{plan, "/bottown/demo.git/git-receive-pack", "", true},
		{plan, "/bottown/other.git/git-receive-pack", "", false},
		{impl, "/bottown/demo.git/info/refs", "git-upload-pack", true},
		{impl, "/bottown/demo.git/info/refs", "git-receive-pack", true},
		{impl, "/bottown/demo.git/git-receive-pack", "", true},
		{impl, "/bottown/other.git/info/refs", "git-upload-pack", false},
		{ver, "/bottown/demo.git/info/refs", "git-upload-pack", true},
		{ver, "/bottown/demo.git/git-upload-pack", "", true},
		{ver, "/bottown/demo.git/info/refs", "git-receive-pack", false},
		{ver, "/bottown/demo.git/git-receive-pack", "", false},
		{dis, "/bottown/demo.git/info/refs", "git-upload-pack", true},
		{dis, "/bottown/demo.git/info/refs", "git-receive-pack", false},
		{dis, "/bottown/demo.git/git-receive-pack", "", false},
	}
	for _, c := range cases {
		if d := c.s.DecideGit(c.path, c.service); d.Allow != c.allow {
			t.Errorf("%s %s?service=%s = %+v, want allow=%v", c.s.Role, c.path, c.service, d, c.allow)
		}
	}
}
