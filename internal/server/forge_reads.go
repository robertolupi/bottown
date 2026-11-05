package server

import (
	"context"
	"net/http"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/secrets"
)

// Reads the web UI makes besides issues and PRs, so that it shows what the
// server reads rather than reading Forgejo itself (#249).

// opIssueDependencies lists the issues that block issue in.Number, with
// their descriptions (the web UI reads milestone markers from them).
func (s *Server) opIssueDependencies(ctx context.Context, p secrets.Principal, in Target) ([]IssueItem, error) {
	org, repo, n, err := s.resolve(p, in, 0)
	if err != nil {
		return nil, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return nil, err
	}
	deps, err := c.AllIssueDependencies(org, repo, n)
	if forgejo.IsRawNotFound(err) {
		return nil, ferr(http.StatusNotFound, "issue #%d not found in %s", n, repo)
	} else if err != nil {
		return nil, err
	}
	out := make([]IssueItem, 0, len(deps))
	for _, d := range deps {
		if d != nil {
			out = append(out, NewIssueItem(d, true))
		}
	}
	return out, nil
}

// MilestoneListIn is the input of the milestone list.
type MilestoneListIn struct {
	Repo  string
	State string // open, closed or all (default: open)
}

func (s *Server) opMilestoneList(ctx context.Context, p secrets.Principal, in MilestoneListIn) ([]MilestoneItem, error) {
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
	ms, resp, err := c.AllMilestones(org, repo, sdk.ListMilestoneOption{State: state})
	if forgejo.IsNotFound(resp, err) {
		return nil, ferr(http.StatusNotFound, "repository %s/%s not found", org, repo)
	} else if err != nil {
		return nil, err
	}
	out := make([]MilestoneItem, 0, len(ms))
	for _, m := range ms {
		if item := milestoneItem(m); item != nil {
			out = append(out, *item)
		}
	}
	return out, nil
}

// BranchOut is a branch's head commit, and whether a protection rule
// covers it.
type BranchOut struct {
	Name      string `json:"name"`
	Commit    string `json:"commit"`
	Protected bool   `json:"protected"`
}

func (s *Server) opBranch(ctx context.Context, p secrets.Principal, repoName, name string) (BranchOut, error) {
	org, repo, err := s.resolveRepo(p, repoName)
	if err != nil {
		return BranchOut{}, err
	}
	if name == "" {
		return BranchOut{}, ferr(http.StatusBadRequest, "name is required")
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return BranchOut{}, err
	}
	b, err := c.Branch(org, repo, name)
	if forgejo.IsRawNotFound(err) {
		return BranchOut{}, ferr(http.StatusNotFound, "branch %s not found in %s", name, repo)
	} else if err != nil {
		return BranchOut{}, err
	}
	return BranchOut{Name: name, Commit: b.Commit, Protected: b.Protected}, nil
}

// repoFile reads the file at path at ref, a commit the caller pinned.
func (s *Server) repoFile(ctx context.Context, p secrets.Principal, repoName, ref, path string) ([]byte, error) {
	org, repo, err := s.resolveRepo(p, repoName)
	if err != nil {
		return nil, err
	}
	if ref == "" || path == "" {
		return nil, ferr(http.StatusBadRequest, "ref and path are required")
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return nil, err
	}
	b, resp, err := c.GetFile(org, repo, ref, path)
	if forgejo.IsNotFound(resp, err) {
		return nil, ferr(http.StatusNotFound, "%s not found at %s in %s", path, ref, repo)
	} else if err != nil {
		return nil, err
	}
	return b, nil
}
