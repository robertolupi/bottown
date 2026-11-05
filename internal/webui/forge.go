package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/robertolupi/bottown/internal/secrets"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// The web UI's reads of forge and run state. Each is one call to the
// server's API, as the owner: the server lists, pages and assesses, and the
// UI shows what it returns (#249). Only what the host alone has (run records
// and logs, worktrees, the checkout) is read here directly.

// pullRequests lists the repository's PRs in state (open, closed or all).
// The server assesses each open PR's reviews; with checks, it adds the
// commit statuses at each open PR's head.
func (s *Server) pullRequests(ctx context.Context, state string, checks bool) ([]*serverapi.PRItem, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "state": {state}}
	if checks {
		q.Set("checks", "1")
	}
	var prs []*serverapi.PRItem
	if err := s.callServer(ctx, http.MethodGet, "/v1/forge/prs?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

// pullRequest reads PR n with its reviews, assessment and checks.
func (s *Server) pullRequest(ctx context.Context, n int64) (*serverapi.PROut, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(n)}}
	var pr serverapi.PROut
	if err := s.callServer(ctx, http.MethodGet, "/v1/forge/pr?"+q.Encode(), nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// issues lists the repository's issues (not PRs) in state; withBody adds
// their descriptions.
func (s *Server) issues(ctx context.Context, state string, withBody bool) ([]*serverapi.IssueItem, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "state": {state}}
	if withBody {
		q.Set("body", "1")
	}
	var issues []*serverapi.IssueItem
	if err := s.callServer(ctx, http.MethodGet, "/v1/forge/issues?"+q.Encode(), nil, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// issue reads issue n; with comments, its discussion too.
func (s *Server) issue(ctx context.Context, n int64, comments bool) (*serverapi.IssueOut, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(n)}}
	if !comments {
		q.Set("comments", "0")
	}
	var is serverapi.IssueOut
	if err := s.callServer(ctx, http.MethodGet, "/v1/forge/issue?"+q.Encode(), nil, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// issueDependencies lists the issues that block issue n.
func (s *Server) issueDependencies(ctx context.Context, n int64) ([]*serverapi.IssueItem, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(n)}}
	var deps []*serverapi.IssueItem
	if err := s.callServer(ctx, http.MethodGet, "/v1/forge/issue/dependencies?"+q.Encode(), nil, &deps); err != nil {
		return nil, err
	}
	return deps, nil
}

// milestones lists the repository's milestones, open and closed.
func (s *Server) milestones(ctx context.Context) ([]serverapi.MilestoneItem, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "state": {"all"}}
	var ms []serverapi.MilestoneItem
	if err := s.callServer(ctx, http.MethodGet, "/v1/forge/milestones?"+q.Encode(), nil, &ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// branch reads branch name: its head commit and whether it is protected.
func (s *Server) branch(ctx context.Context, name string) (serverapi.BranchOut, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "name": {name}}
	var b serverapi.BranchOut
	err := s.callServer(ctx, http.MethodGet, "/v1/forge/branch?"+q.Encode(), nil, &b)
	return b, err
}

// serverRuns lists the repository's runs, each with the state the server
// decided for it; state filters them (live: not ended), history adds purged
// runs.
func (s *Server) serverRuns(ctx context.Context, state string, history bool) ([]serverapi.RunItem, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}}
	if state != "" {
		q.Set("state", state)
	}
	if history {
		q.Set("history", "1")
	}
	var runs []serverapi.RunItem
	if err := s.callServer(ctx, http.MethodGet, "/v1/runs?"+q.Encode(), nil, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// isActive reports whether the server counts run as active: not ended, and
// its key still usable.
func isActive(run serverapi.RunItem) bool { return run.State == secrets.RunActive }
