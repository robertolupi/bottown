package policy

import (
	"slices"
	"strings"
)

// Run roles.
const (
	Implement = "implement"
	Verify    = "verify"
)

// RunScope confines a run key to one repository and to its role. It applies
// after the base Policy: a request must pass both.
type RunScope struct {
	Org, Repo string
	Role      string // Implement | Verify
}

// verifyWrites are the only API writes a verify run may make inside its
// repository: commenting and reviewing. Everything else that changes the
// repository (pushes, PRs, issues, files, branches, releases) is denied.
var verifyWrites = []Rule{
	{Name: "comment", Methods: []string{"POST"}, Pattern: "/repos/*/*/issues/*/comments"},
	{Name: "comment-edit", Methods: []string{"PATCH", "DELETE"}, Pattern: "/repos/*/*/issues/comments/*"},
	{Name: "review", Methods: []string{"POST"}, Pattern: "/repos/*/*/pulls/*/reviews/**"},
	{Name: "reaction", Methods: []string{"POST", "DELETE"}, Pattern: "/repos/*/*/issues/**/reactions"},
}

// implementDenied are writes an implement run may not make: reviewing is
// the verifier's job.
var implementDenied = []Rule{
	{Name: "role-implement-review", Methods: writes, Pattern: "/repos/*/*/pulls/*/reviews/**"},
	{Name: "role-implement-review-requests", Methods: writes, Pattern: "/repos/*/*/pulls/*/requested_reviewers"},
}

// DecideAPI applies the scope to an API request (path below /api/v1,
// cleaned by Clean).
func (s RunScope) DecideAPI(method, apiPath string) Decision {
	method = strings.ToUpper(method)
	segs := split(apiPath)
	if len(segs) >= 1 && segs[0] == "repos" {
		if len(segs) < 3 || segs[1] != strings.ToLower(s.Org) || segs[2] != strings.ToLower(s.Repo) {
			return Decision{Allow: false, Rule: "run-repo"}
		}
	} else if method != "GET" {
		// Outside /repos only reads (own user, users, version, settings).
		return Decision{Allow: false, Rule: "run-repo"}
	}
	if method == "GET" || method == "HEAD" {
		return Decision{Allow: true, Rule: "run-read"}
	}
	switch s.Role {
	case Verify:
		for _, r := range verifyWrites {
			if slices.Contains(r.Methods, method) && match(split(r.Pattern), segs) {
				return Decision{Allow: true, Rule: "run-verify-" + r.Name}
			}
		}
		return Decision{Allow: false, Rule: "role-verify"}
	case Implement:
		for _, r := range implementDenied {
			if slices.Contains(r.Methods, method) && match(split(r.Pattern), segs) {
				return Decision{Allow: false, Rule: r.Name}
			}
		}
		return Decision{Allow: true, Rule: "run-implement"}
	}
	return Decision{Allow: false, Rule: "run-role-unknown"}
}

// DecideGit applies the scope to a git smart-HTTP request. gitPath is the
// path below /git (e.g. /bottown/demo.git/info/refs); service is the
// "service" query parameter of info/refs, if any.
func (s RunScope) DecideGit(gitPath, service string) Decision {
	segs := split(gitPath)
	if len(segs) < 2 || segs[0] != strings.ToLower(s.Org) || strings.TrimSuffix(segs[1], ".git") != strings.ToLower(s.Repo) {
		return Decision{Allow: false, Rule: "run-repo"}
	}
	push := service == "git-receive-pack" || (len(segs) > 0 && segs[len(segs)-1] == "git-receive-pack")
	if push && s.Role != Implement {
		return Decision{Allow: false, Rule: "role-verify-push"}
	}
	if push {
		return Decision{Allow: true, Rule: "run-implement-push"}
	}
	return Decision{Allow: true, Rule: "run-fetch"}
}
