package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/review"
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

// requireRole refuses principals outside a run of one of roles; the first
// is the one error messages suggest starting.
func requireRole(p secrets.Principal, roles ...string) error {
	needs := make([]string, len(roles))
	for i, r := range roles {
		needs[i] = "a " + r + " run"
		if r == policy.Implement {
			needs[i] = "an " + r + " run"
		}
	}
	need := strings.Join(needs, " or ")
	if p.Run == nil {
		return ferr(http.StatusForbidden, "needs %s (start one with `bottown run start %s N`)", need, roles[0])
	}
	if !slices.Contains(roles, p.Run.Kind) {
		return ferr(http.StatusForbidden, "not allowed in a %s run (needs %s)", p.Run.Kind, need)
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
	// Attachments are the files on the description and the comments, as
	// metadata: the bytes are at GET /v1/forge/issue/attachment.
	Attachments []AttachmentOut `json:"attachments"`
	// AttachmentsError says why the attachments could not be listed; the
	// rest of the issue is still complete.
	AttachmentsError string `json:"attachments_error,omitempty"`
}

// AttachmentOut is a file attached to an issue or one of its comments.
type AttachmentOut struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Comment int64     `json:"comment_id,omitempty"` // 0: on the description
	Created time.Time `json:"created"`
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
	Number     int64       `json:"number"`
	Title      string      `json:"title"`
	Body       string      `json:"body"`
	State      string      `json:"state"`
	Merged     bool        `json:"merged"`
	Author     string      `json:"author"`
	HeadBranch string      `json:"head_branch"`
	HeadSHA    string      `json:"head_sha"`
	BaseBranch string      `json:"base_branch"`
	MergeBase  string      `json:"merge_base,omitempty"` // where the head branched from the base
	Mergeable  bool        `json:"mergeable"`
	Reviews    []ReviewOut `json:"reviews"`
	// Assessment is where the reviews leave the PR (internal/review).
	Assessment review.Assessment `json:"assessment"`
	// Carries are the older heads whose approvals carry over to the head,
	// whether or not one of them decides the assessment.
	Carries []review.Carry `json:"carries,omitempty"`
	// Checks are the commit statuses at the head, the newest per context
	// and creator (bottown check writes bottown/check and bottown/e2e).
	Checks []CommitStatus `json:"checks"`
	// ChecksError says why the statuses could not be read.
	ChecksError   string `json:"checks_error,omitempty"`
	Diff          string `json:"diff,omitempty"`
	DiffTruncated bool   `json:"diff_truncated,omitempty"`
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

// PRCreateOut is a created or already-open pull request.
type PRCreateOut struct {
	Number      int64  `json:"number"`
	URL         string `json:"url"`
	Head        string `json:"head"`
	Base        string `json:"base"`
	HeadSHA     string `json:"head_sha,omitempty"`
	AlreadyOpen bool   `json:"already_open,omitempty"`
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
	Merged      bool   `json:"merged,omitempty"`
	MergedBy    string `json:"merged_by,omitempty"`
	Author      string `json:"author"`
	HeadBranch  string `json:"head_branch,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	BaseBranch  string `json:"base_branch,omitempty"`
	Mergeable   bool   `json:"mergeable"`
	Milestone   string `json:"milestone,omitempty"` // the milestone's title
	ReviewState string `json:"review_state"`
	// ReviewReason says why, when an approval carried over (internal/review).
	ReviewReason string `json:"review_reason,omitempty"`
	// Assessment is where the reviews leave an open PR, whose decision is
	// ReviewState; nil for closed and merged PRs, or when the reviews
	// could not be read.
	Assessment *review.Assessment `json:"assessment,omitempty"`
	URL        string             `json:"url"`
	Created    *time.Time         `json:"created,omitempty"`
	Updated    *time.Time         `json:"updated,omitempty"`
	MergedAt   *time.Time         `json:"merged_at,omitempty"`
	ClosedAt   *time.Time         `json:"closed_at,omitempty"`
}

// NewPRItem is pr's list entry without its review state, which needs the
// reviews (opPRList fills it in).
func NewPRItem(pr *sdk.PullRequest) PRItem {
	item := PRItem{
		Number:    pr.Index,
		Title:     pr.Title,
		State:     string(pr.State),
		Merged:    pr.HasMerged,
		MergedBy:  login(pr.MergedBy),
		Author:    login(pr.Poster),
		Mergeable: pr.Mergeable,
		URL:       pr.HTMLURL,
		Created:   pr.Created,
		Updated:   pr.Updated,
		MergedAt:  pr.Merged,
		ClosedAt:  pr.Closed,
	}
	if pr.Head != nil {
		item.HeadBranch, item.HeadSHA = pr.Head.Ref, pr.Head.Sha
	}
	if pr.Base != nil {
		item.BaseBranch = pr.Base.Ref
	}
	if pr.Milestone != nil {
		item.Milestone = pr.Milestone.Title
	}
	return item
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
	cs, _, err := c.AllComments(org, repo, n, sdk.ListIssueCommentOptions{})
	if err != nil {
		return IssueOut{}, err
	}
	out.Comments = []CommentOut{}
	for _, cm := range cs {
		out.Comments = append(out.Comments, CommentOut{ID: cm.ID, Author: login(cm.Poster), Body: cm.Body, Created: cm.Created})
	}
	out.Attachments = []AttachmentOut{}
	as, err := issueFiles(c, org, repo, n)
	if err != nil {
		out.AttachmentsError = err.Error()
	}
	for _, a := range as {
		out.Attachments = append(out.Attachments, AttachmentOut{ID: a.ID, Name: a.Name, Size: a.Size, Comment: a.Comment, Created: a.Created})
	}
	return out, nil
}

// issueFiles lists an issue's uploaded attachments; external links, which
// Forgejo also lists as assets, have no bytes to fetch.
func issueFiles(c *forgejo.Client, org, repo string, n int64) ([]forgejo.Attachment, error) {
	as, err := c.IssueAttachments(org, repo, n)
	if err != nil {
		return nil, fmt.Errorf("listing attachments: %w", err)
	}
	files := as[:0]
	for _, a := range as {
		if a.Type == "" || a.Type == "attachment" {
			files = append(files, a)
		}
	}
	return files, nil
}

// Download is an attachment's contents, for the caller to stream and close.
type Download struct {
	AttachmentOut
	ContentType string
	Length      int64 // -1 if unknown
	Body        io.ReadCloser
}

// opAttachment opens attachment id of an issue (by default the run's), with
// the actor's token. The id must be attached to that issue, so a run key
// reaches only its repository's files, as for the rest of the issue.
func (s *Server) opAttachment(ctx context.Context, p secrets.Principal, in Target, id int64) (*Download, error) {
	def := int64(0)
	if p.Run != nil {
		def = p.Run.Issue
	}
	org, repo, n, err := s.resolve(p, in, def)
	if err != nil {
		return nil, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return nil, err
	}
	as, err := issueFiles(c, org, repo, n)
	if forgejo.IsRawNotFound(err) {
		return nil, ferr(http.StatusNotFound, "issue #%d not found in %s", n, repo)
	} else if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(as, func(a forgejo.Attachment) bool { return a.ID == id })
	if i < 0 {
		return nil, ferr(http.StatusNotFound, "attachment %d is not on issue #%d in %s", id, n, repo)
	}
	a := as[i]
	resp, err := c.DownloadAttachment(a.UUID)
	if err != nil {
		return nil, fmt.Errorf("downloading attachment %d: %w", id, err)
	}
	return &Download{
		AttachmentOut: AttachmentOut{ID: a.ID, Name: a.Name, Size: a.Size, Comment: a.Comment, Created: a.Created},
		ContentType:   resp.Header.Get("Content-Type"),
		Length:        resp.ContentLength,
		Body:          resp.Body,
	}, nil
}

// opIssueCreate files an issue as the actor. Unlike other writes it is
// allowed with a read-only key outside a run, so agents can file issues
// from a conversation with the owner; run keys stay in their repository.
func (s *Server) opIssueCreate(ctx context.Context, p secrets.Principal, in IssueCreateIn) (IssueCreateOut, error) {
	if p.Actor != s.Owner && s.desired().DisableActorIssueCreate {
		s.toolAudit(p, "", "issue_create", "deny", "disabled")
		return IssueCreateOut{}, ferr(http.StatusForbidden, "actors may not file issues on this instance")
	}
	if p.Run != nil && p.Run.Kind == policy.Plan {
		// The plan's issues are created by the owner's milestone apply,
		// from the reviewed plan file; the planner proposes, never files.
		s.toolAudit(p, "", "issue_create", "deny", "role-plan-no-issues")
		return IssueCreateOut{}, ferr(http.StatusForbidden, "plan runs may not create issues")
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
	issues, resp, err := c.AllIssues(org, repo, sdk.ListIssueOption{
		State: state,
		Type:  sdk.IssueTypeIssue,
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
		Author: login(pr.Poster), MergeBase: pr.MergeBase, Mergeable: pr.Mergeable, Reviews: []ReviewOut{},
	}
	if pr.Head != nil {
		out.HeadBranch, out.HeadSHA = pr.Head.Ref, pr.Head.Sha
	}
	if pr.Base != nil {
		out.BaseBranch = pr.Base.Ref
	}
	rs, _, err := c.AllReviews(org, repo, n, sdk.ListPullReviewsOptions{})
	if err != nil {
		return PROut{}, err
	}
	carries := s.carryOver(ctx, org, repo, pr, rs)
	for _, r := range rs {
		out.Reviews = append(out.Reviews, ReviewOut{
			ID: r.ID, Reviewer: login(r.Reviewer), State: string(r.State), CommitID: r.CommitID,
			Body: r.Body, Stale: r.Stale, Dismissed: r.Dismissed, Official: r.Official,
		})
	}
	runs, err := s.runRecords()
	if err != nil {
		return PROut{}, err
	}
	out.Assessment = assessReviews(s.desired(), s.Owner, runs, repo, pr, rs, carries)
	out.Carries = carries
	out.Checks = []CommitStatus{}
	if out.HeadSHA != "" {
		if sts, _, err := c.AllStatuses(org, repo, out.HeadSHA); err != nil {
			out.ChecksError = err.Error()
		} else {
			out.Checks = latestStatuses(sts)
			s.markGate(out.Checks)
		}
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

// opCarries lists the older heads of an open PR whose approvals carry over
// to its head (carry.go), for callers that assess reviews themselves (the
// web UI).
func (s *Server) opCarries(ctx context.Context, p secrets.Principal, in Target) ([]review.Carry, error) {
	def := int64(0)
	if p.Run != nil {
		def = p.Run.PR
	}
	org, repo, n, err := s.resolve(p, in, def)
	if err != nil {
		return nil, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return nil, err
	}
	pr, resp, err := c.GetPullRequest(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return nil, ferr(http.StatusNotFound, "pull request #%d not found in %s", n, repo)
	} else if err != nil {
		return nil, err
	}
	rs, _, err := c.AllReviews(org, repo, n, sdk.ListPullReviewsOptions{})
	if err != nil {
		return nil, err
	}
	carries := s.carryOver(ctx, org, repo, pr, rs)
	if carries == nil {
		carries = []review.Carry{}
	}
	return carries, nil
}

func (s *Server) opPRCreate(ctx context.Context, p secrets.Principal, in PRCreateIn) (PRCreateOut, error) {
	if err := requireRole(p, policy.Implement, policy.Plan); err != nil {
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
	body := strings.TrimSpace(in.Body) + fmt.Sprintf("\n\nCloses #%d\n\n_bottown %s run %s (%s, %s)_", run.Issue, run.Kind, run.ID, run.Actor, run.Family)
	pr, _, err := c.CreatePullRequest(run.Org, run.Repo, sdk.CreatePullRequestOption{
		Head: run.Branch, Base: repo.DefaultBranch, Title: in.Title, Body: body,
	})
	alreadyOpen := false
	if err != nil {
		prs, _, listErr := c.AllPullRequests(run.Org, run.Repo, sdk.ListPullRequestsOptions{State: sdk.StateOpen})
		if listErr != nil {
			return PRCreateOut{}, err
		}
		for _, existing := range prs {
			if existing.Head != nil && existing.Base != nil && existing.Head.Ref == run.Branch && existing.Base.Ref == repo.DefaultBranch {
				pr, alreadyOpen = existing, true
				break
			}
		}
		if !alreadyOpen {
			return PRCreateOut{}, err
		}
	}
	s.toolAudit(p, "", "pr_create", "allow", fmt.Sprintf("#%d", pr.Index))
	s.recordOutcome(run.ID, secrets.OutcomePROpened)
	out := PRCreateOut{Number: pr.Index, URL: pr.HTMLURL, Head: run.Branch, Base: repo.DefaultBranch, AlreadyOpen: alreadyOpen}
	if pr.Head != nil {
		out.HeadSHA = pr.Head.Sha
	}
	return out, nil
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
	prs, resp, err := c.AllPullRequests(org, repo, sdk.ListPullRequestsOptions{
		State: state,
	})
	if forgejo.IsNotFound(resp, err) {
		return nil, ferr(http.StatusNotFound, "repository %s/%s not found", org, repo)
	} else if err != nil {
		return nil, err
	}
	runs, err := s.runRecords()
	if err != nil {
		return nil, err
	}
	out := make([]PRItem, 0, len(prs))
	for _, pr := range prs {
		item := NewPRItem(pr)
		if pr.HasMerged {
			item.ReviewState = "merged"
		} else if pr.State == sdk.StateClosed {
			item.ReviewState = "closed"
		} else {
			rs, _, err := c.AllReviews(org, repo, pr.Index, sdk.ListPullReviewsOptions{})
			if err != nil {
				item.ReviewState = "unknown"
			} else {
				a := assessReviews(s.desired(), s.Owner, runs, repo, pr, rs, s.carryOver(ctx, org, repo, pr, rs))
				item.ReviewState, item.ReviewReason, item.Assessment = string(a.Decision), a.Reason, &a
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// runRecords lists every run record, purged ones included: the review
// assessment reads owner overrides from them.
func (s *Server) runRecords() ([]secrets.Run, error) {
	if s.Store == nil {
		return nil, nil
	}
	st, err := s.Store.Read()
	if err != nil {
		return nil, err
	}
	runs := make([]secrets.Run, 0, len(st.Runs)+len(st.History))
	for _, run := range st.Runs {
		runs = append(runs, run)
	}
	for _, run := range st.History {
		runs = append(runs, run)
	}
	return runs, nil
}

// assessReviews applies the shared review assessment (internal/review) to
// an open PR's reviews, with the owner overrides from runs.
func assessReviews(d config.Desired, owner string, runs []secrets.Run, repo string, pr *sdk.PullRequest, rs []*sdk.PullReview, carries []review.Carry) review.Assessment {
	families := map[string]string{}
	for _, a := range d.Actors {
		families[a.Name] = a.Family
	}
	head := ""
	if pr.Head != nil {
		head = pr.Head.Sha
	}
	return review.Assess(head, review.FromForge(rs), review.Policy{
		Author: login(pr.Poster), Owner: owner, Families: families, Grants: review.Grants(runs, repo, pr.Index), Carries: carries,
	})
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

// reviewOutcomes maps a submitted review's event to the run outcome.
var reviewOutcomes = map[sdk.ReviewStateType]string{
	sdk.ReviewStateApproved:       secrets.OutcomeApproved,
	sdk.ReviewStateRequestChanges: secrets.OutcomeChangesRequested,
	sdk.ReviewStateComment:        secrets.OutcomeCommented,
}

// recordOutcome stores what a run's tool call produced on its record. The
// forge write has already happened, so a failure is logged, not returned.
func (s *Server) recordOutcome(runID, outcome string) {
	if _, err := s.Store.SetOutcome(runID, outcome); err != nil {
		s.Log.Error("record run outcome", "run", runID, "outcome", outcome, "err", err)
	}
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
	s.recordOutcome(run.ID, reviewOutcomes[event])
	return ReviewOut{ID: rv.ID, Reviewer: login(rv.Reviewer), State: string(rv.State), CommitID: rv.CommitID, Body: rv.Body, Official: rv.Official}, nil
}
