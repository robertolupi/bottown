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
	"github.com/robertolupi/bottown/internal/stats"
)

// ComputeStats is the repository's statistics for w (internal/stats): the
// per-issue/PR vector and the breakdowns, built on the shared activity
// derivation (ComputeActivity) so the timeline and the statistics cannot
// disagree. It adds the issues' size labels and milestones, and the merged
// PRs' diffs for the measured size.
func (s *Server) ComputeStats(ctx context.Context, w activity.Window) (*stats.Stats, error) {
	a, err := s.ComputeActivity(ctx, w)
	if err != nil {
		return nil, err
	}
	issues, err := s.issues(ctx, "all", false)
	if err != nil {
		return nil, fmt.Errorf("read issues: %w", err)
	}
	f := stats.Facts{Issues: issues, Diffs: map[int64]string{}}
	var incomplete []string
	for _, th := range a.Threads {
		if th.PR <= 0 || th.PRMerged == nil {
			continue
		}
		diff, err := s.pullRequestDiff(ctx, th.PR)
		if err != nil {
			incomplete = append(incomplete, fmt.Sprintf("diff of !%d: %v", th.PR, err))
			continue
		}
		f.Diffs[th.PR] = diff
	}
	st := stats.Compute(a, f, time.Now())
	st.Incomplete = append(st.Incomplete, incomplete...)
	return st, nil
}

// pullRequestDiff reads PR n's unified diff (GET /v1/forge/pr?diff=1).
func (s *Server) pullRequestDiff(ctx context.Context, n int64) (string, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(n)}, "diff": {"1"}}
	var pr serverapi.PROut
	if err := s.callServer(ctx, http.MethodGet, "/v1/forge/pr?"+q.Encode(), nil, &pr); err != nil {
		return "", err
	}
	return pr.Diff, nil
}

// handleGetStats serves GET /api/stats?since=&until= (RFC 3339; since
// defaults to thirty days before until, and until to now).
func (s *Server) handleGetStats(w http.ResponseWriter, r *http.Request) {
	win, err := statsWindow(r, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	st, err := s.ComputeStats(r.Context(), win)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// statsWindow is the window a statistics request asks for.
func statsWindow(r *http.Request, now time.Time) (activity.Window, error) {
	q := r.URL.Query()
	w := activity.Window{Until: now.UTC()}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return w, fmt.Errorf("until: %w", err)
		}
		w.Until = t
	}
	w.Since = w.Until.Add(-30 * 24 * time.Hour)
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
