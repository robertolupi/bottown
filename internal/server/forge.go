package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/secrets"
)

// Forge operations for agents, shared by the MCP tools and the REST
// endpoints the bottown CLI calls (/v1/forge/...), so both get identical
// checks. With a run key every operation defaults to the run's repository
// and issue or PR and refuses other repositories. Calls are made with the
// actor's own Forgejo token, so the forge attributes them to the actor.

const maxDiffBytes = 200 << 10

// forgeError carries the HTTP status a REST caller should see.
type forgeError struct {
	status int
	msg    string
}

func (e *forgeError) Error() string { return e.msg }

func ferr(status int, format string, args ...any) error {
	return &forgeError{status, fmt.Sprintf(format, args...)}
}

func errStatus(err error) int {
	var fe *forgeError
	if errors.As(err, &fe) {
		return fe.status
	}
	return http.StatusBadGateway
}

// Target selects a repository item; both fields default to the run's.
type Target struct {
	Repo   string `json:"repo,omitempty" jsonschema:"repository name in the bottown org; defaults to the run's repository"`
	Number int64  `json:"number,omitempty" jsonschema:"issue or pull request number; defaults to the run's"`
}

// resolveRepo resolves and confines a repository name for an actor/run.
func (s *Server) resolveRepo(p secrets.Principal, repo string) (string, string, error) {
	org := s.desired().Org
	if o, r, ok := strings.Cut(repo, "/"); ok {
		if strings.EqualFold(o, org) {
			repo = r
		}
	}
	if p.Run != nil {
		if repo != "" && !strings.EqualFold(repo, p.Run.Repo) {
			return "", "", ferr(http.StatusForbidden, "this run is confined to %s/%s", p.Run.Org, p.Run.Repo)
		}
		repo = p.Run.Repo
	}
	if repo == "" {
		return "", "", ferr(http.StatusBadRequest, "repo is required without a run")
	}
	if strings.Contains(repo, "/") {
		return "", "", ferr(http.StatusNotFound, "repository %s not found", repo)
	}
	return org, repo, nil
}

// resolve fills a Target from the run, and confines run keys to their repo.
func (s *Server) resolve(p secrets.Principal, t Target, def int64) (string, string, int64, error) {
	if p.Run != nil && t.Number == 0 {
		t.Number = def
	}
	org, repo, err := s.resolveRepo(p, t.Repo)
	if err != nil {
		return "", "", 0, err
	}
	if t.Number <= 0 {
		return "", "", 0, ferr(http.StatusBadRequest, "number is required without a run")
	}
	return org, repo, t.Number, nil
}

func (s *Server) actorClient(ctx context.Context, actor string) (*forgejo.Client, error) {
	token, ok := s.actorToken(actor)
	if !ok {
		return nil, ferr(http.StatusForbidden, "no forge token for %s", actor)
	}
	return forgejo.New(ctx, s.Rec.BaseURL, token), nil
}

func requireRole(p secrets.Principal, role string) error {
	if p.Run == nil {
		return ferr(http.StatusForbidden, "needs a %s run (start one with `bottown run start %s N`)", role, role)
	}
	if p.Run.Kind != role {
		return ferr(http.StatusForbidden, "not allowed in a %s run (needs a %s run)", p.Run.Kind, role)
	}
	return nil
}

// requireWrite refuses read-only principals: outside a run, actor keys may
// only read and start runs.
func requireWrite(p secrets.Principal) error {
	if p.ReadOnly && p.Run == nil {
		return ferr(http.StatusForbidden, "read-only outside a run: start one with `bottown run start`")
	}
	return nil
}

// toolAudit records the decision on one of the forge operations; repo is
// org/name, empty when the operation did not get as far as resolving one.
func (s *Server) toolAudit(p secrets.Principal, repo, op, decision, detail string) {
	if repo == "" {
		repo = runRepo(p)
	}
	s.record(AuditEntry{Actor: p.Actor, Run: runID(p), Repo: repo, Op: op, Decision: decision, Detail: detail})
}

// IssueOut is an issue with its discussion.
type IssueOut struct {
	Number   int64        `json:"number"`
	Title    string       `json:"title"`
	Body     string       `json:"body"`
	State    string       `json:"state"`
	Author   string       `json:"author"`
	Labels   []string     `json:"labels"`
	Comments []CommentOut `json:"comments"`
}

// CommentOut is one comment.
type CommentOut struct {
	ID      int64     `json:"id"`
	Author  string    `json:"author"`
	Body    string    `json:"body"`
	Created time.Time `json:"created"`
}

// PROut is a pull request with what a reviewer needs.
type PROut struct {
	Number        int64       `json:"number"`
	Title         string      `json:"title"`
	Body          string      `json:"body"`
	State         string      `json:"state"`
	Merged        bool        `json:"merged"`
	Author        string      `json:"author"`
	HeadBranch    string      `json:"head_branch"`
	HeadSHA       string      `json:"head_sha"`
	BaseBranch    string      `json:"base_branch"`
	Mergeable     bool        `json:"mergeable"`
	Reviews       []ReviewOut `json:"reviews"`
	Diff          string      `json:"diff,omitempty"`
	DiffTruncated bool        `json:"diff_truncated,omitempty"`
}

// ReviewOut is one review.
type ReviewOut struct {
	ID        int64  `json:"id"`
	Reviewer  string `json:"reviewer"`
	State     string `json:"state"`
	CommitID  string `json:"commit_id"`
	Body      string `json:"body"`
	Stale     bool   `json:"stale"`
	Dismissed bool   `json:"dismissed"`
	Official  bool   `json:"official"`
}

// PRCreateIn is the input of pr create.
type PRCreateIn struct {
	Title string `json:"title" jsonschema:"pull request title"`
	Body  string `json:"body,omitempty" jsonschema:"description; bottown appends 'Closes #<issue>'"`
}

// PRCreateOut is a created pull request.
type PRCreateOut struct {
	Number int64  `json:"number"`
	URL    string `json:"url"`
	Head   string `json:"head"`
	Base   string `json:"base"`
}

// IssueCreateIn is the input of issue create.
type IssueCreateIn struct {
	Repo  string `json:"repo,omitempty" jsonschema:"repository name in the bottown org; defaults to the run's repository, required outside a run"`
	Title string `json:"title" jsonschema:"issue title"`
	Body  string `json:"body,omitempty" jsonschema:"issue description (markdown); bottown appends who filed it"`
}

// IssueCreateOut is a created issue.
type IssueCreateOut struct {
	Number int64  `json:"number"`
	URL    string `json:"url"`
	Repo   string `json:"repo"`
}

// IssueListIn is the input of issue list.
type IssueListIn struct {
	Repo  string `json:"repo,omitempty" jsonschema:"repository name in the bottown org; defaults to the run's repository, required outside a run"`
	State string `json:"state,omitempty" jsonschema:"state filter: open, closed, or all (default: open)"`
}

// IssueItem is an issue summary in a list.
type IssueItem struct {
	Number int64    `json:"number"`
	Title  string   `json:"title"`
	State  string   `json:"state"`
	Author string   `json:"author"`
	Labels []string `json:"labels,omitempty"`
	URL    string   `json:"url"`
}

// PRListIn is the input of pr list.
type PRListIn struct {
	Repo  string `json:"repo,omitempty" jsonschema:"repository name in the bottown org; defaults to the run's repository, required outside a run"`
	State string `json:"state,omitempty" jsonschema:"state filter: open, closed, or all (default: open)"`
}

// PRItem is a pull request summary in a list.
type PRItem struct {
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Author      string `json:"author"`
	HeadBranch  string `json:"head_branch,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	ReviewState string `json:"review_state"`
	URL         string `json:"url"`
}

// CommentIn is the input of comment.
type CommentIn struct {
	Target
	Body string `json:"body" jsonschema:"comment text (markdown)"`
}

// ReviewIn is the input of review submit.
type ReviewIn struct {
	Event    string `json:"event" jsonschema:"APPROVED, REQUEST_CHANGES or COMMENT"`
	Body     string `json:"body" jsonschema:"the verdict and its reasons"`
	CommitID string `json:"commit_id" jsonschema:"the SHA you verified; must be the run's SHA and the PR's current head"`
}

func login(u *sdk.User) string {
	if u == nil {
		return ""
	}
	return u.UserName
}

func runDefault(p secrets.Principal) int64 {
	if p.Run == nil {
		return 0
	}
	if p.Run.Kind == policy.Verify {
		return p.Run.PR
	}
	return p.Run.Issue
}

func (s *Server) opIssue(ctx context.Context, p secrets.Principal, in Target) (IssueOut, error) {
	def := int64(0)
	if p.Run != nil {
		def = p.Run.Issue
	}
	org, repo, n, err := s.resolve(p, in, def)
	if err != nil {
		return IssueOut{}, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return IssueOut{}, err
	}
	is, resp, err := c.GetIssue(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return IssueOut{}, ferr(http.StatusNotFound, "issue #%d not found in %s", n, repo)
	} else if err != nil {
		return IssueOut{}, err
	}
	out := IssueOut{Number: is.Index, Title: is.Title, Body: is.Body, State: string(is.State), Author: login(is.Poster), Labels: []string{}}
	for _, l := range is.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	cs, _, err := c.ListIssueComments(org, repo, n, sdk.ListIssueCommentOptions{ListOptions: sdk.ListOptions{PageSize: 50}})
	if err != nil {
		return IssueOut{}, err
	}
	out.Comments = []CommentOut{}
	for _, cm := range cs {
		out.Comments = append(out.Comments, CommentOut{ID: cm.ID, Author: login(cm.Poster), Body: cm.Body, Created: cm.Created})
	}
	return out, nil
}

// opIssueCreate files an issue as the actor. Unlike other writes it is
// allowed with a read-only key outside a run, so agents can file issues
// from a conversation with the owner; run keys stay in their repository.
func (s *Server) opIssueCreate(ctx context.Context, p secrets.Principal, in IssueCreateIn) (IssueCreateOut, error) {
	if p.Actor != s.Owner && s.desired().DisableActorIssueCreate {
		s.toolAudit(p, "", "issue_create", "deny", "disabled")
		return IssueCreateOut{}, ferr(http.StatusForbidden, "actors may not file issues on this instance")
	}
	if strings.TrimSpace(in.Title) == "" {
		return IssueCreateOut{}, ferr(http.StatusBadRequest, "title is required")
	}
	org, repo, err := s.resolveRepo(p, in.Repo)
	if err != nil {
		if errStatus(err) == http.StatusForbidden {
			s.toolAudit(p, "", "issue_create", "deny", err.Error())
		}
		return IssueCreateOut{}, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return IssueCreateOut{}, err
	}
	body := strings.TrimSpace(in.Body)
	if p.Actor != s.Owner {
		by := p.Actor
		for _, a := range s.desired().Actors {
			if a.Name == p.Actor && a.Family != "" {
				by = fmt.Sprintf("%s, %s", a.Name, a.Family)
			}
		}
		if p.Run != nil {
			body += fmt.Sprintf("\n\n_filed during bottown %s run %s (%s)_", p.Run.Kind, p.Run.ID, by)
		} else {
			body += fmt.Sprintf("\n\n_filed by %s outside a run_", by)
		}
	}
	is, resp, err := c.CreateIssue(org, repo, sdk.CreateIssueOption{Title: strings.TrimSpace(in.Title), Body: strings.TrimSpace(body)})
	if forgejo.IsNotFound(resp, err) {
		return IssueCreateOut{}, ferr(http.StatusNotFound, "repository %s/%s not found", org, repo)
	} else if err != nil {
		return IssueCreateOut{}, err
	}
	s.toolAudit(p, org+"/"+repo, "issue_create", "allow", fmt.Sprintf("%s#%d", repo, is.Index))
	return IssueCreateOut{Number: is.Index, URL: is.HTMLURL, Repo: repo}, nil
}

func (s *Server) opIssueList(ctx context.Context, p secrets.Principal, in IssueListIn) ([]IssueItem, error) {
	org, repo, err := s.resolveRepo(p, in.Repo)
	if err != nil {
		return nil, err
	}
	state, err := parseState(in.State)
	if err != nil {
		return nil, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return nil, err
	}
	issues, resp, err := c.ListRepoIssues(org, repo, sdk.ListIssueOption{
		State:       state,
		Type:        sdk.IssueTypeIssue,
		ListOptions: sdk.ListOptions{PageSize: 50},
	})
	if forgejo.IsNotFound(resp, err) {
		return nil, ferr(http.StatusNotFound, "repository %s/%s not found", org, repo)
	} else if err != nil {
		return nil, err
	}
	out := make([]IssueItem, 0, len(issues))
	for _, is := range issues {
		item := IssueItem{
			Number: is.Index,
			Title:  is.Title,
			State:  string(is.State),
			Author: login(is.Poster),
			URL:    is.HTMLURL,
			Labels: []string{},
		}
		for _, l := range is.Labels {
			item.Labels = append(item.Labels, l.Name)
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Server) opPR(ctx context.Context, p secrets.Principal, in Target, withDiff bool) (PROut, error) {
	def := int64(0)
	if p.Run != nil {
		def = p.Run.PR
	}
	org, repo, n, err := s.resolve(p, in, def)
	if err != nil {
		return PROut{}, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return PROut{}, err
	}
	pr, resp, err := c.GetPullRequest(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return PROut{}, ferr(http.StatusNotFound, "pull request #%d not found in %s", n, repo)
	} else if err != nil {
		return PROut{}, err
	}
	out := PROut{
		Number: pr.Index, Title: pr.Title, Body: pr.Body, State: string(pr.State), Merged: pr.HasMerged,
		Author: login(pr.Poster), Mergeable: pr.Mergeable, Reviews: []ReviewOut{},
	}
	if pr.Head != nil {
		out.HeadBranch, out.HeadSHA = pr.Head.Ref, pr.Head.Sha
	}
	if pr.Base != nil {
		out.BaseBranch = pr.Base.Ref
	}
	rs, _, err := c.ListPullReviews(org, repo, n, sdk.ListPullReviewsOptions{ListOptions: sdk.ListOptions{PageSize: 50}})
	if err != nil {
		return PROut{}, err
	}
	for _, r := range rs {
		out.Reviews = append(out.Reviews, ReviewOut{
			ID: r.ID, Reviewer: login(r.Reviewer), State: string(r.State), CommitID: r.CommitID,
			Body: r.Body, Stale: r.Stale, Dismissed: r.Dismissed, Official: r.Official,
		})
	}
	if withDiff {
		diff, _, err := c.GetPullRequestDiff(org, repo, n, sdk.PullRequestDiffOptions{})
		if err != nil {
			return PROut{}, err
		}
		if len(diff) > maxDiffBytes {
			diff, out.DiffTruncated = diff[:maxDiffBytes], true
		}
		out.Diff = string(diff)
	}
	return out, nil
}

func (s *Server) opPRCreate(ctx context.Context, p secrets.Principal, in PRCreateIn) (PRCreateOut, error) {
	if err := requireRole(p, policy.Implement); err != nil {
		s.toolAudit(p, "", "pr_create", "deny", err.Error())
		return PRCreateOut{}, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return PRCreateOut{}, ferr(http.StatusBadRequest, "title is required")
	}
	run := p.Run
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return PRCreateOut{}, err
	}
	repo, _, err := c.GetRepo(run.Org, run.Repo)
	if err != nil {
		return PRCreateOut{}, err
	}
	body := strings.TrimSpace(in.Body) + fmt.Sprintf("\n\nCloses #%d\n\n_bottown implement run %s (%s, %s)_", run.Issue, run.ID, run.Actor, run.Family)
	pr, _, err := c.CreatePullRequest(run.Org, run.Repo, sdk.CreatePullRequestOption{
		Head: run.Branch, Base: repo.DefaultBranch, Title: in.Title, Body: body,
	})
	if err != nil {
		return PRCreateOut{}, err
	}
	s.toolAudit(p, "", "pr_create", "allow", fmt.Sprintf("#%d", pr.Index))
	return PRCreateOut{Number: pr.Index, URL: pr.HTMLURL, Head: run.Branch, Base: repo.DefaultBranch}, nil
}

func (s *Server) opPRList(ctx context.Context, p secrets.Principal, in PRListIn) ([]PRItem, error) {
	org, repo, err := s.resolveRepo(p, in.Repo)
	if err != nil {
		return nil, err
	}
	state, err := parseState(in.State)
	if err != nil {
		return nil, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return nil, err
	}
	prs, resp, err := c.ListRepoPullRequests(org, repo, sdk.ListPullRequestsOptions{
		State:       state,
		ListOptions: sdk.ListOptions{PageSize: 50},
	})
	if forgejo.IsNotFound(resp, err) {
		return nil, ferr(http.StatusNotFound, "repository %s/%s not found", org, repo)
	} else if err != nil {
		return nil, err
	}
	out := make([]PRItem, 0, len(prs))
	for _, pr := range prs {
		item := PRItem{
			Number: pr.Index,
			Title:  pr.Title,
			State:  string(pr.State),
			Author: login(pr.Poster),
			URL:    pr.HTMLURL,
		}
		if pr.Head != nil {
			item.HeadBranch = pr.Head.Ref
			item.HeadSHA = pr.Head.Sha
		}
		if pr.HasMerged {
			item.ReviewState = "merged"
		} else if pr.State == sdk.StateClosed {
			item.ReviewState = "closed"
		} else {
			rs, _, err := c.ListPullReviews(org, repo, pr.Index, sdk.ListPullReviewsOptions{ListOptions: sdk.ListOptions{PageSize: 50}})
			if err != nil {
				item.ReviewState = "unknown"
			} else {
				item.ReviewState = reviewState(rs)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func reviewState(reviews []*sdk.PullReview) string {
	hasChangesRequested := false
	hasApproved := false
	for _, r := range reviews {
		if r.Stale || r.Dismissed {
			continue
		}
		switch r.State {
		case sdk.ReviewStateRequestChanges:
			hasChangesRequested = true
		case sdk.ReviewStateApproved:
			hasApproved = true
		}
	}
	if hasChangesRequested {
		return "changes requested"
	}
	if hasApproved {
		return "approved"
	}
	return "needs review"
}

func parseState(s string) (sdk.StateType, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "open":
		return sdk.StateOpen, nil
	case "closed":
		return sdk.StateClosed, nil
	case "all":
		return sdk.StateAll, nil
	default:
		return "", ferr(http.StatusBadRequest, "invalid state %q: must be open, closed, or all", s)
	}
}

func (s *Server) opComment(ctx context.Context, p secrets.Principal, in CommentIn) (CommentOut, error) {
	if err := requireWrite(p); err != nil {
		s.toolAudit(p, "", "comment", "deny", err.Error())
		return CommentOut{}, err
	}
	org, repo, n, err := s.resolve(p, in.Target, runDefault(p))
	if err != nil {
		if errStatus(err) == http.StatusForbidden {
			s.toolAudit(p, "", "comment", "deny", err.Error())
		}
		return CommentOut{}, err
	}
	if strings.TrimSpace(in.Body) == "" {
		return CommentOut{}, ferr(http.StatusBadRequest, "body is required")
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return CommentOut{}, err
	}
	cm, _, err := c.CreateIssueComment(org, repo, n, sdk.CreateIssueCommentOption{Body: in.Body})
	if err != nil {
		return CommentOut{}, err
	}
	s.toolAudit(p, org+"/"+repo, "comment", "allow", fmt.Sprintf("%s#%d", repo, n))
	return CommentOut{ID: cm.ID, Author: login(cm.Poster), Body: cm.Body, Created: cm.Created}, nil
}

// opReview submits a verify run's review pinned to the run's commit; it is
// refused if the PR has moved since the run started, because the verdict
// would not cover the new commits.
func (s *Server) opReview(ctx context.Context, p secrets.Principal, in ReviewIn) (ReviewOut, error) {
	if err := requireRole(p, policy.Verify); err != nil {
		s.toolAudit(p, "", "review_submit", "deny", err.Error())
		return ReviewOut{}, err
	}
	run := p.Run
	event := sdk.ReviewStateType(strings.ToUpper(strings.TrimSpace(in.Event)))
	switch event {
	case sdk.ReviewStateApproved, sdk.ReviewStateRequestChanges, sdk.ReviewStateComment:
	default:
		return ReviewOut{}, ferr(http.StatusBadRequest, "event must be APPROVED, REQUEST_CHANGES or COMMENT")
	}
	if in.CommitID != run.SHA {
		s.toolAudit(p, "", "review_submit", "deny", "commit "+in.CommitID+" is not the run's "+run.SHA)
		return ReviewOut{}, ferr(http.StatusConflict, "commit_id %q is not the commit this run verifies (%s)", in.CommitID, run.SHA)
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return ReviewOut{}, err
	}
	pr, _, err := c.GetPullRequest(run.Org, run.Repo, run.PR)
	if err != nil {
		return ReviewOut{}, err
	}
	if pr.Head == nil || pr.Head.Sha != run.SHA {
		head := ""
		if pr.Head != nil {
			head = pr.Head.Sha
		}
		s.toolAudit(p, "", "review_submit", "deny", "head moved to "+head)
		return ReviewOut{}, ferr(http.StatusConflict, "pull request #%d moved to %s since this run started at %s; start a new verify run", run.PR, head, run.SHA)
	}
	body := strings.TrimSpace(in.Body)
	if run.AllowSameFamily {
		body = fmt.Sprintf("> **Same-family review** (%s reviewing %s's work), allowed by owner override.\n\n", run.Family, run.Author) + body
	}
	body += fmt.Sprintf("\n\n_bottown verify run %s (%s, %s) at %s_", run.ID, run.Actor, run.Family, run.SHA)
	rv, _, err := c.CreatePullReview(run.Org, run.Repo, run.PR, sdk.CreatePullReviewOptions{State: event, Body: body, CommitID: run.SHA})
	if err != nil {
		return ReviewOut{}, err
	}
	s.toolAudit(p, "", "review_submit", "allow", fmt.Sprintf("%s #%d at %s", event, run.PR, run.SHA))
	return ReviewOut{ID: rv.ID, Reviewer: login(rv.Reviewer), State: string(rv.State), CommitID: rv.CommitID, Body: rv.Body, Official: rv.Official}, nil
}
