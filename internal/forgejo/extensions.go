package forgejo

import (
	"fmt"
	"net/http"
	"net/url"
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

func protectionPath(owner, repo, rule string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/branch_protections/" + url.PathEscape(rule)
}
