package server

import (
	"net/http"
	"regexp"
	"strings"
)

// actionRef matches a Forgejo close or reopen keyword before an issue
// reference: "#N", "repo#N", "owner/repo#N" or a link to an issue or pull
// request. Forgejo records each as a cross-reference from the PR it is
// posted on, and at merge applies only the PR's latest one per issue
// (quirk 8 in docs/history/bootstrap.md), so a comment saying "reopen #N"
// overrides the description's "Closes #N". Forgejo matches on the text
// Markdown renders to, so "**Reopen** #N" and "<b>Reopen</b> #N" count:
// emphasis marks and HTML tags may stand around the keyword and between
// it and the reference. It is looser than Forgejo's own match (any
// whitespace after the keyword, not one space), so it refuses a little
// more rather than missing one.
var actionRef = regexp.MustCompile(`(?i)(?:^|[\s(\[*_~>])((?:close[sd]?|fix(?:es|ed)?|resolve[sd]?|reopen(?:s|ed)?):?)(?:[\s*_~]|<[^>\n]*>)+((?:[\w.-]+/)?[\w.-]+#\d+|#\d+|<?https?://[^\s>]+/(?:issues|pulls)/\d+)`)

// fenceOpen matches a line that opens a fenced code block: up to three
// spaces, then three or more backticks or tildes. A backtick fence's info
// string may not hold a backtick.
var fenceOpen = regexp.MustCompile("^ {0,3}(`{3,}[^`]*$|~{3,})")

// actionRefs returns the close and reopen references in a comment's body,
// outside code.
func actionRefs(body string) []string {
	var refs []string
	for _, m := range actionRef.FindAllStringSubmatch(stripCode(body), -1) {
		refs = append(refs, m[1]+" "+strings.Trim(m[2], "<"))
	}
	return refs
}

// stripCode blanks the parts of a Markdown body where Forgejo does not
// look for references: fenced code blocks and inline code spans. It
// follows CommonMark where that is simple, and otherwise keeps text it is
// unsure of, so that an ambiguous span is checked rather than hidden: a
// backslash-escaped backtick does not delimit a span, a span closes only
// on a backtick run of its opener's length, a span does not continue past
// its line, and a fence closes only on a run of its own character at
// least as long as the opener. A fence left open runs to the end, as in
// CommonMark. Backslash escapes outside code are removed, since Forgejo
// sees the escaped character.
func stripCode(body string) string {
	var out strings.Builder
	var fence string // the open fence's run, or "" outside a fence
	for _, line := range strings.Split(body, "\n") {
		if fence != "" {
			if t := strings.TrimLeft(line, " "); len(line)-len(t) <= 3 && strings.HasPrefix(t, fence) &&
				strings.TrimSpace(strings.TrimLeft(t, fence[:1])) == "" {
				fence = ""
			}
			out.WriteString("\n")
			continue
		}
		if m := fenceOpen.FindStringSubmatch(line); m != nil {
			fence = m[1][:len(m[1])-len(strings.TrimLeft(m[1], m[1][:1]))]
			out.WriteString("\n")
			continue
		}
		out.WriteString(stripCodeSpans(line))
		out.WriteString("\n")
	}
	return out.String()
}

// stripCodeSpans blanks the inline code spans in one line and removes
// backslash escapes outside them.
func stripCodeSpans(line string) string {
	var out strings.Builder
	for i := 0; i < len(line); {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line) && isASCIIPunct(line[i+1]):
			out.WriteByte(line[i+1])
			i += 2
		case c == '`':
			n := backticks(line[i:])
			if end := closingRun(line[i+n:], n); end >= 0 {
				out.WriteByte(' ')
				i += n + end + n
				continue
			}
			out.WriteString(line[i : i+n])
			i += n
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// closingRun returns where in s the first run of exactly n backticks
// starts, or -1. Backslashes do not escape inside a code span.
func closingRun(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		k := backticks(s[i:])
		if k == n {
			return i
		}
		i += k
	}
	return -1
}

// backticks returns the length of the backtick run that starts s.
func backticks(s string) int {
	return len(s) - len(strings.TrimLeft(s, "`"))
}

func isASCIIPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// refuseActionRefs refuses a comment or review body for a pull request
// that holds a close or reopen reference: posted on the PR, it would
// decide what merging the PR does to that issue.
func refuseActionRefs(body string) error {
	refs := actionRefs(body)
	if len(refs) == 0 {
		return nil
	}
	return ferr(http.StatusBadRequest,
		"the body says %q: on a pull request Forgejo reads a close/fix/resolve or reopen keyword before an issue reference as what merging the PR does to that issue, overriding the description's link; reword it without the keyword right before the reference (\"#N should be reopened after merging\"), or put the reference in backticks",
		strings.Join(refs, `", "`))
}
