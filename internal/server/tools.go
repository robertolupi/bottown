package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robertolupi/bottown/internal/secrets"
)

// MCP front end for the forge operations in forge.go.

const maxAPIBytes = 256 << 10

func principal(req *mcp.CallToolRequest) secrets.Principal {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return secrets.Principal{}
	}
	p, _ := req.Extra.TokenInfo.Extra["principal"].(secrets.Principal)
	return p
}

func (s *Server) addForgeTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "issue_get",
		Description: "Read an issue with its labels and comments. Defaults to the run's issue.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in Target) (*mcp.CallToolResult, IssueOut, error) {
		out, err := s.opIssue(ctx, principal(req), in)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "issue_create",
		Description: "File a new issue as yourself, e.g. a follow-up from a discussion with the owner. " +
			"Works outside a run (give repo); in a run it defaults to, and stays in, the run's repository.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in IssueCreateIn) (*mcp.CallToolResult, IssueCreateOut, error) {
		out, err := s.opIssueCreate(ctx, principal(req), in)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "issue_list",
		Description: "List issues in a repository. Defaults to the run's repository; outside a run repo is required. " +
			"State filter can be open (default), closed, or all.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in IssueListIn) (*mcp.CallToolResult, []IssueItem, error) {
		out, err := s.opIssueList(ctx, principal(req), in)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "pr_get",
		Description: "Read a pull request: head SHA, branches, reviews and the diff. Defaults to the run's pull request.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in Target) (*mcp.CallToolResult, PROut, error) {
		out, err := s.opPR(ctx, principal(req), in, true)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "pr_list",
		Description: "List pull requests in a repository with author, head SHA, and review state. " +
			"Defaults to the run's repository; outside a run repo is required. " +
			"State filter can be open (default), closed, or all.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PRListIn) (*mcp.CallToolResult, []PRItem, error) {
		out, err := s.opPRList(ctx, principal(req), in)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "pr_create",
		Description: "Open a pull request from this implement run's branch into the default branch, closing the run's issue. Push the branch first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PRCreateIn) (*mcp.CallToolResult, PRCreateOut, error) {
		out, err := s.opPRCreate(ctx, principal(req), in)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "comment",
		Description: "Comment on an issue or pull request. Defaults to the run's issue (implement) or pull request (verify).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CommentIn) (*mcp.CallToolResult, CommentOut, error) {
		out, err := s.opComment(ctx, principal(req), in)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "review_submit",
		Description: "Submit this verify run's review of its pull request, pinned to the commit you verified. " +
			"Refused if the pull request has moved since the run started: the verdict would not cover the new commits.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ReviewIn) (*mcp.CallToolResult, ReviewOut, error) {
		out, err := s.opReview(ctx, principal(req), in)
		return nil, out, err
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
		var body []byte
		if in.Body != nil {
			b, err := json.Marshal(in.Body)
			if err != nil {
				return nil, apiOut{}, err
			}
			body = b
		}
		path, query, _ := strings.Cut(in.Path, "?")
		hr := httptest.NewRequest(strings.ToUpper(in.Method), "/forgejo/api/v1"+path, bytes.NewReader(body))
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
