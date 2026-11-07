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
			for _, role := range []string{Implement, Verify, Discuss, Plan, Retro} {
				s := RunScope{Org: "bottown", Repo: "demo", Role: role}
				if d := s.DecideAPI(m, path); d.Allow || d.Rule != "wiki-via-store" {
					t.Errorf("%s run %s %s = %+v, want wiki-via-store", role, m, path, d)
				}
			}
		}
	}
	for _, role := range []string{Implement, Verify, Discuss, Plan, Retro} {
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
		{Retro, false, "Idea.md", true, "wiki-read"},
		{Retro, true, "Idea.md", false, "wiki-read"},
		{"garden", false, "Idea.md", false, "wiki-default"},
	}
	for _, c := range cases {
		if d := DecideWiki(c.who, c.write, c.path); d.Allow != c.allow || d.Rule != c.rule {
			t.Errorf("DecideWiki(%s, write=%v, %s) = %+v, want allow=%v rule=%s", c.who, c.write, c.path, d, c.allow, c.rule)
		}
	}
}

func TestDecideWikiRound(t *testing.T) {
	// Round 1 of 315-x is consolidated; round 2 is open.
	closed := func(debate string, n int) bool { return debate == "315-x" && n == 1 }
	claude2 := &WikiRound{Debate: "315-x", Round: 2, Actor: "claude"}
	raw2 := &WikiRound{Debate: "315-x", Round: 2, Actor: "claude", Raw: true}
	cons2 := &WikiRound{Debate: "315-x", Round: 2, Actor: "agy", Consolidate: true}
	cases := []struct {
		who   string
		in    *WikiRound
		write bool
		path  string
		allow bool
		rule  string
	}{
		{WikiOwner, nil, true, "reviews/315-x/r2-claude.md", true, "wiki-owner"},
		{WikiOwner, nil, false, "reviews/315-x/r2-codex.md", true, "wiki-owner"},
		// A discuss run writes its own answer, once, and nothing else.
		{Discuss, claude2, true, "reviews/315-x/r2-claude.md", true, "wiki-round-answer"},
		{Discuss, claude2, true, "reviews/315-x/r2-codex.md", false, "wiki-round-write"},
		{Discuss, claude2, true, "reviews/315-x/r1-claude.md", false, "wiki-round-write"},
		{Discuss, claude2, true, "reviews/315-x/r2-consolidated.md", false, "wiki-round-write"},
		{Discuss, claude2, true, "reviews/316-y/r2-claude.md", false, "wiki-round-write"},
		// It reads its own answer and earlier consolidations, never another
		// actor's answer in its round.
		{Discuss, claude2, false, "reviews/315-x/r2-claude.md", true, "wiki-round-answer"},
		{Discuss, claude2, false, "reviews/315-x/r2-codex.md", false, "wiki-round-independent"},
		{Discuss, claude2, false, "reviews/315-x/r1-consolidated.md", true, "wiki-round-earlier"},
		{Discuss, claude2, false, "reviews/315-x/r1-codex.md", false, "wiki-round-independent"},
		{Discuss, raw2, false, "reviews/315-x/r1-codex.md", true, "wiki-round-earlier"},
		{Discuss, raw2, false, "reviews/315-x/r2-codex.md", false, "wiki-round-independent"},
		// The consolidate run reads the round and writes its consolidation.
		{Consolidate, cons2, false, "reviews/315-x/r2-codex.md", true, "wiki-round-consolidate"},
		{Consolidate, cons2, false, "reviews/315-x/r1-claude.md", true, "wiki-round-consolidate"},
		{Consolidate, cons2, true, "reviews/315-x/r2-consolidated.md", true, "wiki-round-consolidate"},
		{Consolidate, cons2, true, "reviews/315-x/r2-agy.md", false, "wiki-round-write"},
		// Everyone else reads a round once it is consolidated, and writes none.
		{WikiActor, nil, false, "reviews/315-x/r1-claude.md", true, "wiki-round-closed"},
		{WikiActor, nil, false, "reviews/315-x/r2-claude.md", false, "wiki-round-open"},
		{Implement, nil, true, "reviews/315-x/r3-claude.md", false, "wiki-round-write"},
		{Discuss, nil, false, "reviews/315-x/r2-claude.md", false, "wiki-round-open"},
	}
	for _, c := range cases {
		d, ok := DecideWikiRound(c.who, c.in, c.write, c.path, closed)
		if !ok || d.Allow != c.allow || d.Rule != c.rule {
			t.Errorf("DecideWikiRound(%s, %+v, write=%v, %s) = %+v, %v; want allow=%v rule=%s", c.who, c.in, c.write, c.path, d, ok, c.allow, c.rule)
		}
	}
	for _, p := range []string{"Home.md", "Review-315-x.md", "reviews/315-x/notes.md"} {
		if _, ok := DecideWikiRound(Discuss, claude2, false, p, closed); ok {
			t.Errorf("DecideWikiRound decided %s, which is not a round file", p)
		}
	}
}

// TestDebateQuiet: a debate round's discuss runs may not comment or react,
// which would break the round's independence; a consolidate run may.
func TestDebateQuiet(t *testing.T) {
	deb := RunScope{Org: "bottown", Repo: "demo", Role: Discuss, Debate: true}
	cons := RunScope{Org: "bottown", Repo: "demo", Role: Consolidate}
	for _, c := range []struct{ method, path string }{
		{"POST", "/repos/bottown/demo/issues/1/comments"},
		{"PATCH", "/repos/bottown/demo/issues/comments/9"},
	} {
		if d := deb.DecideAPI(c.method, c.path); d.Allow || d.Rule != DebateQuiet {
			t.Errorf("debate %s %s = %+v, want %s", c.method, c.path, d, DebateQuiet)
		}
		if d := cons.DecideAPI(c.method, c.path); !d.Allow {
			t.Errorf("consolidate %s %s = %+v, want allowed", c.method, c.path, d)
		}
	}
	if d := deb.DecideAPI("GET", "/repos/bottown/demo/issues/1"); !d.Allow {
		t.Errorf("debate read = %+v", d)
	}
	if d := cons.DecideGit("/bottown/demo.git/info/refs", "git-receive-pack"); d.Allow {
		t.Errorf("consolidate push = %+v", d)
	}
}
