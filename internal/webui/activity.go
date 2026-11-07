package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// ComputeActivity is the repository's activity in w (internal/activity):
// the one derivation the timeline and the statistics views both serve.
func (s *Server) ComputeActivity(ctx context.Context, w activity.Window) (*activity.Activity, error) {
	return activity.Load(ctx, activitySource{s}, w, time.Now())
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
