package forgejo

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
	var ls []OrgLabel
	err := c.do(http.MethodGet, "/orgs/"+url.PathEscape(org)+"/labels?limit=50", nil, &ls)
	return ls, err
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

// IssueAttachments lists the files attached to issue (or pull request)
// index: its description's, then each comment's in order. Forgejo returns
// an issue's comments in one page.
func (c *Client) IssueAttachments(owner, repo string, index int64) ([]Attachment, error) {
	base := fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(owner), url.PathEscape(repo))
	var out []Attachment
	if err := c.do(http.MethodGet, fmt.Sprintf("%s/%d/assets", base, index), nil, &out); err != nil {
		return nil, err
	}
	var comments []struct {
		ID     int64        `json:"id"`
		Assets []Attachment `json:"assets"`
	}
	if err := c.do(http.MethodGet, fmt.Sprintf("%s/%d/comments", base, index), nil, &comments); err != nil {
		return nil, err
	}
	for _, cm := range comments {
		for _, a := range cm.Assets {
			a.Comment = cm.ID
			out = append(out, a)
		}
	}
	return out, nil
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

// BranchHead returns the commit branch points at. The SDK's GetRepoBranch
// escapes the slash in names like issue/4; Forgejo's route takes the name as
// path segments, so each segment is escaped on its own.
func (c *Client) BranchHead(owner, repo, branch string) (string, error) {
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := c.do("GET", fmt.Sprintf("/repos/%s/%s/branches/%s", url.PathEscape(owner), url.PathEscape(repo), strings.Join(segs, "/")), nil, &b); err != nil {
		return "", err
	}
	return b.Commit.ID, nil
}
