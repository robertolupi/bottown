// Package milestone parses and checks milestone plan files: the
// <milestones_dir>/<slug>.md files that `bottown milestone apply` turns into
// an Implementation Epic issue and its child issues (docs/design/milestones.md
// §2.4). Check is offline: it reads bytes and reports problems, and never
// contacts the server or Forgejo. Apply (apply.go) reconciles a plan into
// the forge through the Forge interface, which the CLI implements.
package milestone

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/robertolupi/bottown/internal/config"
)

// A Plan is a parsed milestone plan file.
type Plan struct {
	Slug string // the file name without .md
	File string // the name diagnostics use for the file

	Title   string
	Kickoff int64
	Design  string // repo-relative, or empty
	// DesignLine is the line of the design key, for diagnostics about the
	// file it names.
	DesignLine int

	Overview string // the prose before the first section, trimmed
	Issues   []Issue
}

// An Issue is one `## <Title>` section: a child issue of the epic.
type Issue struct {
	Title   string
	Key     string
	Depends []string // keys in the plan, or "#<N>"
	Body    string
	Line    int // the line of the section's heading
}

// A Diagnostic is one problem in a plan file.
type Diagnostic struct {
	File    string
	Line    int
	Section string // the section's key or heading, if the problem is in one
	Msg     string
}

func (d Diagnostic) String() string {
	var b strings.Builder
	b.WriteString(d.File)
	if d.Line > 0 {
		fmt.Fprintf(&b, ":%d", d.Line)
	}
	b.WriteString(": ")
	if d.Section != "" {
		b.WriteString(d.Section + ": ")
	}
	b.WriteString(d.Msg)
	return b.String()
}

// Diagnostics is the error Check returns: every problem it found, in the
// order of the file.
type Diagnostics []Diagnostic

func (ds Diagnostics) Error() string {
	lines := make([]string, len(ds))
	for i, d := range ds {
		lines[i] = d.String()
	}
	return strings.Join(lines, "\n")
}

var slugRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidSlug reports whether s is a valid plan slug, and so a valid issue key.
func ValidSlug(s string) bool { return slugRE.MatchString(s) }

// SlugOf is the slug a plan file's path gives it: its base name without .md.
func SlugOf(file string) string {
	return strings.TrimSuffix(path.Base(strings.ReplaceAll(file, `\`, "/")), ".md")
}

var issueRefRE = regexp.MustCompile(`^#[1-9][0-9]*$`)

// Check parses the plan file src, named file in diagnostics, and checks it
// against every rule of the format that needs only the file: structure,
// fields, keys, dependencies and that the in-file dependencies form a DAG.
// On any problem it returns the Diagnostics, along with as much of the plan
// as it could parse.
func Check(file string, src []byte) (*Plan, error) {
	p := &parser{plan: &Plan{File: file, Slug: SlugOf(file)}}
	if !ValidSlug(p.plan.Slug) {
		p.errorf(0, "", "plan slug %q (the file name without .md) must match %s", p.plan.Slug, slugRE)
	}
	p.parse(src)
	p.check()
	if len(p.diags) > 0 {
		return p.plan, p.diags
	}
	return p.plan, nil
}

type parser struct {
	plan  *Plan
	diags Diagnostics
}

func (p *parser) errorf(line int, section, format string, args ...any) {
	p.diags = append(p.diags, Diagnostic{File: p.plan.File, Line: line, Section: section, Msg: fmt.Sprintf(format, args...)})
}

// frontMatter is the plan's TOML front matter.
type frontMatter struct {
	Title   string  `toml:"title"`
	Kickoff int64   `toml:"kickoff"`
	Design  *string `toml:"design"` // nil when omitted: an empty value is invalid
}

// issueBlock is the TOML in a section's ```issue fence.
type issueBlock struct {
	Key     string   `toml:"key"`
	Depends []string `toml:"depends"`
}

// fence is an open fenced code block.
type fence struct {
	char byte
	n    int
	info string
	line int
}

// openFence reports whether line opens a fenced code block: up to three
// spaces, then three or more backticks or tildes, then the info string.
func openFence(line string) (fence, bool) {
	s, ok := unindent(line)
	if !ok || len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return fence{}, false
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	info := strings.TrimSpace(s[n:])
	if n < 3 || s[0] == '`' && strings.Contains(info, "`") {
		return fence{}, false
	}
	return fence{char: s[0], n: n, info: info}, true
}

// closes reports whether line closes f.
func (f fence) closes(line string) bool {
	s, ok := unindent(line)
	if !ok {
		return false
	}
	n := 0
	for n < len(s) && s[n] == f.char {
		n++
	}
	return n >= f.n && strings.TrimSpace(s[n:]) == ""
}

// isIssue reports whether f is a section's ```issue block.
func (f fence) isIssue() bool {
	word, _, _ := strings.Cut(f.info, " ")
	return word == "issue"
}

// unindent strips the up to three spaces of indentation a fence or heading
// may have; four or more make an indented code block.
func unindent(line string) (string, bool) {
	i := 0
	for i < len(line) && i < 4 && line[i] == ' ' {
		i++
	}
	return line[i:], i < 4
}

// heading reports whether line is an ATX heading, and its level and title.
func heading(line string) (level int, title string, ok bool) {
	s, ok := unindent(line)
	if !ok {
		return 0, "", false
	}
	for level < len(s) && s[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level < len(s) && s[level] != ' ' && s[level] != '\t' {
		return 0, "", false
	}
	title = strings.TrimSpace(s[level:])
	// An optional closing sequence of #s.
	if t := strings.TrimRight(title, "#"); t != title && (t == "" || strings.HasSuffix(t, " ") || strings.HasSuffix(t, "\t")) {
		title = strings.TrimSpace(t)
	}
	return level, title, true
}

func (p *parser) parse(src []byte) {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Front matter: +++ on line 1, closed by +++ on its own line.
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "+++" {
		p.errorf(1, "", "the file must start with +++ TOML front matter on line 1")
		return
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "+++" {
			end = i
			break
		}
	}
	if end < 0 {
		p.errorf(1, "", "the front matter opened on line 1 is never closed with +++")
		return
	}
	p.frontMatter(lines[1:end], 2)

	// The rest: overview prose, then ## sections.
	var (
		overview []string
		cur      *section // nil in the overview
		sections []*section
		open     *fence // the fenced code block the line is in, if any
	)
	add := func(line string) {
		if cur != nil {
			cur.lines = append(cur.lines, line)
		} else {
			overview = append(overview, line)
		}
	}
	for i := end + 1; i < len(lines); i++ {
		line, n := lines[i], i+1
		inBlock := cur != nil && cur.block != nil && cur.block == open
		switch {
		case open != nil && open.closes(line):
			open = nil
			if inBlock {
				cur.closed = true
				continue
			}
			add(line)
		case inBlock:
			cur.blockLines = append(cur.blockLines, line)
		case open != nil:
			add(line)
		default:
			if f, ok := openFence(line); ok {
				f.line = n
				open = &f
				if cur != nil && f.isIssue() {
					if cur.block == nil && blank(cur.lines) {
						cur.block, cur.lines = open, nil
						continue
					}
					if cur.block != nil {
						p.errorf(n, cur.name(), "a second issue block: a section has exactly one")
					}
				}
				add(line)
				continue
			}
			if level, title, ok := heading(line); ok && level <= 2 {
				if level == 1 {
					p.errorf(n, "", "level-1 heading %q is ambiguous after the front matter: use ## for an issue, and ### or deeper inside one", title)
					continue
				}
				cur = &section{title: title, line: n}
				sections = append(sections, cur)
				continue
			}
			add(line)
		}
	}
	if open != nil {
		sec := ""
		if cur != nil {
			sec = cur.name()
		}
		p.errorf(open.line, sec, "fenced code block is never closed")
	}

	p.plan.Overview = trimBlank(overview)
	if len(sections) == 0 {
		p.errorf(end+1, "", "no ## sections: a plan needs at least one issue")
	}
	for _, s := range sections {
		p.section(s)
	}
}

// section is the raw content of one ## section.
type section struct {
	title      string
	line       int
	block      *fence   // the issue block, if the section starts with one
	closed     bool     // whether the issue block was closed
	blockLines []string // the issue block's TOML
	lines      []string // the body: everything after the issue block
}

// name is how diagnostics name the section until its key is known.
func (s *section) name() string { return fmt.Sprintf("%q", "## "+s.title) }

func (p *parser) frontMatter(lines []string, first int) {
	var fm frontMatter
	doc := strings.Join(lines, "\n")
	if !p.decode(doc, first, "", &fm) {
		return
	}
	p.plan.Title, p.plan.Kickoff = strings.TrimSpace(fm.Title), fm.Kickoff
	if fm.Design != nil {
		p.plan.Design = *fm.Design
	}
	if p.plan.Title == "" {
		p.errorf(1, "", "front matter: title is required")
	}
	if fm.Kickoff <= 0 {
		p.errorf(1, "", "front matter: kickoff is required and must be a positive issue number")
	}
	for i, l := range lines {
		if k, _, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == "design" {
			p.plan.DesignLine = first + i
		}
	}
	if fm.Design != nil {
		if err := config.CheckRepoPath(*fm.Design); err != nil {
			p.errorf(p.plan.DesignLine, "", "front matter: design %q %v", *fm.Design, err)
		}
	}
}

// decode decodes the TOML doc, whose first line is line first of the file,
// into v, reporting what is wrong with it. It reports whether it succeeded.
func (p *parser) decode(doc string, first int, section string, v any) bool {
	err := toml.NewDecoder(strings.NewReader(doc)).DisallowUnknownFields().Decode(v)
	if err == nil {
		return true
	}
	var strict *toml.StrictMissingError
	var de *toml.DecodeError
	switch {
	case errors.As(err, &strict):
		for _, e := range strict.Errors {
			row, _ := e.Position()
			p.errorf(first+row-1, section, "unknown field %q", strings.Join(e.Key(), "."))
		}
	case errors.As(err, &de):
		row, _ := de.Position()
		p.errorf(first+row-1, section, "%v", err)
	default:
		p.errorf(first, section, "%v", err)
	}
	return false
}

func (p *parser) section(s *section) {
	is := Issue{Title: s.title, Line: s.line}
	name := s.name()
	if s.title == "" {
		p.errorf(s.line, "", "a ## heading needs a title")
	}
	if s.block == nil {
		p.errorf(s.line, name, "the section must start with an ```issue TOML block")
	} else if s.closed {
		var ib issueBlock
		if p.decode(strings.Join(s.blockLines, "\n"), s.block.line+1, name, &ib) {
			is.Key, is.Depends = ib.Key, ib.Depends
			if is.Depends == nil {
				is.Depends = []string{}
			}
			if is.Key == "" {
				p.errorf(s.block.line, name, "issue block: key is required")
			}
		}
		is.Body = trimBlank(s.lines)
		if is.Body == "" {
			sec := name
			if is.Key != "" {
				sec = is.Key
			}
			p.errorf(s.line, sec, "the issue body is empty")
		}
	}
	p.plan.Issues = append(p.plan.Issues, is)
}

// check checks keys and dependencies once every section is parsed.
func (p *parser) check() {
	issues := p.plan.Issues
	keys := map[string]int{} // key → index in issues
	for i, is := range issues {
		if is.Key == "" {
			continue // reported already
		}
		if !ValidSlug(is.Key) {
			p.errorf(is.Line, is.Key, "key %q must match %s", is.Key, slugRE)
		}
		if j, dup := keys[is.Key]; dup {
			p.errorf(is.Line, is.Key, "duplicate key: also the key of the section on line %d", issues[j].Line)
			continue
		}
		keys[is.Key] = i
	}
	for _, is := range issues {
		if is.Key == "" {
			continue
		}
		seen := map[string]bool{}
		for _, d := range is.Depends {
			if seen[d] {
				p.errorf(is.Line, is.Key, "duplicate dependency %q", d)
				continue
			}
			seen[d] = true
			switch {
			case strings.HasPrefix(d, "#"):
				if !issueRefRE.MatchString(d) {
					p.errorf(is.Line, is.Key, "dependency %q: an issue reference is # and a positive number", d)
				} else if n, _ := strconv.ParseInt(d[1:], 10, 64); n == p.plan.Kickoff {
					p.errorf(is.Line, is.Key, "dependency %q is the kickoff issue", d)
				}
			case d == is.Key:
				p.errorf(is.Line, is.Key, "depends on itself")
			default:
				if _, ok := keys[d]; !ok {
					p.errorf(is.Line, is.Key, "dependency %q is not a key in this plan", d)
				}
			}
		}
	}
	p.cycles(keys)
}

// cycles reports each cycle among the in-file dependencies, as a path.
func (p *parser) cycles(keys map[string]int) {
	const (
		unseen = iota
		onPath
		done
	)
	state := map[string]int{}
	var stack []string
	var visit func(k string)
	visit = func(k string) {
		state[k] = onPath
		stack = append(stack, k)
		for _, d := range p.plan.Issues[keys[k]].Depends {
			if _, ok := keys[d]; !ok || d == k {
				continue // reported already
			}
			switch state[d] {
			case unseen:
				visit(d)
			case onPath:
				i := len(stack) - 1
				for stack[i] != d {
					i--
				}
				cycle := append(append([]string{}, stack[i:]...), d)
				p.errorf(p.plan.Issues[keys[d]].Line, d, "dependency cycle: %s", strings.Join(cycle, " -> "))
			}
		}
		stack = stack[:len(stack)-1]
		state[k] = done
	}
	for _, is := range p.plan.Issues {
		if is.Key != "" && state[is.Key] == unseen {
			visit(is.Key)
		}
	}
}

func blank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// trimBlank joins lines, without the blank lines at either end.
func trimBlank(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
