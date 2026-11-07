package wiki

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Debates (#315): several agents answer the same question about an issue,
// independently, in rounds, and a consolidate run merges each round's
// answers. Their files live in a wiki subdirectory, which Forgejo does not
// list or render as pages (it serves them under /wiki/raw/), and a page
// bottown rewrites after every consolidated round shows the latest result:
//
//	Review-<name>.md                     the page, rewritten per round
//	reviews/<name>/r1-claude.md          one answer per actor and round
//	reviews/<name>/r1-consolidated.md    the round's consolidation
//
// <name> is [<YYYY-MM-DD>-]<issue>[-<slug>]: the date, when the owner gives
// one, separates debates on one issue held on different days.

// ReviewsDir is the wiki directory that holds every debate's round files.
const ReviewsDir = "reviews"

// Consolidated is the author part of a round's consolidation,
// r<N>-consolidated.md.
const Consolidated = "consolidated"

var (
	nameRE  = regexp.MustCompile(`^(?:(\d{4}-\d{2}-\d{2})-)?([1-9][0-9]*)(?:-([a-z0-9]+(?:-[a-z0-9]+)*))?$`)
	slugRE  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	roundRE = regexp.MustCompile(`^r([1-9][0-9]*)-([a-z0-9][a-z0-9_-]*)\.md$`)
	// Markdown link and image targets, dropped from a title before it is
	// slugged: [text](url) keeps only text.
	mdTargetRE = regexp.MustCompile(`\]\([^)]*\)`)
	nonWordRE  = regexp.MustCompile(`[^a-z0-9]+`)
)

// MaxSlug bounds a debate's slug.
const MaxSlug = 60

// slugWords is how many of a title's words its slug keeps.
const slugWords = 5

// slugChars caps a slug derived from a title, at a word boundary.
const slugChars = 40

// Debate names one debate: its issue, the date it is held under (empty for
// none) and its slug (empty for none).
type Debate struct {
	Issue int64
	Date  string // YYYY-MM-DD, or empty
	Slug  string
}

// Name is the debate's name: [<date>-]<issue>[-<slug>].
func (d Debate) Name() string {
	n := strconv.FormatInt(d.Issue, 10)
	if d.Date != "" {
		n = d.Date + "-" + n
	}
	if d.Slug != "" {
		n += "-" + d.Slug
	}
	return n
}

// Dir is the wiki directory of the debate's round files.
func (d Debate) Dir() string { return ReviewsDir + "/" + d.Name() }

// Page is the debate's page file, which bottown rewrites per round.
func (d Debate) Page() string { return "Review-" + d.Name() + ".md" }

// RoundFile is the file of author's part of round n.
func (d Debate) RoundFile(n int, author string) string {
	return fmt.Sprintf("%s/r%d-%s.md", d.Dir(), n, author)
}

// ParseDebate reads a debate's name.
func ParseDebate(name string) (Debate, bool) {
	m := nameRE.FindStringSubmatch(name)
	if m == nil {
		return Debate{}, false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return Debate{}, false
	}
	if m[1] != "" {
		if _, err := time.Parse(time.DateOnly, m[1]); err != nil {
			return Debate{}, false
		}
	}
	return Debate{Issue: n, Date: m[1], Slug: m[3]}, true
}

// ValidSlug checks a slug the owner gave.
func ValidSlug(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > MaxSlug || !slugRE.MatchString(s) {
		return fmt.Errorf("slug %q must be lowercase letters, digits and single dashes, at most %d characters", s, MaxSlug)
	}
	return nil
}

// Slug derives a debate's slug from its issue's title: its first five
// words, without markdown formatting, lowercase and joined by dashes, cut
// at a word boundary near 40 characters. A title with no letters or digits
// has no slug.
func Slug(title string) string {
	t := mdTargetRE.ReplaceAllString(title, "]")
	t = strings.ToLower(t)
	t = strings.ReplaceAll(t, "'", "")
	t = strings.ReplaceAll(t, "’", "")
	words := strings.Fields(nonWordRE.ReplaceAllString(t, " "))
	if len(words) > slugWords {
		words = words[:slugWords]
	}
	var out string
	for _, w := range words {
		next := w
		if out != "" {
			next = out + "-" + w
		}
		if len(next) > slugChars && out != "" {
			break
		}
		out = next
	}
	if len(out) > MaxSlug {
		out = strings.Trim(out[:MaxSlug], "-")
	}
	return out
}

// Round is a parsed round file: reviews/<debate>/r<N>-<author>.md.
type Round struct {
	Debate string // the debate's name
	N      int
	Author string // an actor, or Consolidated
}

// File is the round file's path.
func (r Round) File() string {
	return fmt.Sprintf("%s/%s/r%d-%s.md", ReviewsDir, r.Debate, r.N, r.Author)
}

// ParseRound reads a wiki path as a round file.
func ParseRound(p string) (Round, bool) {
	segs := strings.Split(p, "/")
	if len(segs) != 3 || segs[0] != ReviewsDir {
		return Round{}, false
	}
	if _, ok := ParseDebate(segs[1]); !ok {
		return Round{}, false
	}
	m := roundRE.FindStringSubmatch(segs[2])
	if m == nil {
		return Round{}, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return Round{}, false
	}
	return Round{Debate: segs[1], N: n, Author: m[2]}, true
}

// Debates are the names of the debates the tree has files of, sorted.
func Debates(t Tree) []string {
	var out []string
	for p := range t {
		if r, ok := ParseRound(p); ok && !slices.Contains(out, r.Debate) {
			out = append(out, r.Debate)
		}
	}
	slices.Sort(out)
	return out
}

// Rounds are a debate's round files in the tree, by round, each round's
// files sorted with the consolidation last.
func Rounds(t Tree, debate string) map[int][]Round {
	out := map[int][]Round{}
	for p := range t {
		if r, ok := ParseRound(p); ok && r.Debate == debate {
			out[r.N] = append(out[r.N], r)
		}
	}
	for _, rs := range out {
		slices.SortFunc(rs, func(a, b Round) int {
			switch {
			case a.Author == b.Author:
				return 0
			case a.Author == Consolidated:
				return 1
			case b.Author == Consolidated:
				return -1
			}
			return strings.Compare(a.Author, b.Author)
		})
	}
	return out
}

// Frontmatter is what bottown stamps on a round file: who wrote it, in which
// run and round, against which commit.
type Frontmatter struct {
	Round int
	Actor string
	Model string
	Run   string
	Issue int64
	SHA   string
	Date  string // YYYY-MM-DD
}

// frontmatterKeys is the order the stamp writes its keys in.
var frontmatterKeys = []string{"round", "actor", "model", "run", "issue", "sha", "date"}

// Stamp puts f at the head of content, replacing any frontmatter the writer
// sent: the stamp is bottown's, not the writer's.
func Stamp(content string, f Frontmatter) string {
	_, body := SplitFrontmatter(content)
	vals := map[string]string{
		"round": strconv.Itoa(f.Round), "actor": f.Actor, "model": f.Model, "run": f.Run,
		"sha": f.SHA, "date": f.Date,
	}
	if f.Issue > 0 {
		vals["issue"] = strconv.FormatInt(f.Issue, 10)
	}
	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range frontmatterKeys {
		if v := vals[k]; v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, oneLine(v))
		}
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimLeft(body, "\n"))
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// SplitFrontmatter separates a file's leading frontmatter (between "---"
// lines) into its keys and returns the rest. A file without one has no
// keys.
func SplitFrontmatter(content string) (map[string]string, string) {
	rest, ok := strings.CutPrefix(content, "---\n")
	if !ok {
		return map[string]string{}, content
	}
	end := strings.Index(rest, "\n---\n")
	var head, body string
	switch {
	case strings.HasPrefix(rest, "---\n"):
		head, body = "", rest[len("---\n"):]
	case end >= 0:
		head, body = rest[:end], rest[end+len("\n---\n"):]
	case strings.HasSuffix(rest, "\n---"):
		head, body = strings.TrimSuffix(rest, "\n---"), ""
	default:
		return map[string]string{}, content
	}
	keys := map[string]string{}
	for _, line := range strings.Split(head, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			keys[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return keys, body
}

// ReviewPage renders a debate's page from its round files in t: the latest
// consolidated round's text, and one line per round linking its files
// under /wiki/raw/, since Forgejo renders no page for them. ok is false
// when no round is consolidated yet.
func ReviewPage(t Tree, debate string, o Options) (content []byte, ok bool) {
	d, valid := ParseDebate(debate)
	rounds := Rounds(t, debate)
	if !valid || len(rounds) == 0 {
		return nil, false
	}
	ns := make([]int, 0, len(rounds))
	for n := range rounds {
		ns = append(ns, n)
	}
	slices.Sort(ns)
	latest := 0
	for _, n := range ns {
		if hasConsolidation(rounds[n]) {
			latest = n
		}
	}
	if latest == 0 {
		return nil, false
	}
	base := strings.TrimSuffix(o.Base, "/")
	repoBase := strings.TrimSuffix(base, "/wiki")
	var b strings.Builder
	fmt.Fprintf(&b, "# Review of #%d", d.Issue)
	if d.Date != "" {
		fmt.Fprintf(&b, " (%s)", d.Date)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Independent answers to the same question about [#%d](%s/issues/%d), one per actor and round, merged by a consolidate run. bottown rewrites this page from `%s/` after every consolidated round, so edits here do not last. It binds nothing: issues and pull requests filed from it do.\n\n",
		d.Issue, repoBase, d.Issue, d.Dir())
	cons := Round{Debate: debate, N: latest, Author: Consolidated}
	fm, body := SplitFrontmatter(string(t[cons.File()]))
	fmt.Fprintf(&b, "## Round %d, consolidated", latest)
	if a := fm["actor"]; a != "" {
		fmt.Fprintf(&b, " by %s", a)
	}
	b.WriteString("\n\n")
	segs, err := Parse(body)
	if err == nil {
		body = Text(segs)
	}
	b.WriteString(strings.TrimSpace(demoteHeadings(body)))
	b.WriteString("\n\n## Rounds\n\n")
	for _, n := range ns {
		var actors, links []string
		date := ""
		for _, r := range rounds[n] {
			rfm, _ := SplitFrontmatter(string(t[r.File()]))
			if date == "" || (rfm["date"] != "" && rfm["date"] < date) {
				date = rfm["date"]
			}
			if r.Author != Consolidated {
				actors = append(actors, r.Author)
			}
			links = append(links, fmt.Sprintf("[%s](%s/raw/%s)", r.Author, base, r.File()))
		}
		fmt.Fprintf(&b, "- Round %d", n)
		if date != "" {
			fmt.Fprintf(&b, " (%s)", date)
		}
		if len(actors) > 0 {
			fmt.Fprintf(&b, ": %s", strings.Join(actors, ", "))
		}
		if !hasConsolidation(rounds[n]) {
			b.WriteString(", not consolidated")
		}
		fmt.Fprintf(&b, ". %s\n", strings.Join(links, " · "))
	}
	return []byte(b.String()), true
}

func hasConsolidation(rs []Round) bool {
	return slices.ContainsFunc(rs, func(r Round) bool { return r.Author == Consolidated })
}

// demoteHeadings moves the consolidation's headings below the page's own
// "## Round N" heading: # becomes ###, ## becomes ####, and so on, outside
// fenced code.
func demoteHeadings(s string) string {
	lines := strings.Split(s, "\n")
	fence := false
	for i, l := range lines {
		trim := strings.TrimLeft(l, " ")
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			fence = !fence
			continue
		}
		if fence || !strings.HasPrefix(trim, "#") {
			continue
		}
		level := len(trim) - len(strings.TrimLeft(trim, "#"))
		if level > 6 || (len(trim) > level && trim[level] != ' ') {
			continue
		}
		lines[i] = strings.Repeat("#", min(level+2, 6)) + trim[level:]
	}
	return strings.Join(lines, "\n")
}
