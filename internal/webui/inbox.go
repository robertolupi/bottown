package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// InboxItemKind identifies the type of an owner inbox item (§4.1).
type InboxItemKind string

const (
	InboxReadyToMerge      InboxItemKind = "ready_to_merge"
	InboxMergeBase         InboxItemKind = "merge_base"
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
	Issue       int64         `json:"issue,omitempty"`
	IssueTitle  string        `json:"issue_title,omitempty"`
	HeadSHA     string        `json:"head_sha,omitempty"`
	Action      *InboxAction  `json:"action,omitempty"`
	Created     time.Time     `json:"created"`
}

// InboxAction is the matching bottown action offered for an inbox item.
type InboxAction struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}

// discussAnswerMargin is how long after a discuss run's end a comment by
// its actor still counts as the answer.
const discussAnswerMargin = time.Minute

// forgejoPageLimit is the page size used when listing issues or pull
// requests from Forgejo. Paging stops at the first page shorter than this.
const forgejoPageLimit = 50

// listAll fetches every item from a Forgejo list endpoint by paging with
// ?page=N until a page shorter than forgejoPageLimit comes back. path must
// already carry its query filters (state, type, …).
func listAll[T any](ctx context.Context, s *Server, path string) ([]T, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var all []T
	for page := 1; ; page++ {
		var items []T
		p := fmt.Sprintf("%s%slimit=%d&page=%d", path, sep, forgejoPageLimit, page)
		if err := s.callServer(ctx, http.MethodGet, p, nil, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < forgejoPageLimit {
			return all, nil
		}
	}
}

// ComputeInbox recomputes the owner inbox items from forge and run state (§4.1).
func (s *Server) ComputeInbox(ctx context.Context) ([]InboxItem, error) {
	org, repo, ok := strings.Cut(s.cfg.FullRepo, "/")
	if !ok {
		org, repo = "bottown", s.cfg.FullRepo
	}
	forgeURL := s.cfg.Instance.ForgejoURL()

	// 1. Actors and their families
	actorMap, err := s.fetchActorMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch actors: %w", err)
	}
	actors := actorFamilies(actorMap)

	// 2. Owner login
	ownerLogin := s.resolveOwnerLogin(ctx)

	// 3. Server runs for this repo
	var serverRuns []secrets.Run
	runsPath := "/v1/runs?repo=" + url.QueryEscape(repo)
	if err := s.callServer(ctx, http.MethodGet, runsPath, nil, &serverRuns); err != nil {
		return nil, fmt.Errorf("fetch runs: %w", err)
	}

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
		if recs, err := ws.Records(io.Discard); err == nil {
			localRuns = recs
		}
	}

	// 5. Open PRs from Forgejo
	pullsPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls?state=open", org, repo)
	prs, err := listAll[*sdk.PullRequest](ctx, s, pullsPath)
	if err != nil {
		return nil, fmt.Errorf("fetch open pull requests: %w", err)
	}

	// Assess each open PR's reviews as the server does for the flow and
	// the PR list (internal/review). Owner overrides come from every run
	// record, purged ones included.
	var allRuns []secrets.Run
	if err := s.callServer(ctx, http.MethodGet, runsPath+"&history=1", nil, &allRuns); err != nil {
		return nil, fmt.Errorf("fetch run history: %w", err)
	}
	assessments := s.assessOpenPullRequests(ctx, repo, prs, ownerLogin, actors, allRuns)
	mergeStates := make(map[int64]serverapi.PRMergeStatus)
	for _, pr := range prs {
		if pr == nil || pr.State != sdk.StateOpen || pr.HasMerged {
			continue
		}
		status, err := s.prMergeStatus(ctx, repo, pr.Index)
		if err == nil {
			mergeStates[pr.Index] = status
		}
	}

	// 6. Open Issues from Forgejo
	issuesPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues?state=open&type=issues", org, repo)
	issues, err := listAll[*sdk.Issue](ctx, s, issuesPath)
	if err != nil {
		return nil, fmt.Errorf("fetch open issues: %w", err)
	}

	issuesByIndex := make(map[int64]*sdk.Issue, len(issues))
	for _, issue := range issues {
		issuesByIndex[issue.Index] = issue
	}
	issueForPR := func(pr *sdk.PullRequest) (int64, string) {
		if pr.Head == nil {
			return 0, ""
		}
		issueNum := parseIssueRef(pr.Head.Ref)
		if issue := issuesByIndex[issueNum]; issue != nil {
			return issueNum, issue.Title
		}
		return issueNum, ""
	}

	var items []InboxItem

	// ----------------------------------------------------
	// Item 1: Ready to merge (§4.1)
	// Open PR, mergeable, and its reviews assess as approved
	// ----------------------------------------------------
	for _, pr := range prs {
		if pr.State != sdk.StateOpen || pr.HasMerged {
			continue
		}
		if !pr.Mergeable || mergeStates[pr.Index].Behind {
			continue
		}
		if a := assessments[pr.Index]; a.Decision == review.Approved {
			approval := a.Review
			approver := actors[approval.Reviewer]
			switch {
			case approval.Reviewer == ownerLogin:
				approver = "owner"
			case approver == actors[userLogin(pr.Poster)]:
				approver += ", same-family override"
			}
			prURL := pr.HTMLURL
			if prURL == "" {
				prURL = fmt.Sprintf("%s/%s/pulls/%d", forgeURL, s.cfg.FullRepo, pr.Index)
			}
			headSHA := ""
			if pr.Head != nil {
				headSHA = pr.Head.Sha
			}
			created := approval.Submitted
			if created.IsZero() && pr.Created != nil {
				created = *pr.Created
			}
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("ready-to-merge-pr-%d", pr.Index),
				Kind:        InboxReadyToMerge,
				Title:       fmt.Sprintf("PR !%d is ready to merge", pr.Index),
				Description: approvedDescription(a, headSHA, approver),
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     created,
			})
		}
	}

	// ----------------------------------------------------
	// Item 2: Merge base, or report conflicts (§4.1)
	// ----------------------------------------------------
	for _, pr := range prs {
		if pr.State != sdk.StateOpen || pr.HasMerged {
			continue
		}
		merge := mergeStates[pr.Index]
		if !merge.Behind && pr.Mergeable {
			continue
		}
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
		base, head := "main", ""
		if pr.Base != nil {
			base = pr.Base.Ref
		}
		if pr.Head != nil {
			head = pr.Head.Ref
		}
		if merge.Clean {
			// An implement run on the PR's issue records the issue and
			// branch, not the PR: merging under it would move the branch
			// its checkout pushes to.
			if issueNum, _ := issueForPR(pr); activeByPR[pr.Index] || activeByIssue[issueNum] {
				continue
			}
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("merge-base-pr-%d", pr.Index),
				Kind:        InboxMergeBase,
				Title:       fmt.Sprintf("Merge %s into %s for !%d", base, head, pr.Index),
				Description: "The branch is behind and the merge is clean",
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				HeadSHA:     merge.HeadSHA,
				Action: &InboxAction{
					Label:   "Merge " + base,
					Command: fmt.Sprintf("bottown flow merge-base !%d", pr.Index),
				},
				Created: created,
			})
			continue
		}
		description := "PR no longer merges cleanly with " + base
		if len(merge.Conflicts) > 0 {
			description = "Conflicting files: " + strings.Join(merge.Conflicts, ", ")
		}
		items = append(items, InboxItem{
			ID:          fmt.Sprintf("conflicts-pr-%d", pr.Index),
			Kind:        InboxConflictsWithMain,
			Title:       fmt.Sprintf("PR !%d conflicts with %s", pr.Index, base),
			Description: description,
			Target:      fmt.Sprintf("!%d", pr.Index),
			TargetURL:   prURL,
			Created:     created,
		})
	}

	// ----------------------------------------------------
	// Item 3: Stopped flow (§4.1, §3.2)
	// Stopped as run failed, or where the flow stops on the reviews:
	// changes requested (out of rounds, or held back by an older head's
	// request) and verifier undecided
	// ----------------------------------------------------
	for _, pr := range prs {
		if pr.State != sdk.StateOpen || pr.HasMerged {
			continue
		}
		if activeByPR[pr.Index] {
			continue
		}

		a := assessments[pr.Index]

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
		} else if a.Decision == review.Commented {
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("stopped-flow-undecided-pr-%d", pr.Index),
				Kind:        InboxStoppedFlow,
				Title:       fmt.Sprintf("Flow stopped on !%d: verifier undecided", pr.Index),
				Description: fmt.Sprintf("Verifier %s ended review with COMMENT at %s", a.Review.Reviewer, shortSHA(a.Review.CommitID)),
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     a.Review.Submitted,
			})
		} else if a.Decision == review.ChangesRequested {
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("stopped-flow-rounds-pr-%d", pr.Index),
				Kind:        InboxStoppedFlow,
				Title:       fmt.Sprintf("Flow stopped on !%d: changes requested", pr.Index),
				Description: fmt.Sprintf("Changes requested at %s by %s", shortSHA(a.Review.CommitID), a.Review.Reviewer),
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     a.Review.Submitted,
			})
		} else if st, ok := s.flowChecksFailed(ctx, org, repo, pr, ownerLogin); ok && !activeByIssue[parseIssueRef(pr.Head.Ref)] {
			items = append(items, InboxItem{
				ID:          fmt.Sprintf("stopped-flow-checks-pr-%d", pr.Index),
				Kind:        InboxStoppedFlow,
				Title:       fmt.Sprintf("Flow stopped on !%d: checks failed", pr.Index),
				Description: fmt.Sprintf("Post-implement checks failed at %s: %s", shortSHA(pr.Head.Sha), st.Description),
				Target:      fmt.Sprintf("!%d", pr.Index),
				TargetURL:   prURL,
				Created:     st.Created,
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
	// Cleaned up by `bottown run gc`
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
					Label:   "Clean up",
					Command: "bottown run gc",
				},
				Created: r.Started,
			})
		}
	}

	// 2. Server runs with key expired while live
	// Ended (revoked) by `bottown run gc`: the run key no longer
	// authenticates, so `run end` cannot end them.
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
				Description: fmt.Sprintf("Run %s (%s by %s) expired while live; `bottown run gc` ends it as revoked", sr.ID, sr.Kind, sr.Actor),
				Target:      sr.ID,
				TargetURL:   fmt.Sprintf("%s/%s", forgeURL, s.cfg.FullRepo),
				Action: &InboxAction{
					Label:   "Clean up",
					Command: "bottown run gc",
				},
				Created: sr.Expires,
			})
		}
	}

	// 3. Failed run left its worktree behind
	// Cleaned up by `bottown run gc --failed`
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
						Command: "bottown run gc --failed",
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
			if sr.Issue == issue.Index && sr.Kind == "discuss" && sr.Status == "succeeded" && sr.Ended != nil {
				if discussRun == nil || sr.Created.After(discussRun.Created) {
					discussRun = sr
				}
			}
		}
		if discussRun == nil {
			continue // Only issues with an ended discuss run count!
		}

		cPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/issues/%d/comments", org, repo, issue.Index)
		// Forgejo returns all issue comments and ignores page/limit.
		var comments []*sdk.Comment
		_ = s.callServer(ctx, http.MethodGet, cPath, nil, &comments)

		// The answer is the discussant's last comment by the run's end
		// (plus a margin for clock skew): a later comment by the same
		// actor, such as an implement handover, is not the answer and
		// must not hide an owner reply made before it.
		discussActor := discussRun.Actor
		answerTime := *discussRun.Ended
		answerBy := discussRun.Ended.Add(discussAnswerMargin)
		for _, c := range comments {
			poster := userLogin(c.Poster)
			if poster == discussActor && !c.Created.After(answerBy) {
				answerTime = c.Created
			}
		}

		ownerReplied := false
		for _, c := range comments {
			poster := userLogin(c.Poster)
			if poster == ownerLogin && c.Created.After(answerTime) {
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
				Created:     answerTime,
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

		// §3.6: an issue is queued when it has both implement/<actor> and verify/<actor> labels
		hasImplement := false
		hasVerify := false
		for _, lbl := range issue.Labels {
			if lbl != nil {
				if strings.HasPrefix(lbl.Name, "implement/") {
					hasImplement = true
				}
				if strings.HasPrefix(lbl.Name, "verify/") {
					hasVerify = true
				}
			}
		}
		if hasImplement && hasVerify {
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

	prByIndex := make(map[int64]*sdk.PullRequest, len(prs))
	for _, pr := range prs {
		prByIndex[pr.Index] = pr
	}
	for i := range items {
		var targetNum int64
		if _, err := fmt.Sscanf(items[i].Target, "#%d", &targetNum); err == nil {
			items[i].Issue = targetNum
			if issue := issuesByIndex[targetNum]; issue != nil {
				items[i].IssueTitle = issue.Title
			}
			continue
		}
		if _, err := fmt.Sscanf(items[i].Target, "!%d", &targetNum); err == nil {
			if pr := prByIndex[targetNum]; pr != nil {
				items[i].Issue, items[i].IssueTitle = issueForPR(pr)
			}
		}
	}

	// Sort newest first
	slices.SortFunc(items, func(a, b InboxItem) int {
		return b.Created.Compare(a.Created)
	})

	return items, nil
}

// flowChecksFailed returns the flow's latest post-implement check at the
// PR's head (the owner's bottown/e2e status) if it failed. Statuses that
// cannot be read show nothing.
func (s *Server) flowChecksFailed(ctx context.Context, org, repo string, pr *sdk.PullRequest, ownerLogin string) (*sdk.Status, bool) {
	if pr.Head == nil || pr.Head.Sha == "" || ownerLogin == "" {
		return nil, false
	}
	sts, err := listAll[*sdk.Status](ctx, s, fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/commits/%s/statuses?sort=newest", org, repo, pr.Head.Sha))
	if err != nil {
		return nil, false
	}
	var latest *sdk.Status
	for _, st := range sts {
		if st.Context == "bottown/e2e" && userLogin(st.Creator) == ownerLogin && (latest == nil || st.Created.After(latest.Created) || st.Created.Equal(latest.Created) && st.ID > latest.ID) {
			latest = st
		}
	}
	return latest, latest != nil && latest.State == sdk.StatusFailure
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

func (s *Server) resolveOwnerLogin(ctx context.Context) string {
	if s.cfg.Instance.OwnerLogin != "" {
		return s.cfg.Instance.OwnerLogin
	}
	var who struct {
		Actor string `json:"actor"`
	}
	if err := s.callServer(ctx, http.MethodGet, "/v1/whoami", nil, &who); err == nil && who.Actor != "" {
		return who.Actor
	}
	return ""
}

func actorFamilies(actorMap map[string]*ActorDetail) map[string]string {
	families := make(map[string]string, len(actorMap))
	for name, a := range actorMap {
		if a != nil {
			families[name] = a.Family
		}
	}
	return families
}

func (s *Server) assessPullRequest(ctx context.Context, repo string, pr *sdk.PullRequest, ownerLogin string, families map[string]string, allRuns []secrets.Run) (review.Assessment, error) {
	if pr == nil {
		return review.Assessment{}, fmt.Errorf("nil pull request")
	}
	carries := s.prCarries(ctx, repo, pr.Index)
	org, _ := s.orgAndRepo()
	revPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls/%d/reviews", org, repo, pr.Index)
	revs, err := listAll[*sdk.PullReview](ctx, s, revPath)
	if err != nil {
		return review.Assessment{}, err
	}
	head := ""
	if pr.Head != nil {
		head = pr.Head.Sha
	}
	return review.Assess(head, review.FromForge(revs), review.Policy{
		Author:   userLogin(pr.Poster),
		Owner:    ownerLogin,
		Families: families,
		Grants:   review.Grants(allRuns, repo, pr.Index),
		Carries:  carries,
	}), nil
}

func (s *Server) assessOpenPullRequests(ctx context.Context, repo string, prs []*sdk.PullRequest, ownerLogin string, families map[string]string, allRuns []secrets.Run) map[int64]review.Assessment {
	assessments := make(map[int64]review.Assessment)
	for _, pr := range prs {
		if pr == nil || pr.State != sdk.StateOpen || pr.HasMerged {
			continue
		}
		a, err := s.assessPullRequest(ctx, repo, pr, ownerLogin, families, allRuns)
		if err != nil {
			continue
		}
		assessments[pr.Index] = a
	}
	return assessments
}

// approvedDescription says who approved a ready PR, and why an approval at
// an older head counts.
func approvedDescription(a review.Assessment, head, approver string) string {
	d := fmt.Sprintf("Approved at %s by %s (%s)", shortSHA(head), a.Review.Reviewer, approver)
	if a.Reason != "" {
		d += ": " + a.Reason
	}
	return d
}

// prCarries asks the server which older heads' approvals carry over to the
// head of PR n (internal/review): the web UI has no git to compare
// patches. An error leaves none, which only withholds the carry-over.
func (s *Server) prCarries(ctx context.Context, repo string, n int64) []review.Carry {
	var carries []review.Carry
	path := fmt.Sprintf("/v1/forge/pr/carries?repo=%s&number=%d", url.QueryEscape(repo), n)
	if err := s.callServer(ctx, http.MethodGet, path, nil, &carries); err != nil {
		return nil
	}
	return carries
}

func (s *Server) prMergeStatus(ctx context.Context, repo string, n int64) (serverapi.PRMergeStatus, error) {
	var status serverapi.PRMergeStatus
	path := fmt.Sprintf("/v1/forge/pr/merge-base?repo=%s&number=%d", url.QueryEscape(repo), n)
	err := s.callServer(ctx, http.MethodGet, path, nil, &status)
	return status, err
}
