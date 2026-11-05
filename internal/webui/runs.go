package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// ActorDetail represents an actor available on the instance.
type ActorDetail struct {
	Name    string `json:"name"`
	Family  string `json:"family"`
	Harness string `json:"harness"`
	// Model is the host's default spec for the actor ([actor_models]),
	// which a run started without one gets.
	Model string `json:"model,omitempty"`
}

// LiveRunInfo represents an active run.
type LiveRunInfo struct {
	ID          string         `json:"id"`
	Target      string         `json:"target"` // "#51" or "!51"
	TargetURL   string         `json:"target_url"`
	Issue       int64          `json:"issue,omitempty"`
	IssueURL    string         `json:"issue_url,omitempty"`
	IssueTitle  string         `json:"issue_title,omitempty"`
	PR          int64          `json:"pr,omitempty"`
	PRURL       string         `json:"pr_url,omitempty"`
	PRTitle     string         `json:"pr_title,omitempty"`
	Milestone   string         `json:"milestone,omitempty"`
	ThreadKey   string         `json:"thread_key,omitempty"`
	Kind        string         `json:"kind"` // implement, verify, discuss
	Actor       string         `json:"actor"`
	Family      string         `json:"family"`
	Harness     string         `json:"harness"`
	Spec        string         `json:"spec,omitempty"` // the harness's model, if chosen
	Interactive bool           `json:"interactive"`
	Created     time.Time      `json:"created"`
	Expires     time.Time      `json:"expires"`
	Elapsed     string         `json:"elapsed"` // e.g. "4m 12s"
	Timeout     string         `json:"timeout"` // e.g. "60m"
	Flow        *FlowInfo      `json:"flow,omitempty"`
	LogTail     []runs.LogLine `json:"log_tail"`
	Denials     []Denial       `json:"denials"`
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

// LiveCheckInfo is a flow's post-implement checks (the repository's e2e
// commands) while they run: not a run, but shown with a live tail like
// one. GET /api/runs/{id}/log serves its log.
type LiveCheckInfo struct {
	ID         string         `json:"id"` // flow-<issue>-<sha>.e2e
	Issue      int64          `json:"issue,omitempty"`
	IssueURL   string         `json:"issue_url,omitempty"`
	IssueTitle string         `json:"issue_title,omitempty"`
	PR         int64          `json:"pr,omitempty"`
	PRURL      string         `json:"pr_url,omitempty"`
	SHA        string         `json:"sha"`
	Commands   []string       `json:"commands"`
	Created    time.Time      `json:"created"`
	Elapsed    string         `json:"elapsed"`
	Timeout    string         `json:"timeout,omitempty"` // the flow's --check-timeout
	LogTail    []runs.LogLine `json:"log_tail"`
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
	Spec       string `json:"spec,omitempty"` // the harness's model, if chosen
	Mode       string `json:"mode"`           // "batch" or "interactive"
	Status     string `json:"status"`         // "succeeded", "failed", "revoked"
	// Outcome is the work result the server recorded (secrets.Outcome*),
	// apart from Status: empty when none was recorded.
	Outcome  string         `json:"outcome,omitempty"`
	Created  time.Time      `json:"created"`
	Ended    *time.Time     `json:"ended,omitempty"`
	Duration string         `json:"duration"` // e.g. "2m 15s"
	Summary  string         `json:"summary"`  // first line of handover
	Usage    *secrets.Usage `json:"usage,omitempty"`
	// Failure is the observed cause of a failed or revoked run, when its
	// ender recorded one: the summary is the agent's own account.
	Failure *secrets.Failure `json:"failure,omitempty"`
	Purged  bool             `json:"purged"`
	Denials []Denial         `json:"denials"`

	// Flow-level state and authoritative PR/review data (§4.2, #155).
	PRHead         string `json:"pr_head,omitempty"`
	PRMerged       bool   `json:"pr_merged,omitempty"`
	PRMergeable    *bool  `json:"pr_mergeable,omitempty"`
	ReviewDecision string `json:"review_decision,omitempty"`
	ReviewAtHead   bool   `json:"review_at_head"`
	FlowState      string `json:"flow_state,omitempty"`
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

// IssueDetails is the issue text shown by reference popups.
type IssueDetails struct {
	Issue int64  `json:"issue"`
	Title string `json:"title"`
	Body  string `json:"body"`
	State string `json:"state"`
	URL   string `json:"url"`
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
	serverRuns, err := s.serverRuns(ctx, "live", false)
	if err != nil {
		return nil, fmt.Errorf("fetch live runs: %w", err)
	}
	if len(serverRuns) == 0 {
		return []LiveRunInfo{}, nil
	}
	denials := s.fetchDenials(ctx)

	// 3. Fetch open PRs only if needed
	prs, _ := s.pullRequests(ctx, "open", false)

	// PR by issue index and PR by PR index
	prByIssue := make(map[int64]*serverapi.PRItem)
	prByIndex := make(map[int64]*serverapi.PRItem, len(prs))
	for _, pr := range prs {
		if pr == nil || pr.Number <= 0 {
			continue
		}
		prByIndex[pr.Number] = pr
		if strings.HasPrefix(pr.HeadBranch, "issue/") {
			var issueNum int64
			if _, err := fmt.Sscanf(pr.HeadBranch, "issue/%d", &issueNum); err == nil {
				prByIssue[issueNum] = pr
			}
		}
	}

	// Fetch issues ONLY for those matching live runs
	issuesByIndex := make(map[int64]*serverapi.IssueOut)
	for _, sr := range serverRuns {
		issueNum := sr.Issue
		if issueNum <= 0 {
			issueNum = parseIssueRef(sr.Branch)
		}
		if issueNum <= 0 && sr.PR > 0 {
			if pr := prByIndex[sr.PR]; pr != nil {
				if num := parseIssueRef(pr.HeadBranch); num > 0 {
					issueNum = num
				}
			}
		}
		if issueNum > 0 && issuesByIndex[issueNum] == nil {
			if iss, err := s.issue(ctx, issueNum, false); err == nil && iss.Number > 0 {
				issuesByIndex[issueNum] = iss
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
		var logTail []runs.LogLine
		if !sr.Interactive {
			logPath := filepath.Join(s.cfg.RepoTop, ".bottown", "runs", sr.ID+".log")
			if logBytes, err := runs.ReadLog(logPath); err == nil {
				parsed := runs.RenderLog(harness, logBytes)
				if len(parsed) > 25 {
					logTail = parsed[len(parsed)-25:]
				} else {
					logTail = parsed
				}
			}
		}
		if logTail == nil {
			logTail = []runs.LogLine{}
		}

		// Calculate flow info ONLY for implement and verify runs when an active flow exists
		var flow *FlowInfo
		issueNum := sr.Issue
		if issueNum <= 0 {
			issueNum = parseIssueRef(sr.Branch)
		}
		if issueNum <= 0 && sr.PR > 0 {
			for _, pr := range prs {
				if pr.Number == sr.PR {
					if num := parseIssueRef(pr.HeadBranch); num > 0 {
						issueNum = num
					}
					break
				}
			}
		}
		prNum := sr.PR
		if prNum <= 0 && issueNum > 0 && sr.Kind != "discuss" {
			if pr := prByIssue[issueNum]; pr != nil {
				prNum = pr.Number
			}
		}

		if (sr.Kind == "implement" || sr.Kind == "verify") && issueNum > 0 {
			flowRec := s.getActiveFlow(issueNum)
			if flowRec != nil {
				pr := prByIssue[issueNum]
				round := 1
				if pr != nil {
					// The server counts the rounds as the flow does.
					round += pr.ChangesRequested
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
				if byIss := prByIssue[issueNum]; byIss != nil && byIss.Number == prNum {
					pr = byIss
				}
			}
			if pr != nil {
				prTitle = pr.Title
				if milestone == "" && pr.Milestone != "" {
					milestone = pr.Milestone
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
			Spec:        sr.Spec,
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

	liveByIssue := make(map[int64]LiveRunInfo)
	for _, r := range liveRuns {
		if r.Flow != nil {
			liveByIssue[r.Flow.Issue] = r
		}
	}
	// A flow between runs may be running its post-implement checks.
	checking := make(map[int64]runs.CheckRecord)
	for _, c := range s.liveCheckRecords() {
		checking[c.Issue] = c
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
		prs, _ := s.pullRequests(ctx, "open", false)
		for _, pr := range prs {
			if pr.HeadBranch == fmt.Sprintf("issue/%d", issueNum) {
				round += pr.ChangesRequested
				break
			}
		}

		step, waiting := "waiting between rounds", true
		if c, ok := checking[issueNum]; ok {
			step, waiting = "post-implement checks at "+shortSHA(c.SHA), false
		}
		label := fmt.Sprintf("#%d · round %d/%d · %s", issueNum, round, maxRounds, step)
		flows = append(flows, ActiveFlowInfo{
			Issue:     issueNum,
			Implement: flowRec.Implement,
			Verify:    flowRec.Verify,
			Round:     round,
			MaxRounds: maxRounds,
			Step:      step,
			Label:     label,
			Waiting:   waiting,
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

// liveCheckRecords are the flows' post-implement checks running in this
// checkout, for its repository.
func (s *Server) liveCheckRecords() []runs.CheckRecord {
	_, repo := s.orgAndRepo()
	var out []runs.CheckRecord
	for _, c := range runs.LiveChecks(filepath.Join(s.cfg.RepoTop, ".bottown", "runs")) {
		if c.Repo == "" || path.Base(c.Repo) == repo {
			out = append(out, c)
		}
	}
	return out
}

// ComputeLiveChecks returns the flows' post-implement checks while they
// run, each with the tail of its log, as a live run has (§4.2).
func (s *Server) ComputeLiveChecks(ctx context.Context) []LiveCheckInfo {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()
	now := time.Now().UTC()
	live := []LiveCheckInfo{}
	for _, c := range s.liveCheckRecords() {
		info := LiveCheckInfo{
			ID:       c.ID,
			Issue:    c.Issue,
			PR:       c.PR,
			SHA:      c.SHA,
			Commands: orEmpty(c.Commands),
			Created:  c.Started,
			Elapsed:  formatDuration(now.Sub(c.Started)),
			LogTail:  []runs.LogLine{},
		}
		if c.Issue > 0 {
			info.IssueURL = fmt.Sprintf("%s/%s/%s/issues/%d", forgeURL, org, repo, c.Issue)
			if iss, err := s.issue(ctx, c.Issue, false); err == nil && iss.Number > 0 {
				info.IssueTitle = iss.Title
			}
		}
		if c.PR > 0 {
			info.PRURL = fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, c.PR)
		}
		if c.Expires.After(c.Started) {
			info.Timeout = formatDuration(c.Expires.Sub(c.Started))
		}
		logPath := filepath.Join(s.cfg.RepoTop, ".bottown", "runs", c.ID+".log")
		if b, err := runs.ReadLog(logPath); err == nil {
			info.LogTail = tail(runs.RenderLog("", b), liveTailLines)
		}
		live = append(live, info)
	}
	return live
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

func isPROpen(pr *serverapi.PRItem) bool {
	if pr == nil {
		return true
	}
	if pr.Merged || pr.MergedAt != nil || pr.ClosedAt != nil {
		return false
	}
	return pr.State == "" || pr.State == string(sdk.StateOpen)
}

// resolveRunsMeta maps runs to their flow thread (issue, PR, milestone, thread key, and active state).
// Implement and verify runs for the same issue and PR share a thread; distinct PRs form separate threads;
// pre-PR implement runs group under the issue and join the PR thread once the PR is created;
// discuss runs remain standalone entries.
func (s *Server) resolveRunsMeta(
	allRuns []serverapi.RunItem,
	issues []*serverapi.IssueItem,
	prs []*serverapi.PRItem,
) (map[string]resolvedRunMeta, map[int64][]int64, map[int64]time.Time) {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()

	issuesByIndex := make(map[int64]*serverapi.IssueItem, len(issues))
	for _, iss := range issues {
		if iss != nil && iss.Number > 0 {
			issuesByIndex[iss.Number] = iss
		}
	}

	prsByIndex := make(map[int64]*serverapi.PRItem, len(prs))
	issueOfPR := make(map[int64]int64)
	for _, pr := range prs {
		if pr == nil || pr.Number <= 0 {
			continue
		}
		prsByIndex[pr.Number] = pr
		if n := parseIssueRef(pr.HeadBranch); n > 0 {
			issueOfPR[pr.Number] = n
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
				if milestone == "" && pr.Milestone != "" {
					milestone = pr.Milestone
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

		if (sr.Kind == "implement" || sr.Kind == "verify") && isActive(sr) {
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

	allRuns, err := s.serverRuns(ctx, "", true)
	if err != nil {
		return nil, fmt.Errorf("fetch history runs: %w", err)
	}
	denials := s.fetchDenials(ctx)

	prs, pullsErr := s.pullRequests(ctx, "all", false)
	prsByIndex := make(map[int64]*serverapi.PRItem, len(prs))
	for _, pr := range prs {
		if pr != nil && pr.Number > 0 {
			prsByIndex[pr.Number] = pr
		}
	}

	issues, _ := s.issues(ctx, "all", false)

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

		var prHead string
		var prMerged bool
		var prMergeable *bool
		var reviewDecision string
		var reviewAtHead bool
		flowState := ""

		if sr.Kind != "discuss" {
			flowState = "unknown"
			if meta.Active {
				flowState = "in_progress"
			} else if pullsErr != nil {
				// Missing / read-failed PR data must remain explicitly neutral/unknown
				flowState = "unknown"
			} else if meta.PR > 0 {
				if pr := prsByIndex[meta.PR]; pr != nil {
					prHead = pr.HeadSHA
					prMerged = pr.Merged
					if prMerged {
						flowState = "merged"
					} else if pr.State == string(sdk.StateOpen) {
						m := pr.Mergeable
						prMergeable = &m
						if !m {
							flowState = "needs_intervention"
						} else if sr.Status == "failed" || sr.Status == "revoked" {
							flowState = "needs_intervention"
						} else if pr.Assessment != nil {
							a := *pr.Assessment
							reviewDecision = string(a.Decision)
							reviewAtHead = a.AtHead(prHead)
							switch a.Decision {
							case review.Approved:
								flowState = "ready_to_merge"
							case review.ChangesRequested:
								flowState = "needs_changes"
							case review.Commented:
								flowState = "needs_intervention"
							case review.NeedsReview:
								flowState = "needs_review"
							}
						} else {
							// Review read failed or unavailable
							flowState = "unknown"
						}
					} else if pr.State == string(sdk.StateClosed) {
						flowState = "needs_intervention"
					}
				} else {
					flowState = "unknown"
				}
			} else {
				// Pre-PR flow run with PR data read successfully
				if sr.Status == "failed" || sr.Status == "revoked" || sr.Outcome == "failed" {
					flowState = "needs_intervention"
				} else {
					flowState = "unknown"
				}
			}
		}

		history = append(history, HistoryRunInfo{
			ID:             sr.ID,
			Target:         target,
			TargetURL:      targetURL,
			Issue:          meta.Issue,
			IssueURL:       meta.IssueURL,
			IssueTitle:     meta.IssueTitle,
			PR:             meta.PR,
			PRURL:          meta.PRURL,
			PRTitle:        meta.PRTitle,
			Milestone:      meta.Milestone,
			ThreadKey:      meta.ThreadKey,
			Active:         meta.Active,
			Kind:           sr.Kind,
			Actor:          sr.Actor,
			Family:         family,
			Harness:        harness,
			Spec:           sr.Spec,
			Mode:           mode,
			Status:         sr.Status,
			Outcome:        sr.Outcome,
			Created:        sr.Created,
			Ended:          sr.Ended,
			Duration:       duration,
			Summary:        summary,
			Usage:          sr.Digest.Usage,
			Failure:        sr.Digest.Failure,
			Purged:         sr.Purged,
			Denials:        orEmpty(denials[sr.ID]),
			PRHead:         prHead,
			PRMerged:       prMerged,
			PRMergeable:    prMergeable,
			ReviewDecision: reviewDecision,
			ReviewAtHead:   reviewAtHead,
			FlowState:      flowState,
		})
	}

	// Sort newest first
	sort.Slice(history, func(i, j int) bool {
		return history[i].Created.After(history[j].Created)
	})

	return history, nil
}

// ComputeIssueDetails fetches the text for an issue reference popup.
func (s *Server) ComputeIssueDetails(ctx context.Context, issueNum int64) (*IssueDetails, error) {
	org, repo := s.orgAndRepo()
	issue, err := s.issue(ctx, issueNum, false)
	if err != nil {
		return nil, fmt.Errorf("fetch issue #%d: %w", issueNum, err)
	}
	issueURL := issue.URL
	if issueURL == "" {
		issueURL = fmt.Sprintf("%s/%s/%s/issues/%d", s.cfg.Instance.ForgejoURL(), org, repo, issueNum)
	}
	return &IssueDetails{
		Issue: issueNum,
		Title: issue.Title,
		Body:  issue.Body,
		State: issue.State,
		URL:   issueURL,
	}, nil
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
	var issue *serverapi.IssueOut
	if issueNum > 0 {
		fetched, err := s.issue(ctx, issueNum, true)
		if err != nil {
			return nil, fmt.Errorf("fetch issue #%d: %w", issueNum, err)
		}
		issue = fetched
	}

	// 2. Fetch all PRs and runs to resolve thread membership accurately
	prs, _ := s.pullRequests(ctx, "all", false)

	serverRuns, _ := s.serverRuns(ctx, "", true)

	var issuesList []*serverapi.IssueItem
	if issue != nil {
		issuesList = []*serverapi.IssueItem{{Number: issue.Number, Title: issue.Title, Milestone: issue.Milestone}}
	}
	metaMap, prsForIssue, prCutoffs := s.resolveRunsMeta(serverRuns, issuesList, prs)

	prsByIndex := make(map[int64]*serverapi.PRItem, len(prs))
	for _, pr := range prs {
		if pr != nil && pr.Number > 0 {
			prsByIndex[pr.Number] = pr
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
		if pr := prsByIndex[prIndex]; pr != nil {
			issueNum = parseIssueRef(pr.HeadBranch)
		}
		if issueNum > 0 && issue == nil {
			if fetched, err := s.issue(ctx, issueNum, true); err == nil {
				issue = fetched
			}
		}
	}

	matchedPR := prsByIndex[prIndex]
	targetThreadKey := flowThreadKey(issueNum, prIndex, "")

	var milestone string
	if issue != nil && issue.Milestone != nil && issue.Milestone.Title != "" {
		milestone = issue.Milestone.Title
	} else if matchedPR != nil && matchedPR.Milestone != "" {
		milestone = matchedPR.Milestone
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
		timeline.State = issue.State
		timeline.URL = fmt.Sprintf("%s/%s/%s/issues/%d", forgeURL, org, repo, issueNum)
		if !issue.Created.IsZero() {
			timeline.Events = append(timeline.Events, TimelineEvent{
				Type:   "issue_created",
				Time:   issue.Created,
				Title:  fmt.Sprintf("Issue #%d opened", issueNum),
				Detail: issue.Title,
				Actor:  issue.Author,
				URL:    timeline.URL,
			})
		}
	} else if matchedPR != nil {
		timeline.Title = matchedPR.Title
		timeline.State = matchedPR.State
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
					Actor:  matchedPR.Author,
					URL:    prURL,
				})
			}

			// Reviews for this specific PR
			if full, err := s.pullRequest(ctx, prIndex); err == nil {
				for _, r := range full.Reviews {
					timeline.Events = append(timeline.Events, TimelineEvent{
						Type:   "review",
						Time:   r.Submitted,
						Title:  fmt.Sprintf("Review on !%d: %s", prIndex, r.State),
						Detail: r.Body,
						Actor:  r.Reviewer,
						Status: r.State,
						URL:    prURL,
					})
				}
			}

			// Merge event
			if matchedPR.Merged && matchedPR.MergedAt != nil {
				timeline.Events = append(timeline.Events, TimelineEvent{
					Type:   "pr_merged",
					Time:   *matchedPR.MergedAt,
					Title:  fmt.Sprintf("Pull request !%d merged into main", prIndex),
					Actor:  matchedPR.MergedBy,
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

		if issue != nil {
			for _, c := range issue.Comments {
				if c.Created.IsZero() {
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
					Title:  fmt.Sprintf("Comment by %s", c.Author),
					Detail: c.Body,
					Actor:  c.Author,
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

// orEmpty returns s, or an empty slice for nil, so that it encodes as [].
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
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
