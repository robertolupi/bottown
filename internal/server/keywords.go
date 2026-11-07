package server

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// actionRef matches a Forgejo close or reopen keyword before an issue
// reference: "#N", "repo#N", "owner/repo#N" or a link to an issue or pull
// request. Forgejo records each as a cross-reference from the PR it is
// posted on, and at merge applies only the PR's latest one per issue
// (quirk 8 in docs/history/bootstrap.md), so a comment saying "reopen #N"
// overrides the description's "Closes #N". It runs on the text
// markdownText returns, where emphasis is gone but HTML blocks are not, so
// it lets emphasis marks and HTML tags stand around the keyword and
// between it and the reference. It is looser than Forgejo's own match
// (any whitespace after the keyword, not one space), so it refuses a
// little more rather than missing one.
var actionRef = regexp.MustCompile(`(?i)(?:^|[\s(\[*_~>])((?:close[sd]?|fix(?:es|ed)?|resolve[sd]?|reopen(?:s|ed)?):?)(?:[\s*_~]|<[^>\n]*>)+((?:[\w.-]+/)?[\w.-]+#\d+|#\d+|<?https?://[^\s>]+/(?:issues|pulls)/\d+)`)

// actionRefs returns the close and reopen references in a comment's body,
// outside code.
func actionRefs(body string) []string {
	var refs []string
	for _, m := range actionRef.FindAllStringSubmatch(markdownText(body), -1) {
		refs = append(refs, m[1]+" "+strings.Trim(m[2], "<"))
	}
	return refs
}

// markdown parses bodies as Forgejo does: goldmark, with GitHub's
// extensions.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// markdownText returns the text of a Markdown body where Forgejo looks for
// references. Parsing it as Forgejo does puts code where CommonMark puts
// it, inside list items and block quotes too, and drops code spans and
// fenced or indented code blocks. Like Forgejo's mdstripper it keeps link
// destinations and autolinks. To refuse rather than miss a reference it
// also keeps what Forgejo skips or reads apart: link text, HTML blocks,
// and text on both sides of emphasis or an inline HTML tag, joined by a
// space; and it removes backslash escapes, which Forgejo would leave
// before the escaped character.
func markdownText(body string) string {
	src := []byte(body)
	var out strings.Builder
	_ = ast.Walk(markdown.Parser().Parse(text.NewReader(src)), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if n.Type() == ast.TypeBlock {
				out.WriteString("\n")
			}
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.CodeSpan, *ast.FencedCodeBlock, *ast.CodeBlock:
			out.WriteString(" ")
			if n.Type() == ast.TypeBlock {
				out.WriteString("\n")
			}
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			for i := 0; i < v.Lines().Len(); i++ {
				seg := v.Lines().At(i)
				out.Write(seg.Value(src))
			}
		case *ast.Text:
			out.WriteString(unescape(string(v.Segment.Value(src))))
			if v.SoftLineBreak() || v.HardLineBreak() {
				out.WriteString("\n")
			}
		case *ast.String:
			out.Write(v.Value)
		case *ast.RawHTML:
			out.WriteString(" ")
		case *ast.Link:
			out.WriteString(" " + string(v.Destination) + " ")
		case *ast.AutoLink:
			out.WriteString(" " + string(v.URL(src)) + " ")
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return out.String()
}

// unescape removes the backslashes that escape ASCII punctuation.
func unescape(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]) {
			i++
		}
		out.WriteByte(s[i])
	}
	return out.String()
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
