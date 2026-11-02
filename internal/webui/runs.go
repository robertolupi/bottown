package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

// ActorDetail represents an actor available on the instance.
type ActorDetail struct {
	Name    string `json:"name"`
	Family  string `json:"family"`
	Harness string `json:"harness"`
}

// LiveRunInfo represents an active run.
type LiveRunInfo struct {
	ID          string    `json:"id"`
	Target      string    `json:"target"` // "#51" or "!51"
	TargetURL   string    `json:"target_url"`
	Issue       int64     `json:"issue,omitempty"`
	IssueURL    string    `json:"issue_url,omitempty"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	PR          int64     `json:"pr,omitempty"`
	PRURL       string    `json:"pr_url,omitempty"`
	PRTitle     string    `json:"pr_title,omitempty"`
	Milestone   string    `json:"milestone,omitempty"`
	ThreadKey   string    `json:"thread_key,omitempty"`
	Kind        string    `json:"kind"` // implement, verify, discuss
	Actor       string    `json:"actor"`
	Family      string    `json:"family"`
	Harness     string    `json:"harness"`
	Interactive bool      `json:"interactive"`
	Created     time.Time `json:"created"`
	Expires     time.Time `json:"expires"`
	Elapsed     string    `json:"elapsed"` // e.g. "4m 12s"
	Timeout     string    `json:"timeout"` // e.g. "60m"
	Flow        *FlowInfo `json:"flow,omitempty"`
	LogTail     []string  `json:"log_tail"`
	Denials     []Denial  `json:"denials"`
}

// Denial is an operation the server's policy refused a run, from
// GET /v1/audit.
type Denial struct {
	Time   time.Time `json:"time"`
	Run    string    `json:"run,omitempty"`
	Op     string    `json:"op"`
	Method string    `json:"method,omitempty"`
	Path   string    `json:"path,omitempty"`
	Rule   string    `json:"rule,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// FlowInfo describes the flow grouping for a live run.
type FlowInfo struct {
	Issue     int64  `json:"issue"`
	Round     int    `json:"round"`
	MaxRounds int    `json:"max_rounds"`
	Step      string `json:"step"`  // "implementing with agy" / "verifying with kimi"
	Label     string `json:"label"` // "#51 · round 2/3 · verifying with kimi"
}

// ActiveFlowInfo represents an in-progress flow supervising an issue.
type ActiveFlowInfo struct {
	Issue     int64  `json:"issue"`
	Implement string `json:"implement"`
	Verify    string `json:"verify"`
	Round     int    `json:"round"`
	MaxRounds int    `json:"max_rounds"`
	Step      string `json:"step"`
	Label     string `json:"label"`
	Waiting   bool   `json:"waiting"`
}

// HistoryRunInfo represents a finished run in the history view.
type HistoryRunInfo struct {
	ID         string `json:"id"`
	Target     string `json:"target"` // "#51" or "!51"
	TargetURL  string `json:"target_url"`
	Issue      int64  `json:"issue,omitempty"`
	IssueURL   string `json:"issue_url,omitempty"`
	IssueTitle string `json:"issue_title,omitempty"`
	PR         int64  `json:"pr,omitempty"`
	PRURL      string `json:"pr_url,omitempty"`
	PRTitle    string `json:"pr_title,omitempty"`
	Milestone  string `json:"milestone,omitempty"`
	ThreadKey  string `json:"thread_key,omitempty"`
	Active     bool   `json:"active,omitempty"`
	Kind       string `json:"kind"`
	Actor      string `json:"actor"`
	Family     string `json:"family"`
	Harness    string `json:"harness"`
	Mode       string `json:"mode"`   // "batch" or "interactive"
	Status     string `json:"status"` // "succeeded", "failed", "revoked"
	// Outcome is the work result the server recorded (secrets.Outcome*),
	// apart from Status: empty when none was recorded.
	Outcome  string         `json:"outcome,omitempty"`
	Created  time.Time      `json:"created"`
	Ended    *time.Time     `json:"ended,omitempty"`
	Duration string         `json:"duration"` // e.g. "2m 15s"
	Summary  string         `json:"summary"`  // first line of handover
	Usage    *secrets.Usage `json:"usage,omitempty"`
	Purged   bool           `json:"purged"`
	Denials  []Denial       `json:"denials"`
}

// HistoryFilter specifies criteria for filtering ended runs.
type HistoryFilter struct {
	Actor  string
	Kind   string
	Status string
	Since  time.Time
}

// TimelineEvent is a single chronological event on an issue or flow thread.
type TimelineEvent struct {
	Type     string     `json:"type"` // "issue_created", "run", "pr_opened", "review", "pr_merged", "comment"
	Time     time.Time  `json:"time"`
	Ended    *time.Time `json:"ended,omitempty"`
	Duration string     `json:"duration,omitempty"`
	Kind     string     `json:"kind,omitempty"`
	Title    string     `json:"title"`
	Detail   string     `json:"detail,omitempty"`
	Actor    string     `json:"actor,omitempty"`
	Status   string     `json:"status,omitempty"`
	Outcome  string     `json:"outcome,omitempty"` // run events: see HistoryRunInfo.Outcome
	RunID    string     `json:"run_id,omitempty"`
	URL      string     `json:"url,omitempty"`
}

// IssueTimeline represents the timeline for an issue or a specific flow thread.
type IssueTimeline struct {
	Issue     int64           `json:"issue"`
	PR        int64           `json:"pr,omitempty"`
	PRURL     string          `json:"pr_url,omitempty"`
	PRTitle   string          `json:"pr_title,omitempty"`
	Milestone string          `json:"milestone,omitempty"`
	ThreadKey string          `json:"thread_key,omitempty"`
	Title     string          `json:"title"`
	State     string          `json:"state"`
	URL       string          `json:"url"`
	Events    []TimelineEvent `json:"events"`
}

func (s *Server) getActiveFlow(issue int64) *FlowRecord {
	if issue <= 0 {
		return nil
	}
	infoPath := filepath.Join(s.cfg.RepoTop, ".bottown", fmt.Sprintf("flow-%d.json", issue))
	data, err := os.ReadFile(infoPath)
	if err != nil {
		return nil
	}
	var rec FlowRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	// The UI always records a real PID. Without one there is no process
	// to check or stop, and ExecuteStop's kill(-PID) would signal the
	// UI's own process group for PID 0.
	if rec.PID <= 0 {
		return nil
	}
	if err := syscall.Kill(rec.PID, 0); err != nil {
		_ = os.Remove(infoPath)
		return nil
	}
	return &rec
}

// ComputeLiveRuns calculates active runs with flow grouping and readable log tails.
func (s *Server) ComputeLiveRuns(ctx context.Context) ([]LiveRunInfo, error) {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()

	// 1. Fetch actors
	actorMap, err := s.fetchActorMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch actors: %w", err)
	}

	// 2. Fetch live runs from server
	var serverRuns []secrets.Run
	runsPath := "/v1/runs?state=live&repo=" + url.QueryEscape(repo)
	if err := s.callServer(ctx, http.MethodGet, runsPath, nil, &serverRuns); err != nil {
		return nil, fmt.Errorf("fetch live runs: %w", err)
	}
	if len(serverRuns) == 0 {
		return []LiveRunInfo{}, nil
	}
	denials := s.fetchDenials(ctx)

	// 3. Fetch open PRs only if needed
	pullsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=open", org, repo)
	prs, _ := listAll[*sdk.PullRequest](ctx, s, pullsPath)

	// PR by issue index and PR by PR index
	prByIssue := make(map[int64]*sdk.PullRequest)
	prByIndex := make(map[int64]*sdk.PullRequest, len(prs))
	for _, pr := range prs {
		if pr == nil || pr.Index <= 0 {
			continue
		}
		prByIndex[pr.Index] = pr
		if pr.Head != nil && strings.HasPrefix(pr.Head.Ref, "issue/") {
			var issueNum int64
			if _, err := fmt.Sscanf(pr.Head.Ref, "issue/%d", &issueNum); err == nil {
				prByIssue[issueNum] = pr
			}
		}
	}

	// Fetch issues and reviews ONLY for issues/PRs matching active runs
	issuesByIndex := make(map[int64]*sdk.Issue)
	prReviews := make(map[int64][]*sdk.PullReview)
	for _, sr := range serverRuns {
		issueNum := sr.Issue
		if issueNum <= 0 {
			issueNum = parseIssueRef(sr.Branch)
		}
		if issueNum <= 0 && sr.PR > 0 {
			if pr := prByIndex[sr.PR]; pr != nil && pr.Head != nil {
				if num := parseIssueRef(pr.Head.Ref); num > 0 {
					issueNum = num
				}
			}
		}
		if issueNum > 0 && issuesByIndex[issueNum] == nil {
			var iss sdk.Issue
			issuePath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d", org, repo, issueNum)
			if err := s.callServer(ctx, http.MethodGet, issuePath, nil, &iss); err == nil && iss.Index > 0 {
				issuesByIndex[issueNum] = &iss
			}
		}
		if sr.Kind != "implement" && sr.Kind != "verify" {
			continue
		}
		if pr := prByIssue[issueNum]; pr != nil && prReviews[pr.Index] == nil {
			revsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls/%d/reviews", org, repo, pr.Index)
			revs, err := listAll[*sdk.PullReview](ctx, s, revsPath)
			if err == nil {
				prReviews[pr.Index] = revs
			}
		}
	}

	now := time.Now().UTC()
	var live []LiveRunInfo

	for _, sr := range serverRuns {
		act := actorMap[sr.Actor]
		family := sr.Family
		if family == "" && act != nil {
			family = act.Family
		}
		harness := sr.Harness
		if harness == "" && act != nil {
			harness = act.Harness
		}

		target := fmt.Sprintf("#%d", sr.Issue)
		targetURL := fmt.Sprintf("%s/%s/%s/issues/%d", forgeURL, org, repo, sr.Issue)
		if sr.PR > 0 {
			target = fmt.Sprintf("!%d", sr.PR)
			targetURL = fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, sr.PR)
		}

		// Read log tail from disk if batch run
		var logTail []string
		if !sr.Interactive {
			logPath := filepath.Join(s.cfg.RepoTop, ".bottown", "runs", sr.ID+".log")
			if logBytes, err := os.ReadFile(logPath); err == nil {
				parsed := runs.RenderLog(harness, logBytes)
				if len(parsed) > 25 {
					logTail = parsed[len(parsed)-25:]
				} else {
					logTail = parsed
				}
			}
		}
		if logTail == nil {
			logTail = []string{}
		}

		// Calculate flow info ONLY for implement and verify runs when an active flow exists
		var flow *FlowInfo
		issueNum := sr.Issue
		if issueNum <= 0 {
			issueNum = parseIssueRef(sr.Branch)
		}
		if issueNum <= 0 && sr.PR > 0 {
			for _, pr := range prs {
				if pr.Index == sr.PR && pr.Head != nil {
					if num := parseIssueRef(pr.Head.Ref); num > 0 {
						issueNum = num
					}
					break
				}
			}
		}
		prNum := sr.PR
		if prNum <= 0 && issueNum > 0 && sr.Kind != "discuss" {
			if pr := prByIssue[issueNum]; pr != nil {
				prNum = pr.Index
			}
		}

		if (sr.Kind == "implement" || sr.Kind == "verify") && issueNum > 0 {
			flowRec := s.getActiveFlow(issueNum)
			if flowRec != nil {
				pr := prByIssue[issueNum]
				round := 1
				if pr != nil {
					for _, r := range prReviews[pr.Index] {
						// Matching internal/flow countRounds: count all requested changes
						// including dismissed/stale ones
						if r.State == sdk.ReviewStateRequestChanges {
							round++
						}
					}
				}
				maxRounds := flowRec.Rounds
				if maxRounds <= 0 {
					maxRounds = 3
				}
				stepWord := "implementing with"
				if sr.Kind == "verify" {
					stepWord = "verifying with"
				}
				step := fmt.Sprintf("%s %s", stepWord, sr.Actor)
				label := fmt.Sprintf("#%d · round %d/%d · %s", issueNum, round, maxRounds, step)
				flow = &FlowInfo{
					Issue:     issueNum,
					Round:     round,
					MaxRounds: maxRounds,
					Step:      step,
					Label:     label,
				}
			}
		}

		var issueURL, issueTitle, prURL, prTitle, milestone, threadKey string
		if issueNum > 0 {
			issueURL = fmt.Sprintf("%s/%s/%s/issues/%d", forgeURL, org, repo, issueNum)
			if iss := issuesByIndex[issueNum]; iss != nil {
				issueTitle = iss.Title
				if iss.Milestone != nil && iss.Milestone.Title != "" {
					milestone = iss.Milestone.Title
				}
			}
		}
		if prNum > 0 {
			prURL = fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, prNum)
			pr := prByIndex[prNum]
			if pr == nil {
				if byIss := prByIssue[issueNum]; byIss != nil && byIss.Index == prNum {
					pr = byIss
				}
			}
			if pr != nil {
				prTitle = pr.Title
				if milestone == "" && pr.Milestone != nil && pr.Milestone.Title != "" {
					milestone = pr.Milestone.Title
				}
			}
		}
		if sr.Kind == "discuss" {
			threadKey = "discuss:" + sr.ID
		} else {
			threadKey = flowThreadKey(issueNum, prNum, sr.ID)
		}

		elapsed := formatDuration(now.Sub(sr.Created))
		timeout := "60m"
		if !sr.Expires.IsZero() && sr.Expires.After(sr.Created) {
			timeout = formatDuration(sr.Expires.Sub(sr.Created))
		}

		live = append(live, LiveRunInfo{
			ID:          sr.ID,
			Target:      target,
			TargetURL:   targetURL,
			Issue:       issueNum,
			IssueURL:    issueURL,
			IssueTitle:  issueTitle,
			PR:          prNum,
			PRURL:       prURL,
			PRTitle:     prTitle,
			Milestone:   milestone,
			ThreadKey:   threadKey,
			Kind:        sr.Kind,
			Actor:       sr.Actor,
			Family:      family,
			Harness:     harness,
			Interactive: sr.Interactive,
			Created:     sr.Created,
			Expires:     sr.Expires,
			Elapsed:     elapsed,
			Timeout:     timeout,
			Flow:        flow,
			LogTail:     logTail,
			Denials:     orEmpty(denials[sr.ID]),
		})
	}

	return live, nil
}

// ComputeActiveFlows returns all active flows supervising issues, including those between runs.
func (s *Server) ComputeActiveFlows(ctx context.Context, liveRuns []LiveRunInfo) []ActiveFlowInfo {
	bottownDir := filepath.Join(s.cfg.RepoTop, ".bottown")
	entries, err := os.ReadDir(bottownDir)
	if err != nil {
		return []ActiveFlowInfo{}
	}

	org, repo := s.orgAndRepo()

	liveByIssue := make(map[int64]LiveRunInfo)
	for _, r := range liveRuns {
		if r.Flow != nil {
			liveByIssue[r.Flow.Issue] = r
		}
	}

	var flows []ActiveFlowInfo
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "flow-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		var issueNum int64
		if _, err := fmt.Sscanf(name, "flow-%d.json", &issueNum); err != nil || issueNum <= 0 {
			continue
		}

		flowRec := s.getActiveFlow(issueNum)
		if flowRec == nil {
			continue
		}

		maxRounds := flowRec.Rounds
		if maxRounds <= 0 {
			maxRounds = 3
		}

		if liveRun, ok := liveByIssue[issueNum]; ok && liveRun.Flow != nil {
			flows = append(flows, ActiveFlowInfo{
				Issue:     issueNum,
				Implement: flowRec.Implement,
				Verify:    flowRec.Verify,
				Round:     liveRun.Flow.Round,
				MaxRounds: liveRun.Flow.MaxRounds,
				Step:      liveRun.Flow.Step,
				Label:     liveRun.Flow.Label,
				Waiting:   false,
			})
			continue
		}

		// When waiting between runs, compute current round from PR reviews
		round := 1
		pullsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=open", org, repo)
		prs, _ := listAll[*sdk.PullRequest](ctx, s, pullsPath)
		for _, pr := range prs {
			if pr.Head != nil && pr.Head.Ref == fmt.Sprintf("issue/%d", issueNum) {
				revsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls/%d/reviews", org, repo, pr.Index)
				revs, err := listAll[*sdk.PullReview](ctx, s, revsPath)
				if err == nil {
					for _, r := range revs {
						if r.State == sdk.ReviewStateRequestChanges {
							round++
						}
					}
				}
				break
			}
		}

		step := "waiting between rounds"
		label := fmt.Sprintf("#%d · round %d/%d · %s", issueNum, round, maxRounds, step)
		flows = append(flows, ActiveFlowInfo{
			Issue:     issueNum,
			Implement: flowRec.Implement,
			Verify:    flowRec.Verify,
			Round:     round,
			MaxRounds: maxRounds,
			Step:      step,
			Label:     label,
			Waiting:   true,
		})
	}

	if flows == nil {
		flows = []ActiveFlowInfo{}
	}
	sort.Slice(flows, func(i, j int) bool {
		return flows[i].Issue < flows[j].Issue
	})
	return flows
}

var summaryPRRegex = regexp.MustCompile(`!(\d+)\b`)

type resolvedRunMeta struct {
	Issue      int64
	IssueURL   string
	IssueTitle string
	PR         int64
	PRURL      string
	PRTitle    string
	Milestone  string
	ThreadKey  string
	Active     bool
}

func parseIssueRef(ref string) int64 {
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

func parseSummaryPR(summary string) int64 {
	m := summaryPRRegex.FindStringSubmatch(summary)
	if len(m) >= 2 {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func flowThreadKey(issueNum, prNum int64, runID string) string {
	switch {
	case issueNum > 0 && prNum > 0:
		return fmt.Sprintf("flow:issue:%d:pr:%d", issueNum, prNum)
	case issueNum > 0:
		return fmt.Sprintf("flow:issue:%d", issueNum)
	case prNum > 0:
		return fmt.Sprintf("flow:pr:%d", prNum)
	default:
		return "flow:run:" + runID
	}
}

func isPROpen(pr *sdk.PullRequest) bool {
	if pr == nil {
		return true
	}
	if pr.HasMerged || pr.Merged != nil || pr.Closed != nil {
		return false
	}
	return pr.State == "" || pr.State == sdk.StateOpen
}

// resolveRunsMeta maps runs to their flow thread (issue, PR, milestone, thread key, and active state).
// Implement and verify runs for the same issue and PR share a thread; distinct PRs form separate threads;
// pre-PR implement runs group under the issue and join the PR thread once the PR is created;
// discuss runs remain standalone entries.
func (s *Server) resolveRunsMeta(
	allRuns []secrets.Run,
	issues []*sdk.Issue,
	prs []*sdk.PullRequest,
) (map[string]resolvedRunMeta, map[int64][]int64, map[int64]time.Time) {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()

	issuesByIndex := make(map[int64]*sdk.Issue, len(issues))
	for _, iss := range issues {
		if iss != nil && iss.Index > 0 {
			issuesByIndex[iss.Index] = iss
		}
	}

	prsByIndex := make(map[int64]*sdk.PullRequest, len(prs))
	issueOfPR := make(map[int64]int64)
	for _, pr := range prs {
		if pr == nil || pr.Index <= 0 {
			continue
		}
		prsByIndex[pr.Index] = pr
		if pr.Head != nil {
			if n := parseIssueRef(pr.Head.Ref); n > 0 {
				issueOfPR[pr.Index] = n
			}
		}
	}

	hasExplicitPRRun := make(map[int64]bool)
	earliestRunForPR := make(map[int64]time.Time)
	for _, sr := range allRuns {
		if sr.PR > 0 {
			hasExplicitPRRun[sr.PR] = true
			if t, ok := earliestRunForPR[sr.PR]; !ok || (!sr.Created.IsZero() && sr.Created.Before(t)) {
				earliestRunForPR[sr.PR] = sr.Created
			}
			if issueOfPR[sr.PR] == 0 {
				if sr.Issue > 0 {
					issueOfPR[sr.PR] = sr.Issue
				} else if n := parseIssueRef(sr.Branch); n > 0 {
					issueOfPR[sr.PR] = n
				}
			}
		}
	}

	for _, sr := range allRuns {
		if sr.Kind == "implement" && sr.Issue > 0 {
			if mentioned := parseSummaryPR(sr.Digest.Summary); mentioned > 0 && issueOfPR[mentioned] == 0 {
				if prsByIndex[mentioned] != nil || hasExplicitPRRun[mentioned] {
					issueOfPR[mentioned] = sr.Issue
				}
			}
		}
	}

	prsForIssue := make(map[int64][]int64)
	seenPR := make(map[int64]bool)
	for prIdx, issNum := range issueOfPR {
		if issNum > 0 && prIdx > 0 && !seenPR[prIdx] {
			seenPR[prIdx] = true
			prsForIssue[issNum] = append(prsForIssue[issNum], prIdx)
		}
	}

	prStartTime := func(prIdx int64) time.Time {
		if pr := prsByIndex[prIdx]; pr != nil && pr.Created != nil && !pr.Created.IsZero() {
			return *pr.Created
		}
		return earliestRunForPR[prIdx]
	}

	for issNum := range prsForIssue {
		cands := prsForIssue[issNum]
		sort.Slice(cands, func(i, j int) bool {
			ti, tj := prStartTime(cands[i]), prStartTime(cands[j])
			if !ti.Equal(tj) && !ti.IsZero() && !tj.IsZero() {
				return ti.Before(tj)
			}
			return cands[i] < cands[j]
		})
		prsForIssue[issNum] = cands
	}

	prCutoffs := make(map[int64]time.Time)
	for _, cands := range prsForIssue {
		for idx, prIdx := range cands {
			pr := prsByIndex[prIdx]
			if idx == len(cands)-1 && isPROpen(pr) {
				continue
			}
			var cutoff time.Time
			if pr != nil {
				if pr.Merged != nil && pr.Merged.After(cutoff) {
					cutoff = *pr.Merged
				}
				if pr.Closed != nil && pr.Closed.After(cutoff) {
					cutoff = *pr.Closed
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
			for _, r := range allRuns {
				if r.Kind == "discuss" {
					continue
				}
				if r.PR == prIdx || (r.Kind == "implement" && parseSummaryPR(r.Digest.Summary) == prIdx) {
					if r.Created.After(cutoff) {
						cutoff = r.Created
					}
					if r.Ended != nil && r.Ended.After(cutoff) {
						cutoff = *r.Ended
					}
				}
			}
			prCutoffs[prIdx] = cutoff
		}
	}

	meta := make(map[string]resolvedRunMeta, len(allRuns))
	activeThreads := make(map[string]bool)
	seenIssues := make(map[int64]bool)
	now := time.Now().UTC()

	for _, sr := range allRuns {
		var issueNum, prNum int64
		var threadKey string

		switch sr.Kind {
		case "discuss":
			issueNum = sr.Issue
			prNum = sr.PR
			threadKey = "discuss:" + sr.ID
		case "verify":
			prNum = sr.PR
			issueNum = sr.Issue
			if issueNum <= 0 && prNum > 0 {
				issueNum = issueOfPR[prNum]
			}
			if issueNum <= 0 {
				issueNum = parseIssueRef(sr.Branch)
			}
			threadKey = flowThreadKey(issueNum, prNum, sr.ID)
		default: // "implement"
			issueNum = sr.Issue
			if issueNum <= 0 {
				issueNum = parseIssueRef(sr.Branch)
			}
			prNum = sr.PR
			if issueNum <= 0 && prNum > 0 {
				issueNum = issueOfPR[prNum]
			}
			if prNum <= 0 && issueNum > 0 {
				cands := prsForIssue[issueNum]
				if mentioned := parseSummaryPR(sr.Digest.Summary); mentioned > 0 && slices.Contains(cands, mentioned) {
					prNum = mentioned
				} else {
					for idx, candPR := range cands {
						if idx == len(cands)-1 && isPROpen(prsByIndex[candPR]) {
							prNum = candPR
							break
						}
						if cutoff := prCutoffs[candPR]; !cutoff.IsZero() && !sr.Created.After(cutoff) {
							prNum = candPR
							break
						}
					}
				}
			}
			threadKey = flowThreadKey(issueNum, prNum, sr.ID)
		}

		if issueNum > 0 {
			seenIssues[issueNum] = true
		}

		var issueURL, issueTitle, prURL, prTitle, milestone string
		if issueNum > 0 {
			issueURL = fmt.Sprintf("%s/%s/%s/issues/%d", forgeURL, org, repo, issueNum)
			if iss := issuesByIndex[issueNum]; iss != nil {
				issueTitle = iss.Title
				if iss.Milestone != nil && iss.Milestone.Title != "" {
					milestone = iss.Milestone.Title
				}
			}
		}
		if prNum > 0 {
			prURL = fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, prNum)
			if pr := prsByIndex[prNum]; pr != nil {
				prTitle = pr.Title
				if milestone == "" && pr.Milestone != nil && pr.Milestone.Title != "" {
					milestone = pr.Milestone.Title
				}
			}
		}

		meta[sr.ID] = resolvedRunMeta{
			Issue:      issueNum,
			IssueURL:   issueURL,
			IssueTitle: issueTitle,
			PR:         prNum,
			PRURL:      prURL,
			PRTitle:    prTitle,
			Milestone:  milestone,
			ThreadKey:  threadKey,
		}

		if (sr.Kind == "implement" || sr.Kind == "verify") && sr.Ended == nil && (sr.Expires.IsZero() || sr.Active(now)) {
			activeThreads[threadKey] = true
		}
	}

	for issueNum := range prsForIssue {
		seenIssues[issueNum] = true
	}
	for issueNum := range seenIssues {
		if s.getActiveFlow(issueNum) != nil {
			cands := prsForIssue[issueNum]
			if len(cands) > 0 && isPROpen(prsByIndex[cands[len(cands)-1]]) {
				activeThreads[flowThreadKey(issueNum, cands[len(cands)-1], "")] = true
			} else {
				activeThreads[flowThreadKey(issueNum, 0, "")] = true
			}
		}
	}

	for id, m := range meta {
		if !strings.HasPrefix(m.ThreadKey, "discuss:") {
			m.Active = activeThreads[m.ThreadKey]
			meta[id] = m
		}
	}

	return meta, prsForIssue, prCutoffs
}

// ComputeHistoryRuns retrieves ended runs matching filter criteria.
func (s *Server) ComputeHistoryRuns(ctx context.Context, filter HistoryFilter) ([]HistoryRunInfo, error) {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()

	actorMap, _ := s.fetchActorMap(ctx)

	var allRuns []secrets.Run
	runsPath := "/v1/runs?history=1&repo=" + url.QueryEscape(repo)
	if err := s.callServer(ctx, http.MethodGet, runsPath, nil, &allRuns); err != nil {
		return nil, fmt.Errorf("fetch history runs: %w", err)
	}
	denials := s.fetchDenials(ctx)

	pullsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=all", org, repo)
	prs, _ := listAll[*sdk.PullRequest](ctx, s, pullsPath)

	issuesPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues?state=all&type=issues", org, repo)
	issues, _ := listAll[*sdk.Issue](ctx, s, issuesPath)

	metaMap, _, _ := s.resolveRunsMeta(allRuns, issues, prs)

	var history []HistoryRunInfo
	for _, sr := range allRuns {
		if sr.Ended == nil {
			continue
		}
		// Apply filters
		if filter.Actor != "" && sr.Actor != filter.Actor {
			continue
		}
		if filter.Kind != "" && sr.Kind != filter.Kind {
			continue
		}
		if filter.Status != "" && sr.Status != filter.Status {
			continue
		}
		if !filter.Since.IsZero() && sr.Created.Before(filter.Since) {
			continue
		}

		act := actorMap[sr.Actor]
		family := sr.Family
		if family == "" && act != nil {
			family = act.Family
		}
		harness := sr.Harness
		if harness == "" && act != nil {
			harness = act.Harness
		}

		target := fmt.Sprintf("#%d", sr.Issue)
		targetURL := fmt.Sprintf("%s/%s/%s/issues/%d", forgeURL, org, repo, sr.Issue)
		if sr.PR > 0 {
			target = fmt.Sprintf("!%d", sr.PR)
			targetURL = fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, sr.PR)
		}

		mode := "batch"
		if sr.Interactive {
			mode = "interactive"
		}

		duration := ""
		if sr.Ended != nil {
			duration = formatDuration(sr.Ended.Sub(sr.Created))
		} else if sr.Digest.Usage != nil && sr.Digest.Usage.DurationMS != nil {
			duration = formatDuration(time.Duration(*sr.Digest.Usage.DurationMS) * time.Millisecond)
		}

		summary := firstLine(sr.Digest.Summary)
		meta := metaMap[sr.ID]

		history = append(history, HistoryRunInfo{
			ID:         sr.ID,
			Target:     target,
			TargetURL:  targetURL,
			Issue:      meta.Issue,
			IssueURL:   meta.IssueURL,
			IssueTitle: meta.IssueTitle,
			PR:         meta.PR,
			PRURL:      meta.PRURL,
			PRTitle:    meta.PRTitle,
			Milestone:  meta.Milestone,
			ThreadKey:  meta.ThreadKey,
			Active:     meta.Active,
			Kind:       sr.Kind,
			Actor:      sr.Actor,
			Family:     family,
			Harness:    harness,
			Mode:       mode,
			Status:     sr.Status,
			Outcome:    sr.Outcome,
			Created:    sr.Created,
			Ended:      sr.Ended,
			Duration:   duration,
			Summary:    summary,
			Usage:      sr.Digest.Usage,
			Purged:     sr.Purged,
			Denials:    orEmpty(denials[sr.ID]),
		})
	}

	// Sort newest first
	sort.Slice(history, func(i, j int) bool {
		return history[i].Created.After(history[j].Created)
	})

	return history, nil
}

// ComputeIssueTimeline gathers runs, PRs, reviews, and merge events for an issue's latest flow thread.
func (s *Server) ComputeIssueTimeline(ctx context.Context, issueNum int64) (*IssueTimeline, error) {
	return s.ComputeFlowTimeline(ctx, issueNum, -1)
}

// ComputeFlowTimeline gathers events and runs for a specific flow thread (issue + PR).
// When prFilter is -1, it selects the latest PR for issueNum (or 0 if no PR exists).
// When prFilter is 0, it scopes to the pre-PR thread for issueNum.
// Discuss runs and runs belonging to other PR threads on the same issue are excluded.
func (s *Server) ComputeFlowTimeline(ctx context.Context, issueNum, prFilter int64) (*IssueTimeline, error) {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()

	// 1. Fetch issue when issueNum > 0
	var issue *sdk.Issue
	if issueNum > 0 {
		issuePath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d", org, repo, issueNum)
		var fetched sdk.Issue
		if err := s.callServer(ctx, http.MethodGet, issuePath, nil, &fetched); err != nil {
			return nil, fmt.Errorf("fetch issue #%d: %w", issueNum, err)
		}
		issue = &fetched
	}

	// 2. Fetch all PRs and runs to resolve thread membership accurately
	pullsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=all", org, repo)
	prs, _ := listAll[*sdk.PullRequest](ctx, s, pullsPath)

	var serverRuns []secrets.Run
	runsPath := "/v1/runs?history=1&repo=" + url.QueryEscape(repo)
	_ = s.callServer(ctx, http.MethodGet, runsPath, nil, &serverRuns)

	var issuesList []*sdk.Issue
	if issue != nil {
		issuesList = []*sdk.Issue{issue}
	}
	metaMap, prsForIssue, prCutoffs := s.resolveRunsMeta(serverRuns, issuesList, prs)

	prsByIndex := make(map[int64]*sdk.PullRequest, len(prs))
	for _, pr := range prs {
		if pr != nil && pr.Index > 0 {
			prsByIndex[pr.Index] = pr
		}
	}

	prIndex := prFilter
	if prIndex < 0 {
		if cands := prsForIssue[issueNum]; len(cands) > 0 {
			prIndex = cands[len(cands)-1]
		} else {
			prIndex = 0
		}
	}

	if issueNum <= 0 && prIndex > 0 {
		if pr := prsByIndex[prIndex]; pr != nil && pr.Head != nil {
			issueNum = parseIssueRef(pr.Head.Ref)
		}
		if issueNum > 0 && issue == nil {
			issuePath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d", org, repo, issueNum)
			var fetched sdk.Issue
			if err := s.callServer(ctx, http.MethodGet, issuePath, nil, &fetched); err == nil {
				issue = &fetched
			}
		}
	}

	matchedPR := prsByIndex[prIndex]
	targetThreadKey := flowThreadKey(issueNum, prIndex, "")

	var milestone string
	if issue != nil && issue.Milestone != nil && issue.Milestone.Title != "" {
		milestone = issue.Milestone.Title
	} else if matchedPR != nil && matchedPR.Milestone != nil && matchedPR.Milestone.Title != "" {
		milestone = matchedPR.Milestone.Title
	}

	timeline := &IssueTimeline{
		Issue:     issueNum,
		PR:        prIndex,
		Milestone: milestone,
		ThreadKey: targetThreadKey,
		Events:    []TimelineEvent{},
	}
	if issue != nil {
		timeline.Title = issue.Title
		timeline.State = string(issue.State)
		timeline.URL = fmt.Sprintf("%s/%s/%s/issues/%d", forgeURL, org, repo, issueNum)
		if !issue.Created.IsZero() {
			timeline.Events = append(timeline.Events, TimelineEvent{
				Type:   "issue_created",
				Time:   issue.Created,
				Title:  fmt.Sprintf("Issue #%d opened", issueNum),
				Detail: issue.Title,
				Actor:  userLogin(issue.Poster),
				URL:    timeline.URL,
			})
		}
	} else if matchedPR != nil {
		timeline.Title = matchedPR.Title
		timeline.State = string(matchedPR.State)
		timeline.URL = fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, prIndex)
	}

	if prIndex > 0 {
		prURL := fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, prIndex)
		timeline.PRURL = prURL
		if matchedPR != nil {
			timeline.PRTitle = matchedPR.Title
			if matchedPR.Created != nil {
				timeline.Events = append(timeline.Events, TimelineEvent{
					Type:   "pr_opened",
					Time:   *matchedPR.Created,
					Title:  fmt.Sprintf("Pull request !%d opened", prIndex),
					Detail: matchedPR.Title,
					Actor:  userLogin(matchedPR.Poster),
					URL:    prURL,
				})
			}

			// Reviews for this specific PR
			revsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls/%d/reviews", org, repo, prIndex)
			revs, err := listAll[*sdk.PullReview](ctx, s, revsPath)
			if err == nil {
				for _, r := range revs {
					timeline.Events = append(timeline.Events, TimelineEvent{
						Type:   "review",
						Time:   r.Submitted,
						Title:  fmt.Sprintf("Review on !%d: %s", prIndex, r.State),
						Detail: r.Body,
						Actor:  userLogin(r.Reviewer),
						Status: string(r.State),
						URL:    prURL,
					})
				}
			}

			// Merge event
			if matchedPR.HasMerged && matchedPR.Merged != nil {
				timeline.Events = append(timeline.Events, TimelineEvent{
					Type:   "pr_merged",
					Time:   *matchedPR.Merged,
					Title:  fmt.Sprintf("Pull request !%d merged into main", prIndex),
					Actor:  userLogin(matchedPR.MergedBy),
					Status: "merged",
					URL:    prURL,
				})
			}
		}
	}

	// 3. Runs belonging specifically to this flow thread (excluding discuss runs and other PR threads)
	for _, sr := range serverRuns {
		if sr.Kind == "discuss" {
			continue
		}
		m := metaMap[sr.ID]
		if m.ThreadKey != targetThreadKey {
			continue
		}
		detail := firstLine(sr.Digest.Summary)
		if detail == "" {
			detail = fmt.Sprintf("Run %s (%s)", sr.ID, sr.Kind)
		}
		runURL := timeline.URL
		if sr.PR > 0 {
			runURL = fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, sr.PR)
		}
		duration := ""
		if sr.Ended != nil {
			duration = formatDuration(sr.Ended.Sub(sr.Created))
		} else if sr.Digest.Usage != nil && sr.Digest.Usage.DurationMS != nil {
			duration = formatDuration(time.Duration(*sr.Digest.Usage.DurationMS) * time.Millisecond)
		}
		status := sr.Status
		if status == "" && sr.Ended == nil {
			status = "live"
		}
		timeline.Events = append(timeline.Events, TimelineEvent{
			Type:     "run",
			Time:     sr.Created,
			Ended:    sr.Ended,
			Duration: duration,
			Kind:     sr.Kind,
			Title:    fmt.Sprintf("Run %s: %s by %s", sr.ID, sr.Kind, sr.Actor),
			Detail:   detail,
			Actor:    sr.Actor,
			Status:   status,
			Outcome:  sr.Outcome,
			RunID:    sr.ID,
			URL:      runURL,
		})
	}

	// 4. Issue comments scoped to this flow thread's window (excluding discuss handovers)
	if issueNum > 0 {
		var afterTime, cutoffTime time.Time
		cands := prsForIssue[issueNum]
		if prIndex > 0 {
			if idx := slices.Index(cands, prIndex); idx >= 0 {
				if idx > 0 {
					afterTime = prCutoffs[cands[idx-1]]
				}
				cutoffTime = prCutoffs[prIndex]
			}
		} else if len(cands) > 0 {
			afterTime = prCutoffs[cands[len(cands)-1]]
		}

		commentsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d/comments", org, repo, issueNum)
		var comments []*sdk.Comment
		if err := s.callServer(ctx, http.MethodGet, commentsPath, nil, &comments); err == nil {
			for _, c := range comments {
				if c == nil || c.Created.IsZero() {
					continue
				}
				if strings.Contains(c.Body, "**bottown run ") && strings.Contains(c.Body, "· discuss ·") {
					continue
				}
				if !afterTime.IsZero() && !c.Created.After(afterTime) {
					continue
				}
				if !cutoffTime.IsZero() && c.Created.After(cutoffTime) {
					continue
				}
				timeline.Events = append(timeline.Events, TimelineEvent{
					Type:   "comment",
					Time:   c.Created,
					Title:  fmt.Sprintf("Comment by %s", userLogin(c.Poster)),
					Detail: c.Body,
					Actor:  userLogin(c.Poster),
					URL:    timeline.URL,
				})
			}
		}
	}

	// Sort events chronologically
	sort.Slice(timeline.Events, func(i, j int) bool {
		return timeline.Events[i].Time.Before(timeline.Events[j].Time)
	})

	return timeline, nil
}

// fetchDenials returns the policy refusals the server has kept, denials and
// rejected requests, by run id. They are selected by run, not repository:
// a run's refusals include its attempts outside its repository and the
// ones with no repository at all. They are an addition to the runs view:
// if the server cannot say, the runs show none rather than failing.
func (s *Server) fetchDenials(ctx context.Context) map[string][]Denial {
	var entries []Denial
	path := "/v1/audit?decision=deny,reject"
	if err := s.callServer(ctx, http.MethodGet, path, nil, &entries); err != nil {
		return nil
	}
	out := map[string][]Denial{}
	for _, e := range entries {
		if e.Run != "" {
			out[e.Run] = append(out[e.Run], e)
		}
	}
	return out
}

func orEmpty(d []Denial) []Denial {
	if d == nil {
		return []Denial{}
	}
	return d
}

func (s *Server) fetchActorMap(ctx context.Context) (map[string]*ActorDetail, error) {
	var list []ActorDetail
	if err := s.callServer(ctx, http.MethodGet, "/v1/actors", nil, &list); err != nil {
		return nil, err
	}
	m := make(map[string]*ActorDetail)
	for i := range list {
		m[list[i].Name] = &list[i]
	}
	return m, nil
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if m > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			return l
		}
	}
	return ""
}

func (s *Server) orgAndRepo() (string, string) {
	org, repo, ok := strings.Cut(s.cfg.FullRepo, "/")
	if !ok {
		return "bottown", s.cfg.FullRepo
	}
	return org, repo
}
