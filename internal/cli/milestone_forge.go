package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/milestone"
)

// proxyForge is milestone.Forge over the server's Forgejo API proxy, with
// the owner's key: the server holds the token, and the owner is exempt from
// the base policy that keeps actors from editing issues and dependencies.
type proxyForge struct {
	base      string // the server's /forgejo/api/v1
	key       string
	org, repo string
	http      *http.Client
}

func newProxyForge(serverURL, key, org, repo string) *proxyForge {
	return &proxyForge{base: serverURL + "/forgejo/api/v1", key: key, org: org, repo: repo,
		http: &http.Client{Timeout: time.Minute}}
}

// call sends a request to path (under /api/v1); out may be nil, a *[]byte
// for the raw body, or a value to decode JSON into. A 404 is
// milestone.ErrNotFound.
func (f *proxyForge) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+f.key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return fmt.Errorf("bottown server unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, milestone.ErrNotFound)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = raw
		return nil
	default:
		return json.Unmarshal(raw, out)
	}
}

func (f *proxyForge) repoPath(rest string) string {
	return "/repos/" + f.org + "/" + f.repo + rest
}

// pathEscape escapes each segment of a repository path.
func pathEscape(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// ownerLogin is the login of the account the key acts as.
func (f *proxyForge) ownerLogin(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := f.call(ctx, "GET", "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func (f *proxyForge) DefaultBranch(ctx context.Context) (string, string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := f.call(ctx, "GET", f.repoPath(""), nil, &r); err != nil {
		return "", "", err
	}
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := f.call(ctx, "GET", f.repoPath("/branches/"+pathEscape(r.DefaultBranch)), nil, &b); err != nil {
		return "", "", err
	}
	if b.Commit.ID == "" {
		return "", "", fmt.Errorf("branch %s has no commit", r.DefaultBranch)
	}
	return r.DefaultBranch, b.Commit.ID, nil
}

func (f *proxyForge) LastCommit(ctx context.Context, ref, path string) (string, error) {
	q := url.Values{"sha": {ref}, "path": {path}, "limit": {"1"},
		"stat": {"false"}, "verification": {"false"}, "files": {"false"}}
	var cs []struct {
		SHA string `json:"sha"`
	}
	if err := f.call(ctx, "GET", f.repoPath("/commits?"+q.Encode()), nil, &cs); err != nil {
		return "", err
	}
	if len(cs) == 0 {
		return "", nil
	}
	return cs[0].SHA, nil
}

func (f *proxyForge) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	var b []byte
	err := f.call(ctx, "GET", f.repoPath("/raw/"+pathEscape(path)+"?ref="+url.QueryEscape(ref)), nil, &b)
	return b, err
}

// apiIssue is the part of Forgejo's issue JSON apply reads.
type apiIssue struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	PullRequest *struct{} `json:"pull_request"`
	Milestone   *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		State string `json:"state"`
	} `json:"milestone"`
}

func (is apiIssue) forge() *milestone.ForgeIssue {
	out := &milestone.ForgeIssue{Number: is.Number, Title: is.Title, Body: is.Body,
		Closed: is.State == "closed", Poster: is.User.Login, PR: is.PullRequest != nil}
	if m := is.Milestone; m != nil {
		out.Milestone = &milestone.ForgeMilestone{ID: m.ID, Title: m.Title, Closed: m.State == "closed"}
	}
	return out
}

func (f *proxyForge) Issue(ctx context.Context, n int64) (*milestone.ForgeIssue, error) {
	var is apiIssue
	if err := f.call(ctx, "GET", f.repoPath(fmt.Sprintf("/issues/%d", n)), nil, &is); err != nil {
		return nil, err
	}
	return is.forge(), nil
}

// pages fetches a paginated list until a page comes back empty.
func pages[T any](ctx context.Context, f *proxyForge, path string, q url.Values) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		q.Set("page", fmt.Sprint(page))
		q.Set("limit", "50")
		var batch []T
		if err := f.call(ctx, "GET", path+"?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return all, nil
		}
		all = append(all, batch...)
	}
}

func (f *proxyForge) Issues(ctx context.Context) ([]*milestone.ForgeIssue, error) {
	list, err := pages[apiIssue](ctx, f, f.repoPath("/issues"), url.Values{"state": {"all"}, "type": {"issues"}})
	if err != nil {
		return nil, err
	}
	out := make([]*milestone.ForgeIssue, 0, len(list))
	for _, is := range list {
		if is.PullRequest == nil {
			out = append(out, is.forge())
		}
	}
	return out, nil
}

func (f *proxyForge) CreateIssue(ctx context.Context, title, body string, ms int64) (int64, error) {
	in := map[string]any{"title": title, "body": body, "milestone": ms}
	var is apiIssue
	if err := f.call(ctx, "POST", f.repoPath("/issues"), in, &is); err != nil {
		return 0, err
	}
	return is.Number, nil
}

func (f *proxyForge) EditIssue(ctx context.Context, n int64, title, body string) error {
	in := map[string]any{"title": title, "body": body}
	return f.call(ctx, "PATCH", f.repoPath(fmt.Sprintf("/issues/%d", n)), in, nil)
}

func (f *proxyForge) BlockedBy(ctx context.Context, n int64) ([]int64, error) {
	list, err := pages[apiIssue](ctx, f, f.repoPath(fmt.Sprintf("/issues/%d/dependencies", n)), url.Values{})
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(list))
	for i, is := range list {
		out[i] = is.Number
	}
	return out, nil
}

// issueMeta names the dependency in a dependencies POST or DELETE.
func (f *proxyForge) issueMeta(dep int64) map[string]any {
	return map[string]any{"owner": f.org, "repo": f.repo, "index": dep}
}

func (f *proxyForge) AddBlockedBy(ctx context.Context, n, dep int64) error {
	return f.call(ctx, "POST", f.repoPath(fmt.Sprintf("/issues/%d/dependencies", n)), f.issueMeta(dep), nil)
}

func (f *proxyForge) RemoveBlockedBy(ctx context.Context, n, dep int64) error {
	return f.call(ctx, "DELETE", f.repoPath(fmt.Sprintf("/issues/%d/dependencies", n)), f.issueMeta(dep), nil)
}
