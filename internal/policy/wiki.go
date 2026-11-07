package policy

import (
	"slices"
	"strings"

	"github.com/robertolupi/bottown/internal/wiki"
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
// matches is denied. #311 (garden runs) adds its rules before the defaults;
// debate round files are decided by DecideWikiRound, and a windowed
// retro's page by DecideWikiRetroPage, before these.
var WikiRules = []WikiRule{
	{Name: "wiki-owner", Who: []string{WikiOwner}, Pattern: "/**", Read: true, Write: true},
	{Name: "wiki-read", Who: []string{WikiActor, Implement, Verify, Discuss, Plan, Retro, Consolidate}, Pattern: "/**", Read: true},
}

// WikiRound is a run's place in a debate round, for the round file rules.
type WikiRound struct {
	Debate      string // the debate's name (wiki.Debate.Name)
	Round       int
	Actor       string
	Raw         bool // earlier rounds' answers are readable, not only their consolidations
	Consolidate bool // the round's consolidate run, not one of its discuss runs
}

// DecideWikiRound decides an access to a debate's round file
// (reviews/<debate>/r<N>-<author>.md), ahead of WikiRules; ok is false for
// any other path, which WikiRules decide. in is the run's round, nil
// outside a debate; consolidated reports whether a debate's round n has
// its consolidation.
//
// Rounds are independent: a discuss run writes only its own answer and
// reads, of its debate, only its own answer and earlier rounds'
// consolidations (their answers too when the round allows), never another
// actor's answer in its round. Its consolidate run writes only the
// round's consolidation and reads the round and those before it. Nobody
// else but the owner writes round files, and they read a round only once
// it is consolidated.
func DecideWikiRound(who string, in *WikiRound, write bool, p string, consolidated func(debate string, n int) bool) (d Decision, ok bool) {
	r, isRound := wiki.ParseRound(p)
	if !isRound {
		return Decision{}, false
	}
	if who == WikiOwner {
		return Decision{Allow: true, Rule: "wiki-owner"}, true
	}
	own := in != nil && r.Debate == in.Debate
	if write {
		switch {
		case own && in.Consolidate && r.N == in.Round && r.Author == wiki.Consolidated:
			return Decision{Allow: true, Rule: "wiki-round-consolidate"}, true
		case own && !in.Consolidate && r.N == in.Round && r.Author == in.Actor:
			return Decision{Allow: true, Rule: "wiki-round-answer"}, true
		}
		return Decision{Allow: false, Rule: "wiki-round-write"}, true
	}
	switch {
	case own && in.Consolidate:
		return Decision{Allow: r.N <= in.Round, Rule: "wiki-round-consolidate"}, true
	case own && r.N == in.Round && r.Author == in.Actor:
		return Decision{Allow: true, Rule: "wiki-round-answer"}, true
	case own && r.N < in.Round && (r.Author == wiki.Consolidated || in.Raw):
		return Decision{Allow: true, Rule: "wiki-round-earlier"}, true
	case own:
		return Decision{Allow: false, Rule: "wiki-round-independent"}, true
	case consolidated(r.Debate, r.N):
		return Decision{Allow: true, Rule: "wiki-round-closed"}, true
	}
	return Decision{Allow: false, Rule: "wiki-round-open"}, true
}

// DecideWikiRetroPage decides an access by who to file p for a windowed
// retro run (#383) whose page is page, ahead of WikiRules: the run may
// read and write its own page, the one bottown named when it created the
// run, and no other. ok is false for any other principal or file, which
// WikiRules decide.
func DecideWikiRetroPage(who, page string, p string) (d Decision, ok bool) {
	if who != Retro || page == "" || p != page+".md" {
		return Decision{}, false
	}
	return Decision{Allow: true, Rule: "wiki-retro-page"}, true
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
