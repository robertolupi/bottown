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
// overrides the description's "Closes #N". It is looser than Forgejo's own
// match (any whitespace after the keyword, not one space), so it refuses
// a little more rather than missing one.
var actionRef = regexp.MustCompile(`(?i)(?:^|[\s(\[])((?:close[sd]?|fix(?:es|ed)?|resolve[sd]?|reopen(?:s|ed)?):?\s+(?:(?:[\w.-]+/)?[\w.-]+#\d+|#\d+|https?://\S+/(?:issues|pulls)/\d+))`)

// codeSpan matches fenced code blocks and inline code, where Forgejo does
// not look for references.
var codeSpan = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~|`[^`\n]*`")

// actionRefs returns the close and reopen references in a comment's body,
// outside code.
func actionRefs(body string) []string {
	var refs []string
	for _, m := range actionRef.FindAllStringSubmatch(codeSpan.ReplaceAllString(body, " "), -1) {
		refs = append(refs, strings.Join(strings.Fields(m[1]), " "))
	}
	return refs
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
