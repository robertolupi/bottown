package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	ID        string         `json:"id"`
	Target    string         `json:"target"` // "#51" or "!51"
	TargetURL string         `json:"target_url"`
	Kind      string         `json:"kind"`
	Actor     string         `json:"actor"`
	Family    string         `json:"family"`
	Harness   string         `json:"harness"`
	Mode      string         `json:"mode"`   // "batch" or "interactive"
	Status    string         `json:"status"` // "succeeded", "failed", "revoked"
	Created   time.Time      `json:"created"`
	Ended     *time.Time     `json:"ended,omitempty"`
	Duration  string         `json:"duration"` // e.g. "2m 15s"
	Summary   string         `json:"summary"`  // first line of handover
	Usage     *secrets.Usage `json:"usage,omitempty"`
	Purged    bool           `json:"purged"`
	Denials   []Denial       `json:"denials"`
}

// HistoryFilter specifies criteria for filtering ended runs.
type HistoryFilter struct {
	Actor  string
	Kind   string
	Status string
	Since  time.Time
}

// TimelineEvent is a single chronological event on an issue.
type TimelineEvent struct {
	Type   string    `json:"type"` // "issue_created", "run", "pr_opened", "review", "pr_merged", "comment"
	Time   time.Time `json:"time"`
	Title  string    `json:"title"`
	Detail string    `json:"detail,omitempty"`
	Actor  string    `json:"actor,omitempty"`
	Status string    `json:"status,omitempty"`
	RunID  string    `json:"run_id,omitempty"`
	URL    string    `json:"url,omitempty"`
}

// IssueTimeline represents the complete history for an issue.
type IssueTimeline struct {
	Issue  int64           `json:"issue"`
	Title  string          `json:"title"`
	State  string          `json:"state"`
	URL    string          `json:"url"`
	Events []TimelineEvent `json:"events"`
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

	// PR by issue index
	prByIssue := make(map[int64]*sdk.PullRequest)
	for _, pr := range prs {
		if pr.Head != nil && strings.HasPrefix(pr.Head.Ref, "issue/") {
			var issueNum int64
			if _, err := fmt.Sscanf(pr.Head.Ref, "issue/%d", &issueNum); err == nil {
				prByIssue[issueNum] = pr
			}
		}
	}

	// Fetch reviews ONLY for PRs matching active runs
	prReviews := make(map[int64][]*sdk.PullReview)
	for _, sr := range serverRuns {
		if sr.Kind != "implement" && sr.Kind != "verify" {
			continue
		}
		issueNum := sr.Issue
		if issueNum <= 0 && sr.PR > 0 {
			for _, pr := range prs {
				if pr.Index == sr.PR && pr.Head != nil && strings.HasPrefix(pr.Head.Ref, "issue/") {
					var num int64
					if _, err := fmt.Sscanf(pr.Head.Ref, "issue/%d", &num); err == nil {
						issueNum = num
					}
					break
				}
			}
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
		if sr.Kind == "implement" || sr.Kind == "verify" {
			issueNum := sr.Issue
			if issueNum <= 0 && sr.PR > 0 {
				for _, pr := range prs {
					if pr.Index == sr.PR && pr.Head != nil && strings.HasPrefix(pr.Head.Ref, "issue/") {
						var num int64
						if _, err := fmt.Sscanf(pr.Head.Ref, "issue/%d", &num); err == nil {
							issueNum = num
						}
						break
					}
				}
			}

			if issueNum > 0 {
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

// ComputeHistoryRuns retrieves ended runs matching filter criteria.
func (s *Server) ComputeHistoryRuns(ctx context.Context, filter HistoryFilter) ([]HistoryRunInfo, error) {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()

	actorMap, _ := s.fetchActorMap(ctx)

	var serverRuns []secrets.Run
	runsPath := "/v1/runs?history=1&state=ended&repo=" + url.QueryEscape(repo)
	if err := s.callServer(ctx, http.MethodGet, runsPath, nil, &serverRuns); err != nil {
		return nil, fmt.Errorf("fetch history runs: %w", err)
	}
	denials := s.fetchDenials(ctx)

	var history []HistoryRunInfo
	for _, sr := range serverRuns {
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

		history = append(history, HistoryRunInfo{
			ID:        sr.ID,
			Target:    target,
			TargetURL: targetURL,
			Kind:      sr.Kind,
			Actor:     sr.Actor,
			Family:    family,
			Harness:   harness,
			Mode:      mode,
			Status:    sr.Status,
			Created:   sr.Created,
			Ended:     sr.Ended,
			Duration:  duration,
			Summary:   summary,
			Usage:     sr.Digest.Usage,
			Purged:    sr.Purged,
			Denials:   orEmpty(denials[sr.ID]),
		})
	}

	// Sort newest first
	sort.Slice(history, func(i, j int) bool {
		return history[i].Created.After(history[j].Created)
	})

	return history, nil
}

// ComputeIssueTimeline gathers all runs, PRs, reviews, and merge events for an issue.
func (s *Server) ComputeIssueTimeline(ctx context.Context, issueNum int64) (*IssueTimeline, error) {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()

	// 1. Fetch issue
	issuePath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d", org, repo, issueNum)
	var issue sdk.Issue
	if err := s.callServer(ctx, http.MethodGet, issuePath, nil, &issue); err != nil {
		return nil, fmt.Errorf("fetch issue #%d: %w", issueNum, err)
	}

	timeline := &IssueTimeline{
		Issue:  issueNum,
		Title:  issue.Title,
		State:  string(issue.State),
		URL:    fmt.Sprintf("%s/%s/%s/issues/%d", forgeURL, org, repo, issueNum),
		Events: []TimelineEvent{},
	}

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

	// 2. Fetch associated PR
	pullsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=all", org, repo)
	prs, _ := listAll[*sdk.PullRequest](ctx, s, pullsPath)

	var matchedPR *sdk.PullRequest
	for _, pr := range prs {
		if pr.Head != nil && pr.Head.Ref == fmt.Sprintf("issue/%d", issueNum) {
			matchedPR = pr
			break
		}
	}

	var prIndex int64
	if matchedPR != nil {
		prIndex = matchedPR.Index
		prURL := fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, prIndex)
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

		// Reviews
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

	// 3. Fetch runs for this issue and its PR
	var serverRuns []secrets.Run
	runsPath := "/v1/runs?history=1&repo=" + url.QueryEscape(repo)
	if err := s.callServer(ctx, http.MethodGet, runsPath, nil, &serverRuns); err == nil {
		for _, sr := range serverRuns {
			if sr.Issue == issueNum || (prIndex > 0 && sr.PR == prIndex) {
				detail := sr.Digest.Summary
				if detail == "" {
					detail = fmt.Sprintf("Run %s (%s)", sr.ID, sr.Kind)
				}
				runURL := timeline.URL
				if sr.PR > 0 {
					runURL = fmt.Sprintf("%s/%s/%s/pulls/%d", forgeURL, org, repo, sr.PR)
				}
				timeline.Events = append(timeline.Events, TimelineEvent{
					Type:   "run",
					Time:   sr.Created,
					Title:  fmt.Sprintf("Run %s: %s by %s", sr.ID, sr.Kind, sr.Actor),
					Detail: detail,
					Actor:  sr.Actor,
					Status: sr.Status,
					RunID:  sr.ID,
					URL:    runURL,
				})
			}
		}
	}

	// 4. Issue comments
	commentsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d/comments", org, repo, issueNum)
	// Forgejo returns all issue comments and ignores page/limit.
	var comments []*sdk.Comment
	err := s.callServer(ctx, http.MethodGet, commentsPath, nil, &comments)
	if err == nil {
		for _, c := range comments {
			if c != nil && !c.Created.IsZero() {
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
