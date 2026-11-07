package server

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/robertolupi/bottown/internal/secrets"
)

// RetroFlow is the flow a retro run on issue or pull request N looks back
// on. The launcher selects the runs of the evidence with it, and the
// server the implementers for the cross-family rule, so the two agree on
// what the flow is:
//
//   - N an issue: issue N's flow, its issue/N branch and pull requests.
//   - N a pull request from an issue/M branch: issue M's flow, of which
//     N is one pull request.
//   - N a pull request from another branch: that pull request's runs,
//     and those on its head branch.
type RetroFlow struct {
	// Target is N, the issue or pull request the retro is on and posts to.
	Target int64
	// Issue is the issue whose flow it is (N, or M); 0 for a pull request
	// not on an issue/M branch.
	Issue int64
	// PR is N when N is a pull request.
	PR int64
	// Branch is the flow's branch: issue/<Issue>, or the pull request's
	// head.
	Branch string
}

// NewRetroFlow is the flow of a retro on n, a pull request from branch
// head when isPR.
func NewRetroFlow(n int64, isPR bool, head string) RetroFlow {
	if !isPR {
		return RetroFlow{Target: n, Issue: n, Branch: IssueBranch(n)}
	}
	f := RetroFlow{Target: n, PR: n, Branch: head}
	if m, ok := IssueBranchNumber(head); ok {
		f.Issue = m
	}
	return f
}

// IssueBranch is the branch implement runs push issue n's work to.
func IssueBranch(n int64) string { return fmt.Sprintf("issue/%d", n) }

// IssueBranchNumber is the issue of an issue/<n> branch.
func IssueBranchNumber(ref string) (int64, bool) {
	v, ok := strings.CutPrefix(ref, "issue/")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n > 0
}

// String names the flow for people: "#7", "!9 of #7", or "!9".
func (f RetroFlow) String() string {
	switch {
	case f.PR > 0 && f.Issue > 0:
		return fmt.Sprintf("!%d of #%d", f.PR, f.Issue)
	case f.PR > 0:
		return fmt.Sprintf("!%d", f.PR)
	}
	return fmt.Sprintf("#%d", f.Issue)
}

// PRs is the set of the flow's pull requests: the target, when it is one;
// those Forgejo lists from the flow's branch (forge); and those runs record
// on it (a verify run has the PR and the branch), ended and purged runs
// included.
func (f RetroFlow) PRs(forge []int64, rs []secrets.Run) map[int64]bool {
	prs := map[int64]bool{}
	if f.PR > 0 {
		prs[f.PR] = true
	}
	for _, p := range forge {
		prs[p] = true
	}
	for _, r := range rs {
		if r.PR > 0 && f.Branch != "" && r.Branch == f.Branch {
			prs[r.PR] = true
		}
	}
	return prs
}

// Has is whether run r belongs to the flow, whose pull requests are prs:
// it worked the issue (implement, plan, discuss), or worked on the flow's
// branch (implement, verify), or verified or discussed one of its pull
// requests (a discuss run records its target, issue or PR, as Issue).
func (f RetroFlow) Has(r secrets.Run, prs map[int64]bool) bool {
	return (f.Issue > 0 && r.Issue == f.Issue) || (f.Branch != "" && r.Branch == f.Branch) ||
		prs[r.Issue] || prs[r.PR]
}
