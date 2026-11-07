package wiki

import (
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Pages and paths. A wiki page is a top-level .md file; files in
// subdirectories are not pages (Forgejo does not render them, and serves
// them only under /wiki/raw/). Forgejo's web path for Name.md is Name, and
// for a page created in its UI or API with a hyphenated title it is
// Name.-, stored as Name.-.md: a link to Name does not reach that page.

// IsPage reports whether p is a wiki page: a top-level .md file.
func IsPage(p string) bool {
	return strings.HasSuffix(p, ".md") && !strings.Contains(p, "/") && len(p) > len(".md")
}

// WebPath is a page's path in Forgejo's wiki URLs (/wiki/<WebPath>).
func WebPath(p string) string { return strings.TrimSuffix(p, ".md") }

// Title is how Forgejo titles a page: dashes are spaces, except in a
// Name.- page, whose dashes are literal.
func Title(p string) string {
	w := WebPath(p)
	if t, ok := strings.CutSuffix(w, ".-"); ok {
		return t
	}
	return strings.ReplaceAll(w, "-", " ")
}

// IsDashPage reports whether p is stored with Forgejo's ".-" suffix.
func IsDashPage(p string) bool { return strings.HasSuffix(p, ".-.md") }

// Resolve finds the file a slug names in files: the slug itself when it
// names a file, else <slug>.md, else <slug>.-.md (the page Forgejo stores
// for a hyphenated title). ok is false when none exists; p is then the file
// a new page with that slug gets: <slug>.md, or the slug itself when it has
// another extension (notes.txt is a file name, not a slug).
func Resolve(files []string, slug string) (p string, ok bool) {
	has := func(f string) bool { return slices.Contains(files, f) }
	if has(slug) {
		return slug, true
	}
	if strings.HasSuffix(slug, ".md") {
		return slug, false
	}
	for _, c := range []string{slug + ".md", slug + ".-.md"} {
		if has(c) {
			return c, true
		}
	}
	if ext := path.Ext(slug); ext != "" && ext != ".-" {
		return slug, false
	}
	return slug + ".md", false
}

// Link is a link from a page's text to another page of the same wiki.
type Link struct {
	Target string // the page file the link names (Name.md), resolved or not
	Raw    string // the link as written
}

var (
	mdLinkRE   = regexp.MustCompile(`(!?)\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	wikiLinkRE = regexp.MustCompile(`\[\[([^\]|]+)(?:\|([^\]]+))?\]\]`)
	codeSpanRE = regexp.MustCompile("`[^`\n]*`")
)

// Links returns the links in text (a page without its blocks) to pages of
// the wiki whose web root is base (/org/repo/wiki). It understands the forms
// Forgejo resolves to a wiki page: [t](Page), [t](./Page), [t](Page.md),
// [t](/org/repo/wiki/Page), [[Page]], [[Page Name]] and [[Text|Page]].
// Images, external links, anchors, other wikis, raw files and links in
// code are not page links.
func Links(text, base string) []Link {
	var out []Link
	for _, line := range proseLines(text) {
		line = codeSpanRE.ReplaceAllString(line, "")
		for _, m := range mdLinkRE.FindAllStringSubmatch(line, -1) {
			if m[1] == "!" {
				continue
			}
			if t, ok := linkTarget(m[2], base); ok {
				out = append(out, Link{Target: t, Raw: m[0]})
			}
		}
		for _, m := range wikiLinkRE.FindAllStringSubmatch(line, -1) {
			target := m[1]
			if m[2] != "" {
				target = m[2]
			}
			if t, ok := linkTarget(strings.ReplaceAll(strings.TrimSpace(target), " ", "-"), base); ok {
				out = append(out, Link{Target: t, Raw: m[0]})
			}
		}
	}
	return out
}

// proseLines are text's lines outside fenced code blocks.
func proseLines(text string) []string {
	var out []string
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
			continue
		}
		out = append(out, line)
	}
	return out
}

// linkTarget is the page file a link target names, if it names one.
func linkTarget(t, base string) (string, bool) {
	if i := strings.IndexAny(t, "#?"); i >= 0 {
		t = t[:i]
	}
	if t == "" || strings.Contains(t, "://") || strings.HasPrefix(t, "mailto:") {
		return "", false
	}
	if u, err := url.PathUnescape(t); err == nil {
		t = u
	}
	if strings.HasPrefix(t, "/") {
		rest, ok := cutPrefixFold(t, strings.TrimSuffix(base, "/")+"/")
		if !ok {
			return "", false
		}
		t = rest
	}
	t = path.Clean(strings.TrimPrefix(t, "./"))
	if t == "." || strings.Contains(t, "/") || strings.HasPrefix(t, ".") {
		return "", false
	}
	t = strings.ReplaceAll(strings.TrimSuffix(t, ".md"), " ", "-")
	if t == "" {
		return "", false
	}
	return t + ".md", true
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}
