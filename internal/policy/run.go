package policy

import (
	"slices"
	"strings"
)

// Run roles.
const (
	Implement = "implement"
	Verify    = "verify"
	// Discuss runs review and discuss an issue without implementing it:
	// they read the repository and comment, like a verifier without a
	// verdict.
	Discuss = "discuss"
	// Plan runs write a milestone plan file and open a pull request for it,
	// like an implement run that may not file issues: the plan's issues are
	// created by the owner's `milestone apply`, never by the planner.
	Plan = "plan"
)

// RunScope confines a run key to one repository and to its role. It applies
// after the base Policy: a request must pass both.
type RunScope struct {
	Org, Repo string
	Role      string // Implement | Verify | Discuss | Plan
}

// verifyWrites are the only API writes a verify or discuss run may make
// directly inside its repository: commenting. Reviews go through the review_submit
// MCP tool, which pins them to the run's commit; everything else that
// changes the repository (pushes, PRs, issues, files, branches, releases) is
// denied.
var verifyWrites = []Rule{
	{Name: "comment", Methods: []string{"POST"}, Pattern: "/repos/*/*/issues/*/comments"},
	{Name: "comment-edit", Methods: []string{"PATCH", "DELETE"}, Pattern: "/repos/*/*/issues/comments/*"},
	{Name: "reaction", Methods: []string{"POST", "DELETE"}, Pattern: "/repos/*/*/issues/**/reactions"},
}

// reviewWrites must go through review_submit in any run.
var reviewWrites = Rule{Name: "review-via-tool", Methods: writes, Pattern: "/repos/*/*/pulls/*/reviews/**"}

// implementDenied are writes an implement run may not make: reviewing is
// the verifier's job.
var implementDenied = []Rule{
	{Name: "role-implement-review", Methods: writes, Pattern: "/repos/*/*/pulls/*/reviews/**"},
	{Name: "role-implement-review-requests", Methods: writes, Pattern: "/repos/*/*/pulls/*/requested_reviewers"},
}

// planNoIssues keeps a plan run from filing issues: the plan proposes them
// in a file, and only the owner's apply creates them.
var planNoIssues = Rule{Name: "role-plan-no-issues", Methods: []string{"POST"}, Pattern: "/repos/*/*/issues"}

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
	case Verify, Discuss:
		if slices.Contains(reviewWrites.Methods, method) && match(split(reviewWrites.Pattern), segs) {
			return Decision{Allow: false, Rule: reviewWrites.Name}
		}
		for _, r := range verifyWrites {
			if slices.Contains(r.Methods, method) && match(split(r.Pattern), segs) {
				return Decision{Allow: true, Rule: "run-" + s.Role + "-" + r.Name}
			}
		}
		return Decision{Allow: false, Rule: "role-" + s.Role}
	case Implement, Plan:
		if s.Role == Plan && method == "POST" && match(split(planNoIssues.Pattern), segs) {
			return Decision{Allow: false, Rule: planNoIssues.Name}
		}
		for _, r := range implementDenied {
			if slices.Contains(r.Methods, method) && match(split(r.Pattern), segs) {
				return Decision{Allow: false, Rule: r.Name}
			}
		}
		return Decision{Allow: true, Rule: "run-" + s.Role}
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
	if push && s.Role != Implement && s.Role != Plan {
		return Decision{Allow: false, Rule: "role-" + s.Role + "-push"}
	}
	if push {
		return Decision{Allow: true, Rule: "run-" + s.Role + "-push"}
	}
	return Decision{Allow: true, Rule: "run-fetch"}
}
