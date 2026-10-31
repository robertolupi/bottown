// Package forgejo is bottown's adapter to Forgejo. REST calls go through the
// Forgejo Go SDK (codeberg.org/mvdkleijn/forgejo-sdk); extensions.go covers
// the few endpoints and fields the SDK lacks; adminclient.go wraps the
// in-container `forgejo admin` CLI; weblogin.go creates browser sessions.
// SDK types stay behind this package and internal/provision.
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

// Client is an SDK client bound to one token and one context, plus raw
// access for the extensions. The SDK keeps its context on the client, so
// create one Client per operation rather than sharing it across goroutines.
type Client struct {
	*sdk.Client
	ctx     context.Context
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client for baseURL (e.g. http://127.0.0.1:3000) using token.
func New(ctx context.Context, baseURL, token string) *Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	opts := []sdk.ClientOption{
		sdk.SetHTTPClient(hc),
		sdk.SetContext(ctx),
		// Forgejo reports e.g. "15.0.11+gitea-1.22.0", which the SDK's
		// Gitea-era version gates do not understand; skip them.
		sdk.SetForgejoVersion(""),
	}
	if token != "" {
		opts = append(opts, sdk.SetToken(token))
	}
	c, err := sdk.NewClient(strings.TrimRight(baseURL, "/"), opts...)
	if err != nil {
		// Only option errors are possible with version checks disabled.
		panic(fmt.Sprintf("forgejo sdk client: %v", err))
	}
	return &Client{Client: c, ctx: ctx, baseURL: strings.TrimRight(baseURL, "/"), token: token, http: hc}
}

// IsNotFound reports whether an SDK call failed with 404.
func IsNotFound(resp *sdk.Response, err error) bool { return StatusIs(resp, err, http.StatusNotFound) }

// StatusIs reports whether an SDK call failed with the given status.
func StatusIs(resp *sdk.Response, err error, status int) bool {
	return err != nil && resp != nil && resp.Response != nil && resp.StatusCode == status
}

// APIError is a non-2xx response to a raw (extension) call.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("forgejo %s %s: %d %s", e.Method, e.Path, e.Status, strings.TrimSpace(e.Body))
}

// IsRawNotFound reports whether a raw call failed with 404.
func IsRawNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// do sends a raw request to /api/v1 + path, for what the SDK does not cover.
func (c *Client) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(c.ctx, method, c.baseURL+"/api/v1"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.http.Do(req)
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

// Healthy reports whether Forgejo's unauthenticated /api/healthz passes.
// (/api/v1/version is no use for readiness: REQUIRE_SIGNIN_VIEW makes it 403
// for anonymous callers.)
func Healthy(ctx context.Context, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}
