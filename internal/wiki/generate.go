package wiki

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Tree is a wiki's files at one commit: path to content.
type Tree map[string][]byte

// Files are the tree's paths, sorted.
func (t Tree) Files() []string {
	out := make([]string, 0, len(t))
	for p := range t {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Options are what the generator needs beyond the tree.
type Options struct {
	// Base is the wiki's web root, /org/repo/wiki, which absolute links
	// to its pages start with.
	Base string
}

// HealthPage is the page the health block lives on; the generator creates
// it when it is missing.
const HealthPage = "Wiki-Health.md"

// Generator renders one kind of block.
type Generator struct {
	Name string
	// Default reports whether page p gets the block when it has none; it
	// is then appended at the end of the page.
	Default func(p string) bool
	// Render is the block's body on page p.
	Render func(ix *Index, p string) string
}

// Generators is the registry of block names. #311 and #315 add theirs.
var Generators = []Generator{
	{Name: "backlinks", Default: func(p string) bool { return p != HealthPage && !special(p) }, Render: renderBacklinks},
	{Name: "health", Default: func(p string) bool { return p == HealthPage }, Render: renderHealth},
}

// Lookup is the generator for a block name, or nil.
func Lookup(name string) *Generator {
	for i := range Generators {
		if Generators[i].Name == name {
			return &Generators[i]
		}
	}
	return nil
}

// Names are the registered block names.
func Names() []string {
	var out []string
	for _, g := range Generators {
		out = append(out, g.Name)
	}
	return out
}

// special pages are Forgejo's sidebar and footer, which render on every
// page and get no blocks of their own.
func special(p string) bool { return strings.HasPrefix(p, "_") }

// Problem is something wrong in the wiki that the health block reports.
type Problem struct {
	Page string `json:"page"`
	Kind string `json:"kind"` // broken-link | dash-page | unknown-block | mangled
	Msg  string `json:"msg"`
}

// Index is what blocks render from: the wiki's pages, their links and its
// problems. It is built from page text outside generated blocks.
type Index struct {
	Pages     []string            // page files, sorted
	Files     []string            // every markdown file, pages included, sorted: where blocks may be
	Links     map[string][]string // page -> pages it links to, sorted, resolved only
	Backlinks map[string][]string // page -> pages linking to it, sorted
	Problems  []Problem           // sorted by page, kind, message
	segs      map[string][]Segment
}

// NewIndex reads a tree. A file with mangled markers is indexed by its
// whole text and reported; its blocks are left alone. Every markdown file
// is checked for blocks, but only pages are indexed for links: files in
// subdirectories are not pages, and a link cannot name one.
func NewIndex(t Tree, o Options) *Index {
	ix := &Index{Links: map[string][]string{}, Backlinks: map[string][]string{}, segs: map[string][]Segment{}}
	for _, p := range t.Files() {
		if IsPage(p) {
			ix.Pages = append(ix.Pages, p)
		}
		if strings.HasSuffix(p, ".md") {
			ix.Files = append(ix.Files, p)
		}
	}
	if !slices.Contains(ix.Pages, HealthPage) {
		ix.Pages = append(ix.Pages, HealthPage)
		sort.Strings(ix.Pages)
		ix.Files = append(ix.Files, HealthPage)
		sort.Strings(ix.Files)
	}
	texts := map[string]string{}
	for _, p := range ix.Files {
		content := string(t[p])
		segs, err := Parse(content)
		texts[p] = content
		if err != nil {
			ix.Problems = append(ix.Problems, Problem{Page: p, Kind: "mangled", Msg: err.Error()})
			continue
		}
		ix.segs[p] = segs
		texts[p] = Text(segs)
		for _, s := range segs {
			if s.Block != "" && Lookup(s.Block) == nil {
				ix.Problems = append(ix.Problems, Problem{Page: p, Kind: "unknown-block", Msg: fmt.Sprintf("unknown generated block %q", s.Block)})
			}
		}
	}
	for _, p := range ix.Pages {
		text := texts[p]
		if IsDashPage(p) {
			ix.Problems = append(ix.Problems, Problem{Page: p, Kind: "dash-page", Msg: fmt.Sprintf(
				"stored as %s (a hyphenated title made in the web UI or API): its URL is %s, and links to %s do not reach it",
				p, WebPath(p), strings.TrimSuffix(WebPath(p), ".-"))})
		}
		seen := map[string]bool{}
		for _, l := range Links(text, o.Base) {
			// The sidebar and footer render on every page: their links
			// are navigation, not backlinks (broken ones still count).
			if l.Target == p || special(p) && slices.Contains(ix.Pages, l.Target) {
				continue
			}
			if !slices.Contains(ix.Pages, l.Target) {
				msg := fmt.Sprintf("%s links to %s, which does not exist", l.Raw, WebPath(l.Target))
				if dash := strings.TrimSuffix(l.Target, ".md") + ".-.md"; t[dash] != nil {
					msg += fmt.Sprintf(" (the page is stored as %s: link %s)", dash, WebPath(dash))
				}
				ix.Problems = append(ix.Problems, Problem{Page: p, Kind: "broken-link", Msg: msg})
				continue
			}
			if !seen[l.Target] {
				seen[l.Target] = true
				ix.Links[p] = append(ix.Links[p], l.Target)
				ix.Backlinks[l.Target] = append(ix.Backlinks[l.Target], p)
			}
		}
		sort.Strings(ix.Links[p])
	}
	for k := range ix.Backlinks {
		sort.Strings(ix.Backlinks[k])
	}
	sort.SliceStable(ix.Problems, func(i, j int) bool {
		a, b := ix.Problems[i], ix.Problems[j]
		if a.Page != b.Page {
			return a.Page < b.Page
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Msg < b.Msg
	})
	return ix
}

// pageLink links to a page; a file in a subdirectory, which has no page
// URL, is named instead.
func pageLink(p string) string {
	if !IsPage(p) {
		return "`" + p + "`"
	}
	return fmt.Sprintf("[%s](%s)", Title(p), WebPath(p))
}

func renderBacklinks(ix *Index, p string) string {
	bl := ix.Backlinks[p]
	if len(bl) == 0 {
		return "\n_No other page links here._\n"
	}
	links := make([]string, len(bl))
	for i, b := range bl {
		links[i] = pageLink(b)
	}
	return "\n**Linked from:** " + strings.Join(links, ", ") + "\n"
}

func renderHealth(ix *Index, _ string) string {
	if len(ix.Problems) == 0 {
		return "\nNo problems found.\n"
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, pr := range ix.Problems {
		fmt.Fprintf(&b, "- %s (%s): %s\n", pageLink(pr.Page), pr.Kind, escapeComment(pr.Msg))
	}
	return b.String()
}

// escapeComment keeps a reported marker from being read as one.
func escapeComment(s string) string { return strings.ReplaceAll(s, "<!--", "&lt;!--") }

// Stale is a block whose content the generator changes.
type Stale struct {
	Page  string `json:"page"`
	Block string `json:"block"`
}

// Result is what Generate changes.
type Result struct {
	Changed  map[string][]byte // files whose content changes, new files included
	Stale    []Stale           // sorted by page, block
	Problems []Problem
}

// Generate regenerates every block in the tree: each block's body is
// re-rendered, and a page missing a block its generators default to gets it
// at the end. Blocks in files in subdirectories are re-rendered too, but
// only pages get default blocks. Files with mangled markers are left alone.
// Generate is a pure function of the tree: run on its own output it changes
// nothing.
func Generate(t Tree, o Options) Result {
	ix := NewIndex(t, o)
	res := Result{Changed: map[string][]byte{}, Problems: ix.Problems}
	for _, p := range ix.Files {
		segs, ok := ix.segs[p]
		if !ok {
			continue // mangled
		}
		segs = slices.Clone(segs)
		has := map[string]bool{}
		var stale []string
		for i, s := range segs {
			g := Lookup(s.Block)
			if s.Block == "" || g == nil {
				continue
			}
			has[s.Block] = true
			if body := g.Render(ix, p); body != s.Text {
				segs[i].Text = body
				stale = append(stale, s.Block)
			}
		}
		for _, g := range Generators {
			if has[g.Name] || !IsPage(p) || !g.Default(p) {
				continue
			}
			segs = appendBlock(segs, g.Name, g.Render(ix, p))
			stale = append(stale, g.Name)
		}
		if len(stale) == 0 {
			continue
		}
		out := Join(segs)
		if t[p] != nil && out == string(t[p]) {
			continue
		}
		if t[p] == nil && p == HealthPage {
			out = "# Wiki health\n\nWhat bottown found wrong in this wiki. Generated: edits to the block below are overwritten.\n\n" + strings.TrimPrefix(out, "\n\n")
		}
		res.Changed[p] = []byte(out)
		for _, b := range stale {
			res.Stale = append(res.Stale, Stale{Page: p, Block: b})
		}
	}
	sort.Slice(res.Stale, func(i, j int) bool {
		if res.Stale[i].Page != res.Stale[j].Page {
			return res.Stale[i].Page < res.Stale[j].Page
		}
		return res.Stale[i].Block < res.Stale[j].Block
	})
	return res
}

// appendBlock adds a block at the end of a page, after a blank line.
func appendBlock(segs []Segment, name, body string) []Segment {
	text := Join(segs)
	sep := ""
	switch {
	case text == "":
	case strings.HasSuffix(text, "\n\n"):
	case strings.HasSuffix(text, "\n"):
		sep = "\n"
	default:
		sep = "\n\n"
	}
	if sep != "" {
		segs = append(segs, Segment{Text: sep})
	}
	return append(segs, Segment{Block: name, Text: body}, Segment{Text: "\n"})
}
