package webui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// ComputeActivity is the repository's activity in w (internal/activity):
// the one derivation the timeline and the statistics views both serve.
// Issues' comments are kept in memory between calls, by their updated
// time, so a load reads only the issues that changed.
func (s *Server) ComputeActivity(ctx context.Context, w activity.Window) (*activity.Activity, error) {
	return activity.Load(ctx, activitySource{s}, &s.activityIssues, w, time.Now())
}

// window is the window a request asks for: since and until as RFC 3339,
// until defaulting to now and since to def before it, so the timeline and
// the statistics views read the same control.
func window(r *http.Request, now time.Time, def time.Duration) (activity.Window, error) {
	q := r.URL.Query()
	w := activity.Window{Until: now.UTC()}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return w, fmt.Errorf("until: %w", err)
		}
		w.Until = t
	}
	w.Since = w.Until.Add(-def)
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return w, fmt.Errorf("since: %w", err)
		}
		w.Since = t
	}
	if !w.Since.Before(w.Until) {
		return w, errors.New("since must be before until")
	}
	return w, nil
}

// activitySource reads activity's facts through the server, as the UI's
// other reads do (forge.go).
type activitySource struct{ s *Server }

func (a activitySource) Runs(ctx context.Context) ([]serverapi.RunItem, error) {
	return a.s.serverRuns(ctx, "", true)
}

func (a activitySource) PullRequests(ctx context.Context) ([]*serverapi.PRItem, error) {
	return a.s.pullRequests(ctx, "all", false)
}

func (a activitySource) Issues(ctx context.Context) ([]*serverapi.IssueItem, error) {
	return a.s.issues(ctx, "all", false)
}

func (a activitySource) PullRequest(ctx context.Context, n int64) (*serverapi.PROut, error) {
	return a.s.pullRequest(ctx, n)
}

func (a activitySource) Issue(ctx context.Context, n int64) (*serverapi.IssueOut, error) {
	return a.s.issue(ctx, n, true)
}

func (a activitySource) Gate(ctx context.Context, n int64) (serverapi.GateOut, error) {
	_, repo := a.s.orgAndRepo()
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(n)}}
	var out serverapi.GateOut
	err := a.s.callServer(ctx, http.MethodGet, "/v1/forge/pr/gate?"+q.Encode(), nil, &out)
	return out, err
}
