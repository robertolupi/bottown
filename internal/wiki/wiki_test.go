package wiki

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestParse(t *testing.T) {
	cases := []struct {
		name, in string
		segs     []Segment
		err      string
	}{
		{name: "no blocks", in: "text\n", segs: []Segment{{Text: "text\n"}}},
		{name: "empty", in: ""},
		{name: "block", in: "a\n" + Open("backlinks") + "x" + Close("backlinks") + "\nb",
			segs: []Segment{{Text: "a\n"}, {Block: "backlinks", Text: "x"}, {Text: "\nb"}}},
		{name: "two blocks", in: Open("a") + Close("a") + Open("b") + "y" + Close("b"),
			segs: []Segment{{Block: "a"}, {Block: "b", Text: "y"}}},
		{name: "unknown name parses", in: Open("zzz") + Close("zzz"), segs: []Segment{{Block: "zzz"}}},
		{name: "missing close", in: "a\n" + Open("backlinks") + "x", err: `block "backlinks" is never closed`},
		{name: "close without open", in: "a\n" + Close("backlinks"), err: `line 2: block "backlinks" closes but was never opened`},
		{name: "nested", in: Open("a") + "\n" + Open("b") + Close("b") + Close("a"), err: `line 2: block "b" opens inside block "a"`},
		{name: "mismatched close", in: Open("a") + Close("b"), err: `block "b" closes inside block "a"`},
		{name: "not a marker", in: "<!-- bottown:Backlinks:generated -->", segs: []Segment{{Text: "<!-- bottown:Backlinks:generated -->"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs, err := Parse(c.in)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(segs, c.segs) {
				t.Fatalf("segs = %#v, want %#v", segs, c.segs)
			}
			if Join(segs) != c.in {
				t.Fatalf("Join = %q, want %q", Join(segs), c.in)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	stale := "a\n" + Open("backlinks") + "\nwhatever an agent wrote\n" + Close("backlinks") + "\n"
	got, err := Normalize(stale)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\n" + Open("backlinks") + Close("backlinks") + "\n"; got != want {
		t.Fatalf("Normalize = %q, want %q", got, want)
	}
	if _, err := Normalize(Open("nope") + Close("nope")); err == nil || !strings.Contains(err.Error(), `unknown generated block "nope"`) {
		t.Fatalf("unknown block: err = %v", err)
	}
	if _, err := Normalize(Open("backlinks")); err == nil {
		t.Fatal("mangled: no error")
	}
}

func TestLinks(t *testing.T) {
	text := strings.Join([]string{
		"[a](Other-Page) [b](./Second) [c](Third.md) [d](/bottown/demo/wiki/Fourth#top)",
		"[[Fifth]] [[Sixth Page]] [[Shown|Seventh]] [e](Dash-Page.-)",
		"not links: ![img](Image) [ext](https://example.com/x) [mail](mailto:a@b) [anchor](#top)",
		"[other wiki](/bottown/other/wiki/X) [raw](/bottown/demo/wiki/raw/reviews/r1.md) [sub](reviews/x.md) `[code](Code)`",
		"```",
		"[fenced](Fenced)",
		"```",
		"[%20](Space%20Page)",
	}, "\n")
	var got []string
	for _, l := range Links(text, "/bottown/demo/wiki") {
		got = append(got, l.Target)
	}
	want := []string{"Other-Page.md", "Second.md", "Third.md", "Fourth.md", "Dash-Page.-.md",
		"Fifth.md", "Sixth-Page.md", "Seventh.md", "Space-Page.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %q\nwant      %q", got, want)
	}
}

func TestResolve(t *testing.T) {
	files := []string{"Home.md", "Dash-Page.-.md", "reviews/1-x/r1-claude.md"}
	cases := []struct {
		slug, want string
		ok         bool
	}{
		{"Home", "Home.md", true},
		{"Home.md", "Home.md", true},
		{"Dash-Page", "Dash-Page.-.md", true},
		{"Dash-Page.-", "Dash-Page.-.md", true},
		{"New-Idea", "New-Idea.md", false},
		{"notes.txt", "notes.txt", false},
		{"reviews/1-x/r1-claude.md", "reviews/1-x/r1-claude.md", true},
		{"reviews/1-x/r1-codex.md", "reviews/1-x/r1-codex.md", false},
	}
	for _, c := range cases {
		if got, ok := Resolve(files, c.slug); got != c.want || ok != c.ok {
			t.Errorf("Resolve(%q) = %q, %v; want %q, %v", c.slug, got, ok, c.want, c.ok)
		}
	}
	if got := Title("Sixth-Page.md"); got != "Sixth Page" {
		t.Errorf("Title = %q", got)
	}
	if got := Title("Dash-Page.-.md"); got != "Dash-Page" {
		t.Errorf("Title dash = %q", got)
	}
}

// goldenTree is the wiki testdata/generate/in holds.
func readTree(t *testing.T, dir string) Tree {
	t.Helper()
	tree := Tree{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		tree[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func apply(t Tree, changed map[string][]byte) Tree {
	out := Tree{}
	for k, v := range t {
		out[k] = v
	}
	for k, v := range changed {
		out[k] = v
	}
	return out
}

// TestGenerateGolden: the generator's output for testdata/generate/in is
// testdata/generate/out (go test -run Golden -update rewrites it), and
// generating on its own output changes nothing.
func TestGenerateGolden(t *testing.T) {
	o := Options{Base: "/bottown/demo/wiki"}
	in := readTree(t, filepath.Join("testdata", "generate", "in"))
	res := Generate(in, o)
	got := apply(in, res.Changed)
	outDir := filepath.Join("testdata", "generate", "out")
	if *update {
		if err := os.RemoveAll(outDir); err != nil {
			t.Fatal(err)
		}
		for p, b := range got {
			f := filepath.Join(outDir, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := readTree(t, outDir)
	for _, p := range append(got.Files(), want.Files()...) {
		if string(got[p]) != string(want[p]) {
			t.Errorf("%s:\n--- got\n%s\n--- want\n%s", p, got[p], want[p])
		}
	}
	if _, ok := res.Changed["Mangled.md"]; ok {
		t.Error("a page with mangled markers was rewritten")
	}
	if _, ok := res.Changed["reviews/1-demo/r1-claude.md"]; ok {
		t.Error("a file in a subdirectory was rewritten")
	}
	again := Generate(got, o)
	if len(again.Changed) != 0 || len(again.Stale) != 0 {
		t.Errorf("not idempotent: second pass changes %v (stale %v)", again.Changed, again.Stale)
	}
	if !reflect.DeepEqual(again.Problems, res.Problems) {
		t.Errorf("problems differ between passes:\n%v\n%v", res.Problems, again.Problems)
	}
	kinds := map[string]bool{}
	for _, p := range res.Problems {
		kinds[p.Kind] = true
	}
	for _, k := range []string{"broken-link", "dash-page", "unknown-block", "mangled"} {
		if !kinds[k] {
			t.Errorf("no %s problem reported: %v", k, res.Problems)
		}
	}
}

// TestGenerateStaleBlocks: a missing block is inserted, a stale one is
// rewritten, a current one is left alone, and the stale list says which.
func TestGenerateStaleBlocks(t *testing.T) {
	o := Options{Base: "/o/r/wiki"}
	tree := Tree{"A.md": []byte("links to [B](B)\n"), "B.md": []byte("B\n")}
	res := Generate(tree, o)
	want := []Stale{{"A.md", "backlinks"}, {"B.md", "backlinks"}, {HealthPage, "health"}}
	if !reflect.DeepEqual(res.Stale, want) {
		t.Fatalf("stale = %v, want %v", res.Stale, want)
	}
	tree = apply(tree, res.Changed)
	if !strings.Contains(string(tree["B.md"]), "**Linked from:** [A](A)") {
		t.Fatalf("B.md = %q", tree["B.md"])
	}
	// The owner adds a link in the web UI: only C's and B's blocks go stale.
	tree["C.md"] = []byte("also [B](B)\n")
	res = Generate(tree, o)
	want = []Stale{{"B.md", "backlinks"}, {"C.md", "backlinks"}}
	if !reflect.DeepEqual(res.Stale, want) {
		t.Fatalf("stale after edit = %v, want %v", res.Stale, want)
	}
	// Someone edits inside a block: generate restores it.
	tree = apply(tree, res.Changed)
	b := strings.Replace(string(tree["B.md"]), "**Linked from:**", "edited by hand", 1)
	tree["B.md"] = []byte(b)
	res = Generate(tree, o)
	if !reflect.DeepEqual(res.Stale, []Stale{{"B.md", "backlinks"}}) || !strings.Contains(string(res.Changed["B.md"]), "**Linked from:** [A](A), [C](C)") {
		t.Fatalf("hand edit not restored: %v %q", res.Stale, res.Changed["B.md"])
	}
}
