// Package forgejo is bottown's own thin client for the Forgejo REST API and
// the `forgejo admin` CLI. It covers only the endpoints bottown itself calls;
// bots reach the full API through the server's proxy instead. Types here are
// wire types and stay inside the adapter.
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls the Forgejo API with one token.
type Client struct {
	BaseURL string // e.g. http://127.0.0.1:3000
	Token   string
	HTTP    *http.Client
}

// New returns a client for baseURL authenticating with token.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is a non-2xx response.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("forgejo %s %s: %d %s", e.Method, e.Path, e.Status, strings.TrimSpace(e.Body))
}

// IsStatus reports whether err is an APIError with the given status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// IsNotFound reports whether err is a 404.
func IsNotFound(err error) bool { return IsStatus(err, http.StatusNotFound) }

// Do sends a request to /api/v1 + path. in is JSON-encoded if non-nil; out is
// decoded from the response if non-nil.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: string(b)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Version returns the server version; it needs no token.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	err := c.Do(ctx, http.MethodGet, "/version", nil, &v)
	return v.Version, err
}

// User is a Forgejo user.
type User struct {
	ID            int64  `json:"id"`
	Login         string `json:"login"`
	Email         string `json:"email"`
	FullName      string `json:"full_name"`
	IsAdmin       bool   `json:"is_admin"`
	ProhibitLogin bool   `json:"prohibit_login"`
}

// Self returns the user owning the client's token.
func (c *Client) Self(ctx context.Context) (User, error) {
	var u User
	err := c.Do(ctx, http.MethodGet, "/user", nil, &u)
	return u, err
}

// GetUser looks a user up by login.
func (c *Client) GetUser(ctx context.Context, login string) (User, error) {
	var u User
	err := c.Do(ctx, http.MethodGet, "/users/"+url.PathEscape(login), nil, &u)
	return u, err
}

// CreateUserOption is the body of POST /admin/users.
type CreateUserOption struct {
	Username           string `json:"username"`
	Email              string `json:"email"`
	FullName           string `json:"full_name,omitempty"`
	Password           string `json:"password"`
	MustChangePassword *bool  `json:"must_change_password,omitempty"`
	Visibility         string `json:"visibility,omitempty"`
}

// AdminCreateUser creates a user (needs a site-admin token).
func (c *Client) AdminCreateUser(ctx context.Context, opt CreateUserOption) (User, error) {
	var u User
	err := c.Do(ctx, http.MethodPost, "/admin/users", opt, &u)
	return u, err
}

// EditUserOption is the body of PATCH /admin/users/{name}.
type EditUserOption struct {
	LoginName     string  `json:"login_name"`
	SourceID      int64   `json:"source_id"`
	Email         *string `json:"email,omitempty"`
	FullName      *string `json:"full_name,omitempty"`
	Admin         *bool   `json:"admin,omitempty"`
	ProhibitLogin *bool   `json:"prohibit_login,omitempty"`
}

// AdminEditUser edits a user (needs a site-admin token).
func (c *Client) AdminEditUser(ctx context.Context, login string, opt EditUserOption) error {
	return c.Do(ctx, http.MethodPatch, "/admin/users/"+url.PathEscape(login), opt, nil)
}

// Org is a Forgejo organization.
type Org struct {
	ID       int64  `json:"id"`
	UserName string `json:"username"`
}

// GetOrg looks an organization up by name.
func (c *Client) GetOrg(ctx context.Context, name string) (Org, error) {
	var o Org
	err := c.Do(ctx, http.MethodGet, "/orgs/"+url.PathEscape(name), nil, &o)
	return o, err
}

// CreateOrg creates a private organization owned by the token's user.
func (c *Client) CreateOrg(ctx context.Context, name string) (Org, error) {
	var o Org
	err := c.Do(ctx, http.MethodPost, "/orgs", map[string]any{
		"username":   name,
		"visibility": "private",
	}, &o)
	return o, err
}

// Team is an organization team.
type Team struct {
	ID                      int64             `json:"id"`
	Name                    string            `json:"name"`
	Permission              string            `json:"permission"`
	IncludesAllRepositories bool              `json:"includes_all_repositories"`
	UnitsMap                map[string]string `json:"units_map"`
}

// ListOrgTeams lists all teams of org.
func (c *Client) ListOrgTeams(ctx context.Context, org string) ([]Team, error) {
	var ts []Team
	err := c.Do(ctx, http.MethodGet, "/orgs/"+url.PathEscape(org)+"/teams?limit=50", nil, &ts)
	return ts, err
}

// TeamOption is the body for creating or editing a team.
type TeamOption struct {
	Name                    string            `json:"name"`
	Description             string            `json:"description,omitempty"`
	Permission              string            `json:"permission"`
	IncludesAllRepositories bool              `json:"includes_all_repositories"`
	CanCreateOrgRepo        bool              `json:"can_create_org_repo"`
	Units                   []string          `json:"units"`
	UnitsMap                map[string]string `json:"units_map"`
}

// CreateTeam creates a team in org.
func (c *Client) CreateTeam(ctx context.Context, org string, opt TeamOption) (Team, error) {
	var t Team
	err := c.Do(ctx, http.MethodPost, "/orgs/"+url.PathEscape(org)+"/teams", opt, &t)
	return t, err
}

// EditTeam edits a team.
func (c *Client) EditTeam(ctx context.Context, id int64, opt TeamOption) error {
	return c.Do(ctx, http.MethodPatch, fmt.Sprintf("/teams/%d", id), opt, nil)
}

// ListTeamMembers lists the members of a team.
func (c *Client) ListTeamMembers(ctx context.Context, id int64) ([]User, error) {
	var us []User
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/teams/%d/members?limit=50", id), nil, &us)
	return us, err
}

// AddTeamMember adds login to a team.
func (c *Client) AddTeamMember(ctx context.Context, id int64, login string) error {
	return c.Do(ctx, http.MethodPut, fmt.Sprintf("/teams/%d/members/%s", id, url.PathEscape(login)), nil, nil)
}

// Label is an issue label.
type Label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	Exclusive   bool   `json:"exclusive"`
}

// ListOrgLabels lists the organization-wide labels.
func (c *Client) ListOrgLabels(ctx context.Context, org string) ([]Label, error) {
	var ls []Label
	err := c.Do(ctx, http.MethodGet, "/orgs/"+url.PathEscape(org)+"/labels?limit=50", nil, &ls)
	return ls, err
}

// CreateOrgLabel creates an organization-wide label.
func (c *Client) CreateOrgLabel(ctx context.Context, org string, l Label) error {
	return c.Do(ctx, http.MethodPost, "/orgs/"+url.PathEscape(org)+"/labels", labelBody(l), nil)
}

// EditOrgLabel edits an organization-wide label.
func (c *Client) EditOrgLabel(ctx context.Context, org string, id int64, l Label) error {
	return c.Do(ctx, http.MethodPatch, fmt.Sprintf("/orgs/%s/labels/%d", url.PathEscape(org), id), labelBody(l), nil)
}

func labelBody(l Label) map[string]any {
	return map[string]any{
		"name":        l.Name,
		"color":       l.Color,
		"description": l.Description,
		"exclusive":   l.Exclusive,
	}
}

// Repo is a repository.
type Repo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Empty         bool   `json:"empty"`
	Private       bool   `json:"private"`
}

// GetRepo returns owner/name.
func (c *Client) GetRepo(ctx context.Context, owner, name string) (Repo, error) {
	var r Repo
	err := c.Do(ctx, http.MethodGet, repoPath(owner, name), nil, &r)
	return r, err
}

// ListOrgRepos lists the repositories of org.
func (c *Client) ListOrgRepos(ctx context.Context, org string) ([]Repo, error) {
	var all []Repo
	for page := 1; ; page++ {
		var rs []Repo
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s/repos?limit=50&page=%d", url.PathEscape(org), page), nil, &rs); err != nil {
			return nil, err
		}
		all = append(all, rs...)
		if len(rs) < 50 {
			return all, nil
		}
	}
}

// CreateOrgRepo creates an empty private repository in org.
func (c *Client) CreateOrgRepo(ctx context.Context, org, name, defaultBranch string) (Repo, error) {
	var r Repo
	err := c.Do(ctx, http.MethodPost, "/orgs/"+url.PathEscape(org)+"/repos", map[string]any{
		"name":           name,
		"private":        true,
		"auto_init":      false,
		"default_branch": defaultBranch,
	}, &r)
	return r, err
}

// SetDefaultBranch sets a repository's default branch.
func (c *Client) SetDefaultBranch(ctx context.Context, owner, name, branch string) error {
	return c.Do(ctx, http.MethodPatch, repoPath(owner, name), map[string]any{"default_branch": branch}, nil)
}

// BranchProtection is the subset of branch protection settings bottown
// manages. Field names follow the Forgejo API.
type BranchProtection struct {
	RuleName                 string   `json:"rule_name,omitempty"`
	BranchName               string   `json:"branch_name,omitempty"`
	EnablePush               bool     `json:"enable_push"`
	EnablePushWhitelist      bool     `json:"enable_push_whitelist"`
	PushWhitelistUsernames   []string `json:"push_whitelist_usernames"`
	EnableForcePush          bool     `json:"enable_force_push"`
	EnableMergeWhitelist     bool     `json:"enable_merge_whitelist"`
	MergeWhitelistUsernames  []string `json:"merge_whitelist_usernames"`
	EnableApprovalsWhitelist bool     `json:"enable_approvals_whitelist"`
	ApprovalsWhitelistUsers  []string `json:"approvals_whitelist_username"`
	RequiredApprovals        int64    `json:"required_approvals"`
	DismissStaleApprovals    bool     `json:"dismiss_stale_approvals"`
	BlockOnRejectedReviews   bool     `json:"block_on_rejected_reviews"`
	BlockOnOfficialReviewReq bool     `json:"block_on_official_review_requests"`
	BlockOnOutdatedBranch    bool     `json:"block_on_outdated_branch"`
	ApplyToAdmins            bool     `json:"apply_to_admins"`
	EnableStatusCheck        bool     `json:"enable_status_check"`
	StatusCheckContexts      []string `json:"status_check_contexts"`
}

// GetBranchProtection returns the protection rule named rule.
func (c *Client) GetBranchProtection(ctx context.Context, owner, name, rule string) (BranchProtection, error) {
	var p BranchProtection
	err := c.Do(ctx, http.MethodGet, repoPath(owner, name)+"/branch_protections/"+url.PathEscape(rule), nil, &p)
	return p, err
}

// CreateBranchProtection creates a protection rule.
func (c *Client) CreateBranchProtection(ctx context.Context, owner, name string, p BranchProtection) error {
	return c.Do(ctx, http.MethodPost, repoPath(owner, name)+"/branch_protections", p, nil)
}

// EditBranchProtection updates the protection rule named rule.
func (c *Client) EditBranchProtection(ctx context.Context, owner, name, rule string, p BranchProtection) error {
	p.RuleName, p.BranchName = "", ""
	return c.Do(ctx, http.MethodPatch, repoPath(owner, name)+"/branch_protections/"+url.PathEscape(rule), p, nil)
}

func repoPath(owner, name string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}
