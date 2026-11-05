// Package client is the one client for host-side calls to a bottown
// server: the CLI, the flow supervisor and the web UI's backend all reach
// the server through it. Every call takes a context and has a timeout, and
// a refusal is an *APIError that callers branch on with errors.Is against
// the sentinels below rather than by matching message text.
package client

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

	"github.com/robertolupi/bottown/internal/server"
)

// DefaultTimeout bounds a call, body included, unless WithTimeout says
// otherwise: a server that accepts the connection but never answers fails
// the call instead of hanging it.
const DefaultTimeout = 2 * time.Minute

// Sentinels for the refusals callers branch on. An *APIError matches them
// with errors.Is: by HTTP status, or by the error code the server sent.
var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("not found")
	// ErrActorBusy is a run refused because its actor, serialised, has
	// another live batch run.
	ErrActorBusy = errors.New(server.ActorBusyMessage)
	// ErrHeadMoved is a verify run refused because the pull request's head
	// is not the commit it was asked to pin.
	ErrHeadMoved = errors.New(server.HeadMovedMessage)
)

// APIError is a non-2xx answer from the server.
type APIError struct {
	Status  int    // HTTP status code
	Method  string // request method
	Path    string // request path, query included
	Message string // the server's error message, or the body; may be empty
	Code    string // the server's machine-readable error code, if any
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status))
	}
	return e.Method + " " + e.Path + ": " + msg
}

// Is matches the sentinels: errors.Is(err, client.ErrNotFound).
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Status == http.StatusForbidden
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrActorBusy:
		return e.Code == server.ErrorCodeActorBusy
	case ErrHeadMoved:
		return e.Code == server.ErrorCodeHeadMoved
	}
	return false
}

// Client calls one bottown server with one bearer key.
type Client struct {
	base string
	key  string
	http *http.Client
}

// New is a client for the server at base (e.g. http://localhost:3031) that
// authenticates with key; an empty key sends no Authorization header.
func New(base, key string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), key: key, http: &http.Client{Timeout: DefaultTimeout}}
}

// WithTimeout is a copy of c whose calls time out after d.
func (c *Client) WithTimeout(d time.Duration) *Client {
	cp := *c
	cp.http = &http.Client{Timeout: d}
	return &cp
}

// Call sends in, if not nil, as JSON to path (starting with /, query
// included) and decodes a 2xx answer into out: nil discards it, a *[]byte
// takes the raw body, anything else is decoded as JSON. A non-2xx answer
// is an *APIError.
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(b), "application/json"
	}
	resp, err := c.Do(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: read the answer: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(method, path, resp.StatusCode, raw)
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = raw
		return nil
	default:
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: decode the answer: %w", method, path, err)
		}
		return nil
	}
}

// Do sends body to path and returns the response whatever its status; the
// caller closes its body. It is for answers that are not JSON or that the
// caller reads on a non-2xx status too. Error makes an *APIError of one.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bottown server unreachable (is it up?): %w", err)
	}
	return resp, nil
}

// Error reads a non-2xx response's body (closing it) into an *APIError.
func Error(resp *http.Response) error {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return newAPIError(resp.Request.Method, resp.Request.URL.RequestURI(), resp.StatusCode, raw)
}

func newAPIError(method, path string, status int, raw []byte) *APIError {
	e := &APIError{Status: status, Method: method, Path: path}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"` // Forgejo's
		Code    string `json:"code"`
	}
	if json.Unmarshal(raw, &body) == nil && (body.Error != "" || body.Message != "") {
		e.Message, e.Code = body.Error+body.Message, body.Code
	} else {
		e.Message = strings.TrimSpace(string(raw))
	}
	return e
}
