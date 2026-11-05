package flow

import (
	"context"
	"fmt"
	"net/url"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// Client reads forge and run state through the owner-authenticated server API.
type Client struct {
	c *client.Client
}

func NewClient(inst config.Instance, key string) *Client {
	return &Client{c: client.New(inst.ServerURL(), key)}
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	return c.c.Call(ctx, method, path, in, out)
}

func (c *Client) Actors(ctx context.Context) ([]server.ActorInfo, error) {
	var actors []server.ActorInfo
	err := c.call(ctx, "GET", "/v1/actors", nil, &actors)
	return actors, err
}

func (c *Client) OpenPR(ctx context.Context, repo, branch string) (server.PRItem, bool, error) {
	q := url.Values{"repo": {repo}, "state": {"open"}}
	var prs []server.PRItem
	if err := c.call(ctx, "GET", "/v1/forge/prs?"+q.Encode(), nil, &prs); err != nil {
		return server.PRItem{}, false, err
	}
	var found server.PRItem
	for _, pr := range prs {
		if pr.HeadBranch != branch {
			continue
		}
		if found.Number != 0 {
			return server.PRItem{}, false, fmt.Errorf("more than one open PR from %s", branch)
		}
		found = pr
	}
	return found, found.Number != 0, nil
}

func (c *Client) PR(ctx context.Context, repo string, number int64) (server.PROut, error) {
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(number)}}
	var pr server.PROut
	err := c.call(ctx, "GET", "/v1/forge/pr?"+q.Encode(), nil, &pr)
	return pr, err
}

func (c *Client) ReconcileCarries(ctx context.Context, repo string, number int64) error {
	in := server.Target{Repo: repo, Number: number}
	return c.call(ctx, "POST", "/v1/forge/pr/carries", in, nil)
}

func (c *Client) PRMergeStatus(ctx context.Context, repo string, number int64) (server.PRMergeStatus, error) {
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(number)}}
	var status server.PRMergeStatus
	err := c.call(ctx, "GET", "/v1/forge/pr/merge-base?"+q.Encode(), nil, &status)
	return status, err
}

func (c *Client) MergeBase(ctx context.Context, repo string, number int64, head string) (server.PRMergeOut, error) {
	in := server.PRMergeIn{Target: server.Target{Repo: repo, Number: number}, HeadSHA: head}
	var out server.PRMergeOut
	err := c.call(ctx, "POST", "/v1/forge/pr/merge-base", in, &out)
	return out, err
}

func (c *Client) LiveRuns(ctx context.Context, repo string) ([]secrets.Run, error) {
	q := url.Values{"repo": {repo}, "state": {"live"}}
	var runs []secrets.Run
	err := c.call(ctx, "GET", "/v1/runs?"+q.Encode(), nil, &runs)
	return runs, err
}

func (c *Client) RecordGate(ctx context.Context, repo, sha, state, description string) error {
	in := server.CommitStatusIn{Repo: repo, SHA: sha, Check: "e2e", State: state, Description: description}
	return c.call(ctx, "POST", "/v1/forge/status", in, nil)
}

func (c *Client) GateFailures(ctx context.Context, repo string, pr int64) ([]string, error) {
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(pr)}}
	var out server.GateOut
	err := c.call(ctx, "GET", "/v1/forge/pr/gate?"+q.Encode(), nil, &out)
	return out.Failed, err
}

func (c *Client) Comment(ctx context.Context, repo string, number int64, body string) error {
	in := server.CommentIn{Target: server.Target{Repo: repo, Number: number}, Body: body}
	return c.call(ctx, "POST", "/v1/forge/comment", in, nil)
}
