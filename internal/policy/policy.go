// Package policy decides which Forgejo API calls an actor may make through
// bottown's proxy. Token scopes and team permissions already bound what each
// bot can do, so the default is allow; the rules deny the policy-carrying
// writes that bottown performs itself (merge, commit status, protection,
// labels, dependencies, repository and org administration).
package policy

import (
	"path"
	"slices"
	"strings"
)

// Rule matches a request by method and path pattern. Patterns are matched
// segment by segment against the path below /api/v1: "*" matches one
// segment and a trailing "**" matches zero or more.
type Rule struct {
	Name    string
	Methods []string // empty = any method
	Pattern string
	Allow   bool
}

// Policy is an ordered rule list; the first match wins.
type Policy struct {
	// Exempt actors (the owner) skip the rules entirely.
	Exempt []string
	Rules  []Rule
}

// Decision is the outcome for one request.
type Decision struct {
	Allow bool
	Rule  string // name of the matching rule, or "default"
}

var writes = []string{"POST", "PUT", "PATCH", "DELETE"}

// Default returns the step-1 rule set.
func Default(owner string) Policy {
	return Policy{
		Exempt: []string{owner},
		Rules: []Rule{
			{Name: "admin", Pattern: "/admin/**"},
			{Name: "merge", Methods: writes, Pattern: "/repos/*/*/pulls/*/merge"},
			{Name: "commit-status", Methods: writes, Pattern: "/repos/*/*/statuses/**"},
			{Name: "branch-protection", Methods: writes, Pattern: "/repos/*/*/branch_protections/**"},
			{Name: "issue-labels", Methods: writes, Pattern: "/repos/*/*/issues/*/labels/**"},
			{Name: "repo-labels", Methods: writes, Pattern: "/repos/*/*/labels/**"},
			{Name: "issue-dependencies", Methods: writes, Pattern: "/repos/*/*/issues/*/dependencies/**"},
			{Name: "issue-blocks", Methods: writes, Pattern: "/repos/*/*/issues/*/blocks/**"},
			{Name: "repo-settings", Methods: []string{"PATCH", "DELETE"}, Pattern: "/repos/*/*"},
			{Name: "collaborators", Methods: writes, Pattern: "/repos/*/*/collaborators/**"},
			{Name: "hooks", Pattern: "/repos/*/*/hooks/**"},
			{Name: "deploy-keys", Pattern: "/repos/*/*/keys/**"},
			{Name: "transfer", Methods: writes, Pattern: "/repos/*/*/transfer/**"},
			{Name: "user-settings", Methods: writes, Pattern: "/user/**"},
			{Name: "user-tokens", Pattern: "/users/*/tokens/**"},
			{Name: "orgs", Methods: writes, Pattern: "/orgs/**"},
			{Name: "teams", Methods: writes, Pattern: "/teams/**"},
		},
	}
}

// Clean normalises an API path for matching and forwarding. ok is false for
// paths that do not round-trip (dot segments, doubled slashes), which are
// rejected rather than guessed at.
func Clean(p string) (string, bool) {
	if p == "" || p[0] != '/' {
		p = "/" + p
	}
	c := path.Clean(p)
	if c != strings.TrimSuffix(p, "/") && c != p {
		return "", false
	}
	return c, true
}

// Decide applies the policy to actor's request. path must come from Clean.
func (p Policy) Decide(actor, method, apiPath string) Decision {
	if slices.Contains(p.Exempt, actor) {
		return Decision{Allow: true, Rule: "exempt"}
	}
	segs := split(apiPath)
	method = strings.ToUpper(method)
	for _, r := range p.Rules {
		if len(r.Methods) > 0 && !slices.Contains(r.Methods, method) {
			continue
		}
		if match(split(r.Pattern), segs) {
			return Decision{Allow: r.Allow, Rule: r.Name}
		}
	}
	return Decision{Allow: true, Rule: "default"}
}

func split(p string) []string {
	p = strings.Trim(strings.ToLower(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func match(pat, segs []string) bool {
	for i, ps := range pat {
		if ps == "**" && i == len(pat)-1 {
			return true
		}
		if i >= len(segs) {
			return false
		}
		if ps != "*" && ps != segs[i] {
			return false
		}
	}
	return len(pat) == len(segs)
}
