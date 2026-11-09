package forgejo

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

// Extensions: Forgejo API surface that forgejo-sdk v3 does not model. Keep
// this file small; drop an entry when the SDK grows it.

// OrgLabel is an organization-wide label. The SDK has repository labels
// only, and its Label type has no "exclusive" (scoped label) flag.
type OrgLabel struct {
	ID          int64  `json:"id,omitempty"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	Exclusive   bool   `json:"exclusive"`
}

// ListOrgLabels lists org's labels.
func (c *Client) ListOrgLabels(org string) ([]OrgLabel, error) {
	var all []OrgLabel
	for page := 1; ; page++ {
		var ls []OrgLabel
		if err := c.do(http.MethodGet, fmt.Sprintf("/orgs/%s/labels?limit=50&page=%d", url.PathEscape(org), page), nil, &ls); err != nil {
			return nil, err
		}
		all = append(all, ls...)
		if len(ls) < 50 {
			return all, nil
		}
	}
}

// CreateOrgLabel creates an organization-wide label.
func (c *Client) CreateOrgLabel(org string, l OrgLabel) error {
	l.ID = 0
	return c.do(http.MethodPost, "/orgs/"+url.PathEscape(org)+"/labels", l, nil)
}

// EditOrgLabel edits an organization-wide label.
func (c *Client) EditOrgLabel(org string, id int64, l OrgLabel) error {
	l.ID = 0
	return c.do(http.MethodPatch, fmt.Sprintf("/orgs/%s/labels/%d", url.PathEscape(org), id), l, nil)
}

// ApplyToAdmins reads Forgejo's "admins cannot bypass this rule" flag of a
// branch protection rule. The SDK's BranchProtection lacks it (Gitea calls
// the similar field block_admin_merge_override).
func (c *Client) ApplyToAdmins(owner, repo, rule string) (bool, error) {
	var v struct {
		ApplyToAdmins bool `json:"apply_to_admins"`
	}
	err := c.do(http.MethodGet, protectionPath(owner, repo, rule), nil, &v)
	return v.ApplyToAdmins, err
}

// SetApplyToAdmins sets that flag.
func (c *Client) SetApplyToAdmins(owner, repo, rule string, v bool) error {
	return c.do(http.MethodPatch, protectionPath(owner, repo, rule), map[string]bool{"apply_to_admins": v}, nil)
}

// Attachment is a file attached to an issue, a pull request or one of their
// comments. The SDK's Issue and Comment types lack their "assets".
type Attachment struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	UUID    string    `json:"uuid"`
	Created time.Time `json:"created_at"`
	// Type is "attachment" for an uploaded file, "external" for a link.
	Type    string `json:"type"`
	Comment int64  `json:"-"` // the comment it is on; 0 for the description
}

// Comment is an issue (or pull request) comment with its attachments,
// which the SDK's Comment lacks.
type Comment struct {
	ID      int64        `json:"id"`
	Body    string       `json:"body"`
	Poster  *sdk.User    `json:"user"`
	Created time.Time    `json:"created_at"`
	Assets  []Attachment `json:"assets"`
}

// IssueComments lists all the comments on issue (or pull request) index,
// oldest first, in one request: Forgejo ignores page and limit there and
// returns the complete list.
func (c *Client) IssueComments(owner, repo string, index int64) ([]Comment, error) {
	var out []Comment
	err := c.do(http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(repo), index), nil, &out)
	return out, err
}

// RepoComment is a comment as the repository-wide list gives it: with its
// updated time and the issue or pull request it is on.
type RepoComment struct {
	Comment
	Updated time.Time `json:"updated_at"`
	// IssueURL is the issue's web URL, empty for a pull request's comment,
	// whose PullRequestURL is set instead.
	IssueURL       string `json:"issue_url"`
	PullRequestURL string `json:"pull_request_url"`
}

// Issue is the number of the issue c is on, or 0 for a pull request's
// comment or a URL that does not name an issue.
func (c RepoComment) Issue() int64 {
	if c.PullRequestURL != "" {
		return 0
	}
	u, err := url.Parse(c.IssueURL)
	if err != nil {
		return 0
	}
	rest, num, ok := strings.Cut(strings.TrimSuffix(u.Path, "/"), "/issues/")
	if !ok || rest == "" || strings.Contains(num, "/") {
		return 0
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// repoCommentsPage is the page size RepoComments asks for: Forgejo's
// largest by default.
const repoCommentsPage = 50

// RepoComments lists the comments on the repository's issues and pull
// requests that were created or edited at or after since (all of them for
// a zero since), oldest first. Forgejo ignores limit there unless page is
// given too, and caps a page at its own maximum, which may be smaller, so
// it reads page by page until a page is empty or brings no comment it has
// already seen.
func (c *Client) RepoComments(owner, repo string, since time.Time) ([]RepoComment, error) {
	var out []RepoComment
	seen := map[int64]bool{}
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(repoCommentsPage)}}
		if !since.IsZero() {
			q.Set("since", since.UTC().Format(time.RFC3339))
		}
		var items []RepoComment
		if err := c.do(http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/comments?%s", url.PathEscape(owner), url.PathEscape(repo), q.Encode()), nil, &items); err != nil {
			return nil, err
		}
		fresh := 0
		for _, cm := range items {
			if !seen[cm.ID] {
				seen[cm.ID] = true
				out = append(out, cm)
				fresh++
			}
		}
		if fresh == 0 {
			return out, nil
		}
	}
}

// DescriptionAttachments lists the files attached to the description of
// issue (or pull request) index.
func (c *Client) DescriptionAttachments(owner, repo string, index int64) ([]Attachment, error) {
	var out []Attachment
	err := c.do(http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/%d/assets", url.PathEscape(owner), url.PathEscape(repo), index), nil, &out)
	return out, err
}

// CommentAttachments are the files attached to comments, in their order,
// each with the comment it is on.
func CommentAttachments(comments []Comment) []Attachment {
	var out []Attachment
	for _, cm := range comments {
		for _, a := range cm.Assets {
			a.Comment = cm.ID
			out = append(out, a)
		}
	}
	return out
}

// IssueAttachments lists the files attached to issue (or pull request)
// index: its description's, then each comment's in order. A caller that
// also needs the comments lists them with IssueComments and collects
// their files with CommentAttachments, rather than reading them twice.
func (c *Client) IssueAttachments(owner, repo string, index int64) ([]Attachment, error) {
	out, err := c.DescriptionAttachments(owner, repo, index)
	if err != nil {
		return nil, err
	}
	comments, err := c.IssueComments(owner, repo, index)
	if err != nil {
		return nil, err
	}
	return append(out, CommentAttachments(comments)...), nil
}

// DownloadAttachment opens an attachment's contents. Forgejo serves them on
// its web route /attachments/<uuid>, not under /api/v1, but accepts the
// API token there; the caller closes the body.
func (c *Client) DownloadAttachment(uuid string) (*http.Response, error) {
	path := "/attachments/" + url.PathEscape(uuid)
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	// No client timeout: a large file may take longer than an API call;
	// the caller's context bounds it. Redirects are not followed: Forgejo
	// answers a request it did not authenticate with one to its login page.
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &APIError{Method: http.MethodGet, Path: path, Status: resp.StatusCode, Body: string(b)}
	}
	return resp, nil
}

func protectionPath(owner, repo, rule string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/branch_protections/" + url.PathEscape(rule)
}

// AutoDismissMessage is the timeline text Forgejo writes when it dismisses
// an approval itself, for new commits under "dismiss stale approvals".
const AutoDismissMessage = "New commits pushed, approval review dismissed automatically according to repository settings"

// AutoDismissedReviews lists the reviews on pull request index that Forgejo
// dismissed by itself, from the PR's timeline; a dismissal by a person
// carries their own message.
func (c *Client) AutoDismissedReviews(owner, repo string, index int64) (map[int64]bool, error) {
	const limit = 50
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/timeline", url.PathEscape(owner), url.PathEscape(repo), index)
	out := map[int64]bool{}
	seen := map[int64]bool{}
	for page := 1; page <= 100; page++ {
		var events []struct {
			ID       int64  `json:"id"`
			Type     string `json:"type"`
			Body     string `json:"body"`
			ReviewID int64  `json:"review_id"`
		}
		if err := c.do(http.MethodGet, fmt.Sprintf("%s?page=%d&limit=%d", path, page, limit), nil, &events); err != nil {
			return nil, err
		}
		fresh := 0
		for _, e := range events {
			if seen[e.ID] {
				continue
			}
			seen[e.ID], fresh = true, fresh+1
			if e.Type == "dismiss_review" && e.ReviewID != 0 {
				out[e.ReviewID] = strings.TrimSpace(e.Body) == AutoDismissMessage
			}
		}
		if len(events) < limit || fresh == 0 {
			break
		}
	}
	for id, auto := range out {
		if !auto {
			delete(out, id)
		}
	}
	return out, nil
}

// BranchHead returns the commit branch points at.
func (c *Client) BranchHead(owner, repo, branch string) (string, error) {
	b, err := c.Branch(owner, repo, branch)
	return b.Commit, err
}

// OpenPRDrafts maps each of the repository's open pull requests to
// Forgejo's draft flag. The SDK's PullRequest lacks the field.
func (c *Client) OpenPRDrafts(owner, repo string) (map[int64]bool, error) {
	out := map[int64]bool{}
	for page := 1; ; page++ {
		var prs []struct {
			Number int64 `json:"number"`
			Draft  bool  `json:"draft"`
		}
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&limit=50&page=%d", url.PathEscape(owner), url.PathEscape(repo), page)
		if err := c.do(http.MethodGet, path, nil, &prs); err != nil {
			return nil, err
		}
		for _, pr := range prs {
			out[pr.Number] = pr.Draft
		}
		if len(prs) < 50 {
			return out, nil
		}
	}
}

// BranchInfo is a branch's head commit and whether a protection rule
// covers it.
type BranchInfo struct {
	Commit    string
	Protected bool
}

// Branch reads a branch. The SDK's GetRepoBranch escapes the slash in names
// like issue/4; Forgejo's route takes the name as path segments, so each
// segment is escaped on its own.
func (c *Client) Branch(owner, repo, branch string) (BranchInfo, error) {
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	if err := c.do("GET", fmt.Sprintf("/repos/%s/%s/branches/%s", url.PathEscape(owner), url.PathEscape(repo), strings.Join(segs, "/")), nil, &b); err != nil {
		return BranchInfo{}, err
	}
	return BranchInfo{Commit: b.Commit.ID, Protected: b.Protected}, nil
}

// AllIssueDependencies returns every issue that blocks issue index. The
// SDK does not model issue dependencies.
func (c *Client) AllIssueDependencies(owner, repo string, index int64) ([]*sdk.Issue, error) {
	var all []*sdk.Issue
	for page := 1; ; page++ {
		var items []*sdk.Issue
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/dependencies?page=%d&limit=50", url.PathEscape(owner), url.PathEscape(repo), index, page)
		if err := c.do("GET", path, nil, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < 50 {
			return all, nil
		}
	}
}

// CreateWikiPage creates a wiki page through the API, which is how a
// repository's wiki comes to exist: a git push to a wiki Forgejo has not
// created is refused.
func (c *Client) CreateWikiPage(owner, repo, title, content, message string) error {
	in := map[string]string{
		"title":          title,
		"content_base64": base64.StdEncoding.EncodeToString([]byte(content)),
		"message":        message,
	}
	return c.do("POST", fmt.Sprintf("/repos/%s/%s/wiki/new", url.PathEscape(owner), url.PathEscape(repo)), in, nil)
}
