package wiki

import (
	"reflect"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"fixed text":                     "fixed-text",
		"`bottown flow` refactor":        "bottown-flow-refactor",
		"**Bold** and _italic_ words":    "bold-and-italic-words",
		"See [the docs](http://x/y) now": "see-the-docs-now",
		"one two three four five six":    "one-two-three-four-five",
		"Don't split: it's fine":         "dont-split-its-fine",
		"Independent multi-agent discussion rounds, consolidated in the wiki": "independent-multi-agent-discussion",
		"!!! ???": "",
		"":        "",
		"supercalifragilisticexpialidocious-and-then-some words": "supercalifragilisticexpialidocious-and",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
		if err := ValidSlug(Slug(in)); err != nil {
			t.Errorf("Slug(%q) is not a valid slug: %v", in, err)
		}
	}
}

func TestValidSlug(t *testing.T) {
	for _, s := range []string{"a", "a-b", "x1-2y"} {
		if err := ValidSlug(s); err != nil {
			t.Errorf("ValidSlug(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"A", "a--b", "-a", "a-", "a_b", "a/b", strings.Repeat("a", MaxSlug+1)} {
		if ValidSlug(s) == nil {
			t.Errorf("ValidSlug(%q) accepted", s)
		}
	}
}

func TestDebateNames(t *testing.T) {
	cases := []struct {
		d    Debate
		name string
	}{
		{Debate{Issue: 315, Slug: "multi-agent"}, "315-multi-agent"},
		{Debate{Issue: 315}, "315"},
		{Debate{Issue: 315, Date: "2026-11-07", Slug: "x"}, "2026-11-07-315-x"},
		{Debate{Issue: 7, Date: "2026-11-07"}, "2026-11-07-7"},
	}
	for _, c := range cases {
		if got := c.d.Name(); got != c.name {
			t.Errorf("%+v.Name() = %q, want %q", c.d, got, c.name)
		}
		back, ok := ParseDebate(c.name)
		if !ok || back != c.d {
			t.Errorf("ParseDebate(%q) = %+v, %v; want %+v", c.name, back, ok, c.d)
		}
	}
	d := Debate{Issue: 315, Slug: "x"}
	if d.Dir() != "reviews/315-x" || d.Page() != "Review-315-x.md" || d.RoundFile(2, "codex") != "reviews/315-x/r2-codex.md" {
		t.Errorf("paths: %s %s %s", d.Dir(), d.Page(), d.RoundFile(2, "codex"))
	}
	for _, bad := range []string{"", "x", "0", "315-", "315-A", "2026-13-40-315", "-315"} {
		if _, ok := ParseDebate(bad); ok {
			t.Errorf("ParseDebate(%q) accepted", bad)
		}
	}
}

func TestParseRound(t *testing.T) {
	r, ok := ParseRound("reviews/315-x/r12-claude.md")
	if !ok || r != (Round{Debate: "315-x", N: 12, Author: "claude"}) || r.File() != "reviews/315-x/r12-claude.md" {
		t.Errorf("ParseRound = %+v, %v", r, ok)
	}
	for _, bad := range []string{"reviews/315-x/r0-claude.md", "reviews/315-x/r1-.md", "reviews/315-x/notes.md", "reviews/x/r1-claude.md", "other/315-x/r1-claude.md", "reviews/315-x/sub/r1-claude.md", "reviews/315-x/r1-claude.txt"} {
		if _, ok := ParseRound(bad); ok {
			t.Errorf("ParseRound(%q) accepted", bad)
		}
	}
}

func TestStamp(t *testing.T) {
	f := Frontmatter{Round: 1, Actor: "claude", Model: "opus", Run: "r-1", Issue: 315, SHA: "abc", Date: "2026-11-07"}
	got := Stamp("---\nround: 9\nactor: mallory\n---\n\n# Answer\n", f)
	want := "---\nround: 1\nactor: claude\nmodel: opus\nrun: r-1\nissue: 315\nsha: abc\ndate: 2026-11-07\n---\n\n# Answer\n"
	if got != want {
		t.Errorf("Stamp =\n%s\nwant\n%s", got, want)
	}
	keys, body := SplitFrontmatter(got)
	if keys["round"] != "1" || keys["actor"] != "claude" || body != "\n# Answer\n" {
		t.Errorf("SplitFrontmatter = %v, %q", keys, body)
	}
	if got := Stamp("plain\n", Frontmatter{Round: 2, Actor: "codex"}); got != "---\nround: 2\nactor: codex\n---\n\nplain\n" {
		t.Errorf("Stamp without frontmatter = %q", got)
	}
	if keys, body := SplitFrontmatter("---\nnot closed\n"); len(keys) != 0 || body != "---\nnot closed\n" {
		t.Errorf("unclosed frontmatter = %v, %q", keys, body)
	}
}

func TestReviewPage(t *testing.T) {
	o := Options{Base: "/bottown/demo/wiki"}
	tree := Tree{
		"Home.md":                          []byte("home\n"),
		"reviews/315-x/r1-claude.md":       []byte("---\nround: 1\nactor: claude\ndate: 2026-11-06\n---\n\nA\n"),
		"reviews/315-x/r1-codex.md":        []byte("---\nround: 1\nactor: codex\ndate: 2026-11-06\n---\n\nB\n"),
		"reviews/315-x/r1-consolidated.md": []byte("---\nround: 1\nactor: agy\ndate: 2026-11-07\n---\n\n# Merged\n\nfirst\n"),
		"reviews/315-x/r2-claude.md":       []byte("---\nround: 2\nactor: claude\ndate: 2026-11-08\n---\n\nC\n"),
		"reviews/316-y/r1-claude.md":       []byte("other\n"),
	}
	if _, ok := ReviewPage(Tree{"reviews/315-x/r1-claude.md": []byte("a")}, "315-x", o); ok {
		t.Error("ReviewPage without a consolidation rendered a page")
	}
	page, ok := ReviewPage(tree, "315-x", o)
	if !ok {
		t.Fatal("no page")
	}
	s := string(page)
	for _, want := range []string{
		"# Review of #315\n",
		"[#315](/bottown/demo/issues/315)",
		"## Round 1, consolidated by agy\n\n### Merged\n\nfirst\n",
		"- Round 1 (2026-11-06): claude, codex. [claude](/bottown/demo/wiki/raw/reviews/315-x/r1-claude.md) · [codex](/bottown/demo/wiki/raw/reviews/315-x/r1-codex.md) · [consolidated](/bottown/demo/wiki/raw/reviews/315-x/r1-consolidated.md)\n",
		"- Round 2 (2026-11-08): claude, not consolidated. [claude](/bottown/demo/wiki/raw/reviews/315-x/r2-claude.md)\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("page lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "316") {
		t.Errorf("page mentions another debate:\n%s", s)
	}
	// The page's links are raw files, not pages: no broken links on health.
	tree["Review-315-x.md"] = page
	for _, p := range NewIndex(tree, o).Problems {
		if p.Page == "Review-315-x.md" {
			t.Errorf("health problem on the page: %+v", p)
		}
	}
	if got := Debates(tree); !reflect.DeepEqual(got, []string{"315-x", "316-y"}) {
		t.Errorf("Debates = %v", got)
	}
}

func TestDemoteHeadings(t *testing.T) {
	in := "# A\n## B\n```\n# code\n```\n#nospace\n###### six\n"
	want := "### A\n#### B\n```\n# code\n```\n#nospace\n###### six\n"
	if got := demoteHeadings(in); got != want {
		t.Errorf("demoteHeadings = %q, want %q", got, want)
	}
}
