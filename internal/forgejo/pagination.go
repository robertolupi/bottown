package forgejo

import sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

// allPages reads complete lists. On failure it returns no partial result and
// preserves the failing response for status checks. An exact full page requires
// another request, including when the endpoint omits pagination headers.
func allPages[T any](fetch func(sdk.ListOptions) ([]T, *sdk.Response, error)) ([]T, *sdk.Response, error) {
	var all []T
	for page := 1; ; page++ {
		items, resp, err := fetch(sdk.ListOptions{Page: page, PageSize: 50})
		if err != nil {
			return nil, resp, err
		}
		all = append(all, items...)
		if len(items) < 50 {
			return all, resp, nil
		}
	}
}

// AllIssues returns every page, retaining the supplied filters.
func (c *Client) AllIssues(org, repo string, opts sdk.ListIssueOption) ([]*sdk.Issue, *sdk.Response, error) {
	return allPages(func(page sdk.ListOptions) ([]*sdk.Issue, *sdk.Response, error) {
		opts.ListOptions = page
		return c.ListRepoIssues(org, repo, opts)
	})
}

// AllPullRequests returns every page, retaining the supplied filters.
func (c *Client) AllPullRequests(org, repo string, opts sdk.ListPullRequestsOptions) ([]*sdk.PullRequest, *sdk.Response, error) {
	return allPages(func(page sdk.ListOptions) ([]*sdk.PullRequest, *sdk.Response, error) {
		opts.ListOptions = page
		return c.ListRepoPullRequests(org, repo, opts)
	})
}

// AllComments returns every page, retaining the supplied filters.
func (c *Client) AllComments(org, repo string, index int64, opts sdk.ListIssueCommentOptions) ([]*sdk.Comment, *sdk.Response, error) {
	return allPages(func(page sdk.ListOptions) ([]*sdk.Comment, *sdk.Response, error) {
		opts.ListOptions = page
		return c.ListIssueComments(org, repo, index, opts)
	})
}

// AllReviews returns every page, retaining the supplied filters.
func (c *Client) AllReviews(org, repo string, index int64, opts sdk.ListPullReviewsOptions) ([]*sdk.PullReview, *sdk.Response, error) {
	return allPages(func(page sdk.ListOptions) ([]*sdk.PullReview, *sdk.Response, error) {
		opts.ListOptions = page
		return c.ListPullReviews(org, repo, index, opts)
	})
}
