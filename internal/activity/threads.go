package activity

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/server"
)

// Runs are grouped into threads. Implement and verify runs for the same
// issue and PR share a flow thread; distinct PRs of one issue form
// separate threads; implement runs before the PR group under the issue and
// join the PR's thread once it exists. Discuss and retro runs stand alone,
// one thread each. The web UI's History, the timeline and the statistics
// all group runs with Group, so they agree on what a thread is.

// ThreadKey is the key of the flow thread for issue and PR; with neither,
// the run's own thread.
func ThreadKey(issue, pr int64, runID string) string {
	switch {
	case issue > 0 && pr > 0:
		return fmt.Sprintf("flow:issue:%d:pr:%d", issue, pr)
	case issue > 0:
		return fmt.Sprintf("flow:issue:%d", issue)
	case pr > 0:
		return fmt.Sprintf("flow:pr:%d", pr)
	default:
		return "flow:run:" + runID
	}
}

// Standalone is whether runs of kind stand alone, one thread each, outside
// the flows: discuss and retro runs look at an issue without working it,
// so they neither join its flow nor keep it active.
func Standalone(kind string) bool { return kind == "discuss" || kind == "retro" }

// StandaloneKey is the thread of a standalone run.
func StandaloneKey(kind, runID string) string { return kind + ":" + runID }

// IsStandaloneKey is whether key is a standalone run's thread.
func IsStandaloneKey(key string) bool {
	return strings.HasPrefix(key, "discuss:") || strings.HasPrefix(key, "retro:")
}

// BranchIssue is the issue an issue/<n> branch works, or 0.
func BranchIssue(ref string) int64 {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "refs/heads/")
	if strings.HasPrefix(ref, "issue/") {
		var n int64
		if _, err := fmt.Sscanf(ref, "issue/%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

var summaryPR = regexp.MustCompile(`!(\d+)\b`)

// SummaryPR is the first PR a run's handover summary names (!<n>), or 0.
func SummaryPR(summary string) int64 {
	m := summaryPR.FindStringSubmatch(summary)
	if len(m) >= 2 {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// PROpen is whether pr is open; an unknown PR (nil) counts as open.
func PROpen(pr *server.PRItem) bool {
	if pr == nil {
		return true
	}
	if pr.Merged || pr.MergedAt != nil || pr.ClosedAt != nil {
		return false
	}
	return pr.State == "" || pr.State == string(sdk.StateOpen)
}

// RunThread is the thread a run belongs to.
type RunThread struct {
	Issue int64
	PR    int64
	Key   string
}

// Grouping is how a set of runs and the repository's PRs group into
// threads.
type Grouping struct {
	// Runs are each run's thread, by run id.
	Runs map[string]RunThread
	// PRsForIssue are each issue's PRs, oldest first.
	PRsForIssue map[int64][]int64
	// PRCutoffs are, for each of an issue's PRs but an open last one, the
	// time after which the issue's work belongs to a later PR.
	PRCutoffs map[int64]time.Time
	prs       map[int64]*server.PRItem
}

// Group groups runs into threads, with the repository's PRs.
func Group(runs []server.RunItem, prs []*server.PRItem) Grouping {
	g := Grouping{
		Runs:        make(map[string]RunThread, len(runs)),
		PRsForIssue: map[int64][]int64{},
		PRCutoffs:   map[int64]time.Time{},
		prs:         make(map[int64]*server.PRItem, len(prs)),
	}
	issueOfPR := make(map[int64]int64)
	for _, pr := range prs {
		if pr == nil || pr.Number <= 0 {
			continue
		}
		g.prs[pr.Number] = pr
		if n := BranchIssue(pr.HeadBranch); n > 0 {
			issueOfPR[pr.Number] = n
		}
	}

	hasExplicitPRRun := make(map[int64]bool)
	earliestRunForPR := make(map[int64]time.Time)
	for _, sr := range runs {
		if sr.PR > 0 {
			hasExplicitPRRun[sr.PR] = true
			if t, ok := earliestRunForPR[sr.PR]; !ok || (!sr.Created.IsZero() && sr.Created.Before(t)) {
				earliestRunForPR[sr.PR] = sr.Created
			}
			if issueOfPR[sr.PR] == 0 {
				if sr.Issue > 0 {
					issueOfPR[sr.PR] = sr.Issue
				} else if n := BranchIssue(sr.Branch); n > 0 {
					issueOfPR[sr.PR] = n
				}
			}
		}
	}

	for _, sr := range runs {
		if sr.Kind == "implement" && sr.Issue > 0 {
			if mentioned := SummaryPR(sr.Digest.Summary); mentioned > 0 && issueOfPR[mentioned] == 0 {
				if g.prs[mentioned] != nil || hasExplicitPRRun[mentioned] {
					issueOfPR[mentioned] = sr.Issue
				}
			}
		}
	}

	for prIdx, issNum := range issueOfPR {
		if issNum > 0 && prIdx > 0 {
			g.PRsForIssue[issNum] = append(g.PRsForIssue[issNum], prIdx)
		}
	}

	prStartTime := func(prIdx int64) time.Time {
		if pr := g.prs[prIdx]; pr != nil && pr.Created != nil && !pr.Created.IsZero() {
			return *pr.Created
		}
		return earliestRunForPR[prIdx]
	}

	for issNum := range g.PRsForIssue {
		cands := g.PRsForIssue[issNum]
		sort.Slice(cands, func(i, j int) bool {
			ti, tj := prStartTime(cands[i]), prStartTime(cands[j])
			if !ti.Equal(tj) && !ti.IsZero() && !tj.IsZero() {
				return ti.Before(tj)
			}
			return cands[i] < cands[j]
		})
		g.PRsForIssue[issNum] = cands
	}

	for _, cands := range g.PRsForIssue {
		for idx, prIdx := range cands {
			pr := g.prs[prIdx]
			if idx == len(cands)-1 && PROpen(pr) {
				continue
			}
			var cutoff time.Time
			if pr != nil {
				if pr.MergedAt != nil && pr.MergedAt.After(cutoff) {
					cutoff = *pr.MergedAt
				}
				if pr.ClosedAt != nil && pr.ClosedAt.After(cutoff) {
					cutoff = *pr.ClosedAt
				}
				if cutoff.IsZero() {
					if pr.Updated != nil && pr.Updated.After(cutoff) {
						cutoff = *pr.Updated
					}
					if pr.Created != nil && pr.Created.After(cutoff) {
						cutoff = *pr.Created
					}
				}
			}
			for _, r := range runs {
				if Standalone(r.Kind) {
					continue
				}
				if r.PR == prIdx || (r.Kind == "implement" && SummaryPR(r.Digest.Summary) == prIdx) {
					if r.Created.After(cutoff) {
						cutoff = r.Created
					}
					if r.Ended != nil && r.Ended.After(cutoff) {
						cutoff = *r.Ended
					}
				}
			}
			g.PRCutoffs[prIdx] = cutoff
		}
	}

	for _, sr := range runs {
		var issueNum, prNum int64
		var key string
		switch sr.Kind {
		case "discuss", "retro":
			issueNum, prNum = sr.Issue, sr.PR
			key = StandaloneKey(sr.Kind, sr.ID)
		case "verify":
			prNum = sr.PR
			issueNum = sr.Issue
			if issueNum <= 0 && prNum > 0 {
				issueNum = issueOfPR[prNum]
			}
			if issueNum <= 0 {
				issueNum = BranchIssue(sr.Branch)
			}
			key = ThreadKey(issueNum, prNum, sr.ID)
		default: // "implement"
			issueNum = sr.Issue
			if issueNum <= 0 {
				issueNum = BranchIssue(sr.Branch)
			}
			prNum = sr.PR
			if issueNum <= 0 && prNum > 0 {
				issueNum = issueOfPR[prNum]
			}
			if prNum <= 0 && issueNum > 0 {
				prNum = g.PRAt(issueNum, sr.Created, SummaryPR(sr.Digest.Summary))
			}
			key = ThreadKey(issueNum, prNum, sr.ID)
		}
		g.Runs[sr.ID] = RunThread{Issue: issueNum, PR: prNum, Key: key}
	}
	return g
}

// PRAt is the PR whose thread an issue's work at t, which names no PR of
// its own, belongs to: mentioned, if it is one of the issue's PRs, else
// the first PR whose cutoff t does not pass, else an open last PR; 0 when
// the work comes before them all or the issue has none.
func (g Grouping) PRAt(issue int64, t time.Time, mentioned int64) int64 {
	cands := g.PRsForIssue[issue]
	if mentioned > 0 && slices.Contains(cands, mentioned) {
		return mentioned
	}
	for idx, cand := range cands {
		if idx == len(cands)-1 && PROpen(g.prs[cand]) {
			return cand
		}
		if cutoff := g.PRCutoffs[cand]; !cutoff.IsZero() && !t.After(cutoff) {
			return cand
		}
	}
	return 0
}

// LatestPR is issue's latest PR, or 0.
func (g Grouping) LatestPR(issue int64) int64 {
	if cands := g.PRsForIssue[issue]; len(cands) > 0 {
		return cands[len(cands)-1]
	}
	return 0
}
