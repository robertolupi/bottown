package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/secrets"
)

// Forge tools for agents. With a run key every tool defaults to the run's
// repository and issue or PR, so agents rarely name them; role checks are
// done here as well as in the proxy. Calls are made with the actor's own
// Forgejo token, so the forge attributes them to the actor.

const (
	maxDiffBytes = 200 << 10
	maxAPIBytes  = 256 << 10
)

// Target selects a repository item; both fields default to the run's.
type Target struct {
	Repo   string `json:"repo,omitempty" jsonschema:"repository name in the bottown org; defaults to the run's repository"`
	Number int64  `json:"number,omitempty" jsonschema:"issue or pull request number; defaults to the run's"`
}

func principal(req *mcp.CallToolRequest) secrets.Principal {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return secrets.Principal{}
	}
	p, _ := req.Extra.TokenInfo.Extra["principal"].(secrets.Principal)
	return p
}

// resolve fills a Target from the run, and confines run keys to their repo.
func (s *Server) resolve(p secrets.Principal, t Target, def int64) (string, string, int64, error) {
	org := s.desired().Org
	if p.Run != nil {
		if t.Repo != "" && !strings.EqualFold(t.Repo, p.Run.Repo) {
			return "", "", 0, fmt.Errorf("this run is confined to %s/%s", p.Run.Org, p.Run.Repo)
		}
		t.Repo = p.Run.Repo
		if t.Number == 0 {
			t.Number = def
		}
	}
	if t.Repo == "" || t.Number <= 0 {
		return "", "", 0, errors.New("repo and number are required without a run key")
	}
	return org, t.Repo, t.Number, nil
}

func (s *Server) actorClient(ctx context.Context, actor string) (*forgejo.Client, error) {
	token, ok := s.actorToken(actor)
	if !ok {
		return nil, fmt.Errorf("no forge token for %s", actor)
	}
	return forgejo.New(ctx, s.Rec.BaseURL, token), nil
}

func requireRole(p secrets.Principal, role string) error {
	if p.Run == nil {
		return fmt.Errorf("needs a %s run key", role)
	}
	if p.Run.Kind != role {
		return fmt.Errorf("not allowed in a %s run (needs a %s run)", p.Run.Kind, role)
	}
	return nil
}

func (s *Server) toolAudit(p secrets.Principal, tool, decision, detail string) {
	run := ""
	if p.Run != nil {
		run = p.Run.ID
	}
	s.Log.Info("mcp", "actor", p.Actor, "run", run, "tool", tool, "decision", decision, "detail", detail)
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
	Diff          string      `json:"diff"`
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

func (s *Server) addForgeTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "issue_get",
		Description: "Read an issue with its labels and comments. Defaults to the run's issue.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in Target) (*mcp.CallToolResult, IssueOut, error) {
		p := principal(req)
		def := int64(0)
		if p.Run != nil {
			def = p.Run.Issue
		}
		org, repo, n, err := s.resolve(p, in, def)
		if err != nil {
			return nil, IssueOut{}, err
		}
		c, err := s.actorClient(ctx, p.Actor)
		if err != nil {
			return nil, IssueOut{}, err
		}
		is, _, err := c.GetIssue(org, repo, n)
		if err != nil {
			return nil, IssueOut{}, err
		}
		out := IssueOut{Number: is.Index, Title: is.Title, Body: is.Body, State: string(is.State), Author: login(is.Poster), Labels: []string{}}
		for _, l := range is.Labels {
			out.Labels = append(out.Labels, l.Name)
		}
		cs, _, err := c.ListIssueComments(org, repo, n, sdk.ListIssueCommentOptions{ListOptions: sdk.ListOptions{PageSize: 50}})
		if err != nil {
			return nil, IssueOut{}, err
		}
		out.Comments = []CommentOut{}
		for _, cm := range cs {
			out.Comments = append(out.Comments, CommentOut{ID: cm.ID, Author: login(cm.Poster), Body: cm.Body, Created: cm.Created})
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "pr_get",
		Description: "Read a pull request: head SHA, branches, reviews and the diff. Defaults to the run's pull request.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in Target) (*mcp.CallToolResult, PROut, error) {
		p := principal(req)
		def := int64(0)
		if p.Run != nil {
			def = p.Run.PR
		}
		org, repo, n, err := s.resolve(p, in, def)
		if err != nil {
			return nil, PROut{}, err
		}
		c, err := s.actorClient(ctx, p.Actor)
		if err != nil {
			return nil, PROut{}, err
		}
		pr, _, err := c.GetPullRequest(org, repo, n)
		if err != nil {
			return nil, PROut{}, err
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
			return nil, PROut{}, err
		}
		for _, r := range rs {
			out.Reviews = append(out.Reviews, ReviewOut{
				ID: r.ID, Reviewer: login(r.Reviewer), State: string(r.State), CommitID: r.CommitID,
				Body: r.Body, Stale: r.Stale, Dismissed: r.Dismissed, Official: r.Official,
			})
		}
		diff, _, err := c.GetPullRequestDiff(org, repo, n, sdk.PullRequestDiffOptions{})
		if err != nil {
			return nil, PROut{}, err
		}
		if len(diff) > maxDiffBytes {
			diff, out.DiffTruncated = diff[:maxDiffBytes], true
		}
		out.Diff = string(diff)
		return nil, out, nil
	})

	type prCreateIn struct {
		Title string `json:"title" jsonschema:"pull request title"`
		Body  string `json:"body,omitempty" jsonschema:"description; bottown appends 'Closes #<issue>'"`
	}
	type prCreateOut struct {
		Number int64  `json:"number"`
		URL    string `json:"url"`
		Head   string `json:"head"`
		Base   string `json:"base"`
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "pr_create",
		Description: "Open a pull request from this implement run's branch into the default branch, closing the run's issue. Push the branch first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prCreateIn) (*mcp.CallToolResult, prCreateOut, error) {
		p := principal(req)
		if err := requireRole(p, policy.Implement); err != nil {
			s.toolAudit(p, "pr_create", "deny", err.Error())
			return nil, prCreateOut{}, err
		}
		if strings.TrimSpace(in.Title) == "" {
			return nil, prCreateOut{}, errors.New("title is required")
		}
		run := p.Run
		c, err := s.actorClient(ctx, p.Actor)
		if err != nil {
			return nil, prCreateOut{}, err
		}
		repo, _, err := c.GetRepo(run.Org, run.Repo)
		if err != nil {
			return nil, prCreateOut{}, err
		}
		body := strings.TrimSpace(in.Body) + fmt.Sprintf("\n\nCloses #%d\n\n_bottown implement run %s (%s, %s)_", run.Issue, run.ID, run.Actor, run.Family)
		pr, _, err := c.CreatePullRequest(run.Org, run.Repo, sdk.CreatePullRequestOption{
			Head: run.Branch, Base: repo.DefaultBranch, Title: in.Title, Body: body,
		})
		if err != nil {
			return nil, prCreateOut{}, err
		}
		s.toolAudit(p, "pr_create", "allow", fmt.Sprintf("#%d", pr.Index))
		return nil, prCreateOut{Number: pr.Index, URL: pr.HTMLURL, Head: run.Branch, Base: repo.DefaultBranch}, nil
	})

	type commentIn struct {
		Target
		Body string `json:"body" jsonschema:"comment text (markdown)"`
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "comment",
		Description: "Comment on an issue or pull request. Defaults to the run's issue (implement) or pull request (verify).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in commentIn) (*mcp.CallToolResult, CommentOut, error) {
		p := principal(req)
		org, repo, n, err := s.resolve(p, in.Target, runDefault(p))
		if err != nil {
			return nil, CommentOut{}, err
		}
		if strings.TrimSpace(in.Body) == "" {
			return nil, CommentOut{}, errors.New("body is required")
		}
		c, err := s.actorClient(ctx, p.Actor)
		if err != nil {
			return nil, CommentOut{}, err
		}
		cm, _, err := c.CreateIssueComment(org, repo, n, sdk.CreateIssueCommentOption{Body: in.Body})
		if err != nil {
			return nil, CommentOut{}, err
		}
		s.toolAudit(p, "comment", "allow", fmt.Sprintf("%s#%d", repo, n))
		return nil, CommentOut{ID: cm.ID, Author: login(cm.Poster), Body: cm.Body, Created: cm.Created}, nil
	})

	type reviewIn struct {
		Event    string `json:"event" jsonschema:"APPROVED, REQUEST_CHANGES or COMMENT"`
		Body     string `json:"body" jsonschema:"the verdict and its reasons"`
		CommitID string `json:"commit_id" jsonschema:"the SHA you verified; must be the run's SHA and the PR's current head"`
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "review_submit",
		Description: "Submit this verify run's review of its pull request, pinned to the commit you verified. " +
			"Refused if the pull request has moved since the run started: the verdict would not cover the new commits.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in reviewIn) (*mcp.CallToolResult, ReviewOut, error) {
		p := principal(req)
		if err := requireRole(p, policy.Verify); err != nil {
			s.toolAudit(p, "review_submit", "deny", err.Error())
			return nil, ReviewOut{}, err
		}
		run := p.Run
		event := sdk.ReviewStateType(strings.ToUpper(strings.TrimSpace(in.Event)))
		switch event {
		case sdk.ReviewStateApproved, sdk.ReviewStateRequestChanges, sdk.ReviewStateComment:
		default:
			return nil, ReviewOut{}, errors.New("event must be APPROVED, REQUEST_CHANGES or COMMENT")
		}
		if in.CommitID != run.SHA {
			s.toolAudit(p, "review_submit", "deny", "commit "+in.CommitID+" is not the run's "+run.SHA)
			return nil, ReviewOut{}, fmt.Errorf("commit_id %q is not the commit this run verifies (%s)", in.CommitID, run.SHA)
		}
		c, err := s.actorClient(ctx, p.Actor)
		if err != nil {
			return nil, ReviewOut{}, err
		}
		pr, _, err := c.GetPullRequest(run.Org, run.Repo, run.PR)
		if err != nil {
			return nil, ReviewOut{}, err
		}
		if pr.Head == nil || pr.Head.Sha != run.SHA {
			head := ""
			if pr.Head != nil {
				head = pr.Head.Sha
			}
			s.toolAudit(p, "review_submit", "deny", "head moved to "+head)
			return nil, ReviewOut{}, fmt.Errorf("pull request #%d moved to %s since this run started at %s; start a new verify run", run.PR, head, run.SHA)
		}
		body := strings.TrimSpace(in.Body)
		if run.AllowSameFamily {
			body = fmt.Sprintf("> **Same-family review** (%s reviewing %s's work), allowed by owner override.\n\n", run.Family, run.Author) + body
		}
		body += fmt.Sprintf("\n\n_bottown verify run %s (%s, %s) at %s_", run.ID, run.Actor, run.Family, run.SHA)
		rv, _, err := c.CreatePullReview(run.Org, run.Repo, run.PR, sdk.CreatePullReviewOptions{State: event, Body: body, CommitID: run.SHA})
		if err != nil {
			return nil, ReviewOut{}, err
		}
		s.toolAudit(p, "review_submit", "allow", fmt.Sprintf("%s #%d at %s", event, run.PR, run.SHA))
		return nil, ReviewOut{ID: rv.ID, Reviewer: login(rv.Reviewer), State: string(rv.State), CommitID: rv.CommitID, Body: rv.Body, Official: rv.Official}, nil
	})

	type apiIn struct {
		Method string `json:"method" jsonschema:"HTTP method"`
		Path   string `json:"path" jsonschema:"Forgejo API path below /api/v1, e.g. /repos/bottown/x/issues"`
		Body   any    `json:"body,omitempty" jsonschema:"JSON request body"`
	}
	type apiOut struct {
		Status    int  `json:"status"`
		Body      any  `json:"body"`
		Truncated bool `json:"truncated,omitempty"`
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "forge_api",
		Description: "Call any Forgejo API endpoint as yourself, under the same bottown policy as the HTTP proxy " +
			"(run keys stay in their repository and role; merges, statuses, labels and admin are bottown's).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in apiIn) (*mcp.CallToolResult, apiOut, error) {
		p := principal(req)
		if !strings.HasPrefix(in.Path, "/") {
			return nil, apiOut{}, errors.New("path must start with /")
		}
		var body *bytes.Reader
		if in.Body != nil {
			b, err := json.Marshal(in.Body)
			if err != nil {
				return nil, apiOut{}, err
			}
			body = bytes.NewReader(b)
		} else {
			body = bytes.NewReader(nil)
		}
		path, query, _ := strings.Cut(in.Path, "?")
		hr := httptest.NewRequest(strings.ToUpper(in.Method), "/forgejo/api/v1"+path, body)
		hr.URL.RawQuery = query
		hr.Header.Set("Content-Type", "application/json")
		hr = hr.WithContext(context.WithValue(ctx, ctxKey{}, p))
		rec := httptest.NewRecorder()
		// The same handler as the HTTP proxy, so the policy is identical.
		s.apiProxy(rec, hr)
		out := apiOut{Status: rec.Code}
		raw := rec.Body.Bytes()
		if len(raw) > maxAPIBytes {
			raw, out.Truncated = raw[:maxAPIBytes], true
		}
		var v any
		if !out.Truncated && json.Unmarshal(raw, &v) == nil {
			out.Body = v
		} else {
			out.Body = string(raw)
		}
		if rec.Code == http.StatusForbidden {
			s.toolAudit(p, "forge_api", "deny", in.Method+" "+path)
		}
		return nil, out, nil
	})
}
