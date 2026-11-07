package policy

import (
	"slices"
	"strings"
)

// The wiki is reached only through bottown's wiki store (`bottown wiki`,
// /v1/wiki), which decides each read and write by role and path and keeps
// the generated blocks. Forgejo's wiki API and the wiki's git repository
// are the owner's alone: an agent going around the store could write a
// page unchecked, or read files a round keeps from it (#315).

// wikiAPI is Forgejo's wiki API, closed to everyone but the owner.
var wikiAPI = Rule{Name: "wiki-via-store", Pattern: "/repos/*/*/wiki/**"}

// IsWikiGit reports whether a git proxy path (below /git) is a wiki's
// repository: /org/repo.wiki.git/...
func IsWikiGit(gitPath string) bool {
	segs := split(gitPath)
	return len(segs) >= 2 && strings.HasSuffix(strings.TrimSuffix(segs[1], ".git"), ".wiki")
}

// Wiki principals: the owner outside a run, an actor outside a run (Actor),
// or a run, by its role.
const (
	WikiOwner = "owner"
	WikiActor = "actor"
)

// WikiRule grants reads, writes or both on wiki paths matching Pattern
// (segments, "*" one, a trailing "**" any number) to the principals in Who
// (WikiOwner, WikiActor, or a run role). A delete is a write.
type WikiRule struct {
	Name    string
	Who     []string
	Pattern string
	Read    bool
	Write   bool
}

// WikiRules are the store's rules, the first match wins; a path no rule
// matches is denied. #311 (garden runs) and #315 (round files) add theirs
// before the defaults.
var WikiRules = []WikiRule{
	{Name: "wiki-owner", Who: []string{WikiOwner}, Pattern: "/**", Read: true, Write: true},
	{Name: "wiki-read", Who: []string{WikiActor, Implement, Verify, Discuss, Plan, Retro}, Pattern: "/**", Read: true},
}

// DecideWiki decides a wiki store access by who (WikiOwner, WikiActor or a
// run role) to the file path p (relative to the wiki's root).
func DecideWiki(who string, write bool, p string) Decision {
	segs := split(p)
	for _, r := range WikiRules {
		if !slices.Contains(r.Who, who) || !match(split(r.Pattern), segs) {
			continue
		}
		if write && !r.Write || !write && !r.Read {
			return Decision{Allow: false, Rule: r.Name}
		}
		return Decision{Allow: true, Rule: r.Name}
	}
	return Decision{Allow: false, Rule: "wiki-default"}
}
