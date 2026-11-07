package server

import (
	"fmt"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/milestone"
)

// MilestoneBranchPrefix starts a milestone's integration branch,
// milestone/<plan slug>, which its children's pull requests merge into.
const MilestoneBranchPrefix = "milestone/"

// runBase decides the base of a run on issue is: the branch its work
// starts from and its pull request merges into. In order:
//
//  1. the base of the issue's open pull request from issue/<n>;
//  2. milestone/<slug>, when the issue is a milestone child (its body ends
//     in a child marker), filed by the owner (milestone.Trusted), and that
//     branch exists on the forge;
//  3. the repository's default branch.
//
// An untrusted marker, an epic's marker, or a milestone branch nobody
// created yet never changes the base.
func (s *Server) runBase(api *forgejo.Client, org, repo string, is *sdk.Issue, defaultBranch string) (string, error) {
	head := fmt.Sprintf("issue/%d", is.Index)
	prs, _, err := api.AllPullRequests(org, repo, sdk.ListPullRequestsOptions{State: sdk.StateOpen})
	if err != nil {
		return "", fmt.Errorf("list open pull requests: %w", err)
	}
	for _, pr := range prs {
		if pr.Head != nil && pr.Base != nil && pr.Head.Ref == head && pr.Base.Ref != "" {
			return pr.Base.Ref, nil
		}
	}
	m, _, ok := milestone.ParseMarker(is.Body)
	if !ok || m.Epic || m.Key == "" || is.Poster == nil || !milestone.Trusted(is.Poster.UserName, s.Owner) {
		return defaultBranch, nil
	}
	branch := MilestoneBranchPrefix + m.Plan
	if _, err := api.Branch(org, repo, branch); forgejo.IsRawNotFound(err) {
		return defaultBranch, nil
	} else if err != nil {
		return "", fmt.Errorf("branch %s: %w", branch, err)
	}
	return branch, nil
}
