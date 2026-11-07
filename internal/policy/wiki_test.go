package policy

import "testing"

// TestWikiOnlyThroughStore: no actor or run role reaches Forgejo's wiki API
// or the wiki's git repository; the owner, outside a run, still does.
func TestWikiOnlyThroughStore(t *testing.T) {
	p := Default("rlupi")
	for _, m := range []string{"GET", "POST", "PATCH", "DELETE"} {
		for _, path := range []string{"/repos/bottown/demo/wiki/new", "/repos/bottown/demo/wiki/page/Home", "/repos/bottown/demo/wiki/pages", "/repos/bottown/demo/wiki/revisions/Home"} {
			if d := p.Decide("claude", m, path); d.Allow || d.Rule != "wiki-via-store" {
				t.Errorf("actor %s %s = %+v, want wiki-via-store", m, path, d)
			}
			if d := p.Decide("rlupi", m, path); !d.Allow {
				t.Errorf("owner %s %s = %+v, want allowed", m, path, d)
			}
			for _, role := range []string{Implement, Verify, Discuss, Plan} {
				s := RunScope{Org: "bottown", Repo: "demo", Role: role}
				if d := s.DecideAPI(m, path); d.Allow || d.Rule != "wiki-via-store" {
					t.Errorf("%s run %s %s = %+v, want wiki-via-store", role, m, path, d)
				}
			}
		}
	}
	for _, role := range []string{Implement, Verify, Discuss, Plan} {
		s := RunScope{Org: "bottown", Repo: "demo", Role: role}
		for _, c := range []struct{ path, service string }{
			{"/bottown/demo.wiki.git/info/refs", "git-upload-pack"},
			{"/bottown/demo.wiki.git/info/refs", "git-receive-pack"},
			{"/bottown/demo.wiki.git/git-upload-pack", ""},
			{"/bottown/demo.wiki.git/git-receive-pack", ""},
			{"/bottown/DEMO.WIKI/info/refs", "git-upload-pack"},
		} {
			if d := s.DecideGit(c.path, c.service); d.Allow || d.Rule != "wiki-via-store" {
				t.Errorf("%s run git %s?service=%s = %+v, want wiki-via-store", role, c.path, c.service, d)
			}
		}
	}
	if IsWikiGit("/bottown/demo.git/info/refs") || !IsWikiGit("/bottown/demo.wiki.git/info/refs") {
		t.Error("IsWikiGit")
	}
}

func TestDecideWiki(t *testing.T) {
	cases := []struct {
		who   string
		write bool
		path  string
		allow bool
		rule  string
	}{
		{WikiOwner, true, "Home.md", true, "wiki-owner"},
		{WikiOwner, false, "reviews/1-x/r1-claude.md", true, "wiki-owner"},
		{WikiActor, false, "Home.md", true, "wiki-read"},
		{WikiActor, true, "Home.md", false, "wiki-read"},
		{Implement, false, "Home.md", true, "wiki-read"},
		{Implement, true, "Home.md", false, "wiki-read"},
		{Verify, true, "Idea.md", false, "wiki-read"},
		{Discuss, true, "reviews/1-x/r1-claude.md", false, "wiki-read"},
		{Plan, false, "Idea.md", true, "wiki-read"},
		{"garden", false, "Idea.md", false, "wiki-default"},
	}
	for _, c := range cases {
		if d := DecideWiki(c.who, c.write, c.path); d.Allow != c.allow || d.Rule != c.rule {
			t.Errorf("DecideWiki(%s, write=%v, %s) = %+v, want allow=%v rule=%s", c.who, c.write, c.path, d, c.allow, c.rule)
		}
	}
}
