package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

// InboxItemKind identifies the type of an owner inbox item (§4.1).
type InboxItemKind string

const (
	InboxReadyToMerge      InboxItemKind = "ready_to_merge"
	InboxConflictsWithMain InboxItemKind = "conflicts_with_main"
	InboxStoppedFlow       InboxItemKind = "stopped_flow"
	InboxBrokenRun         InboxItemKind = "broken_run"
	InboxDiscussAnswer     InboxItemKind = "discuss_answer"
	InboxActorFiledIssue   InboxItemKind = "actor_filed_issue"
)

// InboxItem is one row in the owner inbox view (§4.1).
type InboxItem struct {
	ID          string        `json:"id"`
	Kind        InboxItemKind `json:"kind"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Target      string        `json:"target"`
	TargetURL   string        `json:"target_url"`
	Action      *InboxAction  `json:"action,omitempty"`
	Created     time.Time     `json:"created"`
}

// InboxAction is the matching bottown action offered for an inbox item.
type InboxAction struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}

// ComputeInbox recomputes the owner inbox items from forge and run state (§4.1).
func (s *Server) ComputeInbox(ctx context.Context) ([]InboxItem, error) {
	org, repo, ok := strings.Cut(s.cfg.FullRepo, "/")
	if !ok {
		org, repo = "bottown", s.cfg.FullRepo
	}
	forgeURL := s.cfg.Instance.ForgejoURL()

	// 1. Actors and their families (GET /v1/actors)
	var actorList []struct {
		Name    string `json:"name"`
		Family  string `json:"family"`
		Harness string `json:"harness"`
	}
	actors := make(map[string]string) // name -> family
	if err := s.callServer(ctx, http.MethodGet, "/v1/actors", nil, &actorList); err == nil {
		for _, a := range actorList {
			actors[a.Name] = a.Family
		}
	}

	// 2. Owner login
	ownerLogin := s.cfg.Instance.OwnerLogin
	if ownerLogin == "" {
		var who struct {
			Actor string `json:"actor"`
		}
		if err := s.callServer(ctx, http.MethodGet, "/v1/whoami", nil, &who); err == nil && who.Actor != "" {
			ownerLogin = who.Actor
		}
	}

	// 3. Server runs for this repo
	var serverRuns []secrets.Run
	runsPath := "/v1/runs?repo=" + url.QueryEscape(repo)
	_ = s.callServer(ctx, http.MethodGet, runsPath, nil, &serverRuns)

	now := time.Now()
	activeByIssue := make(map[int64]bool)
	activeByPR := make(map[int64]bool)
	for _, sr := range serverRuns {
		if sr.Ended == nil && (sr.Expires.IsZero() || !now.After(sr.Expires)) {
			if sr.Issue > 0 {
				activeByIssue[sr.Issue] = true
			}
			if sr.PR > 0 {
				activeByPR[sr.PR] = true
			}
		}
	}

	// 4. Local workspace runs
	var localRuns []runs.Record
	runsDir := filepath.Join(s.cfg.RepoTop, ".bottown", "runs")
	if fi, err := os.Stat(runsDir); err == nil && fi.IsDir() {
		ws := &runs.Workspace{
			Top:     s.cfg.RepoTop,
			Dir:     filepath.Join(s.cfg.RepoTop, ".bottown"),
			RunsDir: runsDir,
		}
		if recs, err := ws.Records(); err == nil {
			localRuns = recs
		}
	}

	// 5. Open PRs from Forgejo
	pullsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=open", org, repo)
	var prs []*sdk.PullRequest
	_ = s.callServer(ctx, http.MethodGet, pullsPath, nil, &prs)

	// Fetch reviews for each open PR
	prReviews := make(map[int64][]*sdk.PullReview)
	for _, pr := range prs {
		revPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls/%d/reviews", org, repo, pr.Index)
		var revs []*sdk.PullReview
		if err := s.callServer(ctx, http.MethodGet, revPath, nil, &revs); err == nil {
			prReviews[pr.Index] = revs
		}
	}

	// 6. Open Issues from Forgejo
	issuesPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues?state=open&type=issues", org, repo)
	var issues []*sdk.Issue
	_ = s.callServer(ctx, http.MethodGet, issuesPath, nil, &issues)

	var items []InboxItem

	// ----------------------------------------------------
	// Item 1: Ready to merge (§4.1)
	// Open PR, mergeable, approved at current head by another family
	// ----------------------------------------------------
	for _, pr := range prs {
		if pr.State != sdk.StateOpen || pr.HasMerged {
			continue
		}
		if !pr.Mergeable {
			continue
		}
		authorLogin := userLogin(pr.Poster)
		authorFamily := actors[authorLogin]
		revs := prReviews[pr.Index]

		var approvedReview *sdk.PullReview
		var reviewerFamily string
		for _, r := range revs {
			if r.Stale || r.Dismissed || !r.Official {
				continue
			}
			if pr.Head == nil || r.CommitID != pr.Head.Sha {
				continue
			}
			if r.State == sdk.ReviewStateApproved {
				revLogin := userLogin(r.Reviewer)
				fam := actors[revLogin]
				if fam != "" && fam != authorFamily {
					approvedReview = r
					reviewerFamily = fam
					break
				}
			}
		}

		if approvedReview != nil {
			prURL := pr.HTMLURL
			if prURL == "" {
				prURL = fmt.Sprintf("%s/%s/pulls/%d", forgeURL, s.cfg.FullRepo, pr.Index)
			}
			headSHA := ""
			if pr.Head != nil {
				headSHA = pr.Head.Sha
			}
			created := approvedReview.Submitted
			if created.IsZero() && pr.Created != nil {
				created = *pr.Created
			}
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("ready-to-merge-pr-%d", pr.Index),
				Kind:        InboxReadyToMerge,
				Title:       fmt.Sprintf("PR !%d is ready to merge", pr.Index),
				Description: fmt.Sprintf("Approved at %s by %s (%s)", shortSHA(headSHA), userLogin(approvedReview.Reviewer), reviewerFamily),
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     created,
			})
		}
	}

	// ----------------------------------------------------
	// Item 2: Conflicts with main (§4.1)
	// Open PR that no longer merges cleanly
	// ----------------------------------------------------
	for _, pr := range prs {
		if pr.State != sdk.StateOpen || pr.HasMerged {
			continue
		}
		if !pr.Mergeable {
			prURL := pr.HTMLURL
			if prURL == "" {
				prURL = fmt.Sprintf("%s/%s/pulls/%d", forgeURL, s.cfg.FullRepo, pr.Index)
			}
			created := time.Now()
			if pr.Updated != nil {
				created = *pr.Updated
			} else if pr.Created != nil {
				created = *pr.Created
			}
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("conflicts-pr-%d", pr.Index),
				Kind:        InboxConflictsWithMain,
				Title:       fmt.Sprintf("PR !%d conflicts with main", pr.Index),
				Description: "PR no longer merges cleanly with main",
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     created,
			})
		}
	}

	// ----------------------------------------------------
	// Item 3: Stopped flow (§4.1, §3.2)
	// Stopped as out of rounds, run failed, or verifier undecided
	// ----------------------------------------------------
	for _, pr := range prs {
		if pr.State != sdk.StateOpen || pr.HasMerged {
			continue
		}
		if activeByPR[pr.Index] {
			continue
		}

		revs := prReviews[pr.Index]
		var latestHeadReview *sdk.PullReview
		changesRequestedCount := 0
		for _, r := range revs {
			if r.Dismissed {
				continue
			}
			if r.State == sdk.ReviewStateRequestChanges {
				changesRequestedCount++
			}
			if pr.Head != nil && r.CommitID == pr.Head.Sha && !r.Stale {
				latestHeadReview = r
			}
		}

		// Check server runs for this PR
		var latestRun *secrets.Run
		for i := range serverRuns {
			sr := &serverRuns[i]
			if sr.PR == pr.Index {
				if latestRun == nil || sr.Created.After(latestRun.Created) {
					latestRun = sr
				}
			}
		}

		prURL := pr.HTMLURL
		if prURL == "" {
			prURL = fmt.Sprintf("%s/%s/pulls/%d", forgeURL, s.cfg.FullRepo, pr.Index)
		}

		if latestRun != nil && latestRun.Status == "failed" {
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("stopped-flow-run-failed-pr-%d", pr.Index),
				Kind:        InboxStoppedFlow,
				Title:       fmt.Sprintf("Flow stopped on !%d: run failed", pr.Index),
				Description: fmt.Sprintf("Run %s (%s by %s) failed", latestRun.ID, latestRun.Kind, latestRun.Actor),
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     latestRun.Created,
			})
		} else if latestHeadReview != nil && latestHeadReview.State == sdk.ReviewStateComment {
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("stopped-flow-undecided-pr-%d", pr.Index),
				Kind:        InboxStoppedFlow,
				Title:       fmt.Sprintf("Flow stopped on !%d: verifier undecided", pr.Index),
				Description: fmt.Sprintf("Verifier %s ended review with COMMENT at %s", userLogin(latestHeadReview.Reviewer), shortSHA(latestHeadReview.CommitID)),
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     latestHeadReview.Submitted,
			})
		} else if latestHeadReview != nil && latestHeadReview.State == sdk.ReviewStateRequestChanges {
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("stopped-flow-rounds-pr-%d", pr.Index),
				Kind:        InboxStoppedFlow,
				Title:       fmt.Sprintf("Flow stopped on !%d: out of rounds", pr.Index),
				Description: fmt.Sprintf("Changes requested at %s by %s (%d review round%s)", shortSHA(latestHeadReview.CommitID), userLogin(latestHeadReview.Reviewer), changesRequestedCount, pluralS(changesRequestedCount)),
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     latestHeadReview.Submitted,
			})
		}
	}

	for _, issue := range issues {
		if issue.State != sdk.StateOpen || issue.PullRequest != nil {
			continue
		}
		if activeByIssue[issue.Index] {
			continue
		}
		var latestRun *secrets.Run
		for i := range serverRuns {
			sr := &serverRuns[i]
			if sr.Issue == issue.Index && sr.PR == 0 {
				if latestRun == nil || sr.Created.After(latestRun.Created) {
					latestRun = sr
				}
			}
		}
		if latestRun != nil && latestRun.Status == "failed" {
			issueURL := issue.HTMLURL
			if issueURL == "" {
				issueURL = fmt.Sprintf("%s/%s/issues/%d", forgeURL, s.cfg.FullRepo, issue.Index)
			}
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("stopped-flow-run-failed-issue-%d", issue.Index),
				Kind:        InboxStoppedFlow,
				Title:       fmt.Sprintf("Flow stopped on #%d: run failed", issue.Index),
				Description: fmt.Sprintf("Run %s (%s by %s) failed", latestRun.ID, latestRun.Kind, latestRun.Actor),
				Target:      fmt.Sprintf("#%d", issue.Index),
				TargetURL:   issueURL,
				Created:     latestRun.Created,
			})
		}
	}

	// ----------------------------------------------------
	// Item 4: Broken run (§4.1)
	// Dead run, key expired while live, or failed run with worktree left
	// ----------------------------------------------------
	seenBrokenRun := make(map[string]bool)

	// 1. Local dead runs (process exited without run end)
	for _, r := range localRuns {
		if r.Status == runs.StatusDead {
			seenBrokenRun[r.ID] = true
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("broken-run-dead-%s", r.ID),
				Kind:        InboxBrokenRun,
				Title:       fmt.Sprintf("Run %s is dead", r.ID),
				Description: fmt.Sprintf("Process %d exited without run end", r.PID),
				Target:      r.ID,
				TargetURL:   fmt.Sprintf("%s/%s", forgeURL, s.cfg.FullRepo),
				Action: &InboxAction{
					Label:   "Stop run",
					Command: "bottown run stop " + r.ID,
				},
				Created: r.Started,
			})
		}
	}

	// 2. Server runs with key expired while live
	for _, sr := range serverRuns {
		if sr.Ended == nil && !sr.Expires.IsZero() && now.After(sr.Expires) {
			if seenBrokenRun[sr.ID] {
				continue
			}
			seenBrokenRun[sr.ID] = true
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("broken-run-expired-%s", sr.ID),
				Kind:        InboxBrokenRun,
				Title:       fmt.Sprintf("Run %s key expired while live", sr.ID),
				Description: fmt.Sprintf("Run %s (%s by %s) expired", sr.ID, sr.Kind, sr.Actor),
				Target:      sr.ID,
				TargetURL:   fmt.Sprintf("%s/%s", forgeURL, s.cfg.FullRepo),
				Action: &InboxAction{
					Label:   "Stop run",
					Command: "bottown run stop " + sr.ID,
				},
				Created: sr.Expires,
			})
		}
	}

	// 3. Failed run left its worktree behind
	for _, r := range localRuns {
		if r.Status == runs.StatusFailed {
			wtPath := r.Worktree
			if wtPath == "" {
				wtPath = filepath.Join(s.cfg.RepoTop, ".bottown", "runs", r.ID)
			}
			if fi, err := os.Stat(wtPath); err == nil && fi.IsDir() {
				items = append(items, InboxItem{
					ID:          fmt.Sprintf("broken-run-worktree-%s", r.ID),
					Kind:        InboxBrokenRun,
					Title:       fmt.Sprintf("Failed run %s left worktree behind", r.ID),
					Description: fmt.Sprintf("Worktree still present for %s", r.ID),
					Target:      r.ID,
					TargetURL:   fmt.Sprintf("%s/%s", forgeURL, s.cfg.FullRepo),
					Action: &InboxAction{
						Label:   "Clean up",
						Command: "bottown run gc",
					},
					Created: r.Started,
				})
			}
		}
	}

	// ----------------------------------------------------
	// Item 5: Discuss answer (§4.1)
	// Discuss run answered on an issue, owner has not commented since
	// ----------------------------------------------------
	for _, issue := range issues {
		if issue.State != sdk.StateOpen || issue.PullRequest != nil {
			continue
		}
		var discussRun *secrets.Run
		for i := range serverRuns {
			sr := &serverRuns[i]
			if sr.Issue == issue.Index && sr.Kind == "discuss" && sr.Status == "succeeded" {
				if discussRun == nil || sr.Created.After(discussRun.Created) {
					discussRun = sr
				}
			}
		}

		cPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d/comments", org, repo, issue.Index)
		var comments []*sdk.Comment
		if err := s.callServer(ctx, http.MethodGet, cPath, nil, &comments); err != nil {
			continue
		}

		var latestDiscussTime time.Time
		var discussActor string
		if discussRun != nil {
			discussActor = discussRun.Actor
		}

		for _, c := range comments {
			poster := userLogin(c.Poster)
			if actors[poster] != "" {
				if discussActor == "" || poster == discussActor || discussRun != nil {
					if c.Created.After(latestDiscussTime) {
						latestDiscussTime = c.Created
						discussActor = poster
					}
				}
			}
		}

		if latestDiscussTime.IsZero() && discussRun != nil {
			if discussRun.Ended != nil {
				latestDiscussTime = *discussRun.Ended
			} else {
				latestDiscussTime = discussRun.Created
			}
		}

		if latestDiscussTime.IsZero() {
			continue
		}

		ownerReplied := false
		for _, c := range comments {
			poster := userLogin(c.Poster)
			if poster == ownerLogin && c.Created.After(latestDiscussTime) {
				ownerReplied = true
				break
			}
		}

		if !ownerReplied {
			issueURL := issue.HTMLURL
			if issueURL == "" {
				issueURL = fmt.Sprintf("%s/%s/issues/%d", forgeURL, s.cfg.FullRepo, issue.Index)
			}
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("discuss-answer-issue-%d", issue.Index),
				Kind:        InboxDiscussAnswer,
				Title:       fmt.Sprintf("Discuss answer on #%d waiting on owner reply", issue.Index),
				Description: fmt.Sprintf("Discussant %s answered; owner has not replied", discussActor),
				Target:      fmt.Sprintf("#%d", issue.Index),
				TargetURL:   issueURL,
				Created:     latestDiscussTime,
			})
		}
	}

	// ----------------------------------------------------
	// Item 6: Actor-filed issue (§4.1)
	// Filed by an actor, not queued, no milestone, not closed
	// ----------------------------------------------------
	for _, issue := range issues {
		if issue.State != sdk.StateOpen || issue.PullRequest != nil {
			continue
		}
		authorLogin := userLogin(issue.Poster)
		fam := actors[authorLogin]
		if fam == "" {
			continue
		}

		if issue.Milestone != nil && issue.Milestone.ID > 0 {
			continue
		}

		isQueued := false
		for _, lbl := range issue.Labels {
			if lbl != nil {
				if strings.HasPrefix(lbl.Name, "implement/") || strings.HasPrefix(lbl.Name, "verify/") || lbl.Name == "queued" {
					isQueued = true
					break
				}
			}
		}
		if isQueued {
			continue
		}

		issueURL := issue.HTMLURL
		if issueURL == "" {
			issueURL = fmt.Sprintf("%s/%s/issues/%d", forgeURL, s.cfg.FullRepo, issue.Index)
		}
		items = append(items, InboxItem{
			ID:          fmt.Sprintf("actor-filed-issue-%d", issue.Index),
			Kind:        InboxActorFiledIssue,
			Title:       fmt.Sprintf("Actor-filed issue #%d not triaged", issue.Index),
			Description: fmt.Sprintf("Filed by %s (%s); no milestone or queue label", authorLogin, fam),
			Target:      fmt.Sprintf("#%d", issue.Index),
			TargetURL:   issueURL,
			Created:     issue.Created,
		})
	}

	// Sort newest first
	slices.SortFunc(items, func(a, b InboxItem) int {
		return b.Created.Compare(a.Created)
	})

	return items, nil
}

func userLogin(u *sdk.User) string {
	if u == nil {
		return ""
	}
	if u.UserName != "" {
		return u.UserName
	}
	return u.LoginName
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
