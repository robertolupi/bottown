package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/diffstat"
	serverapi "github.com/robertolupi/bottown/internal/server"
	"github.com/robertolupi/bottown/internal/stats"
)

// ComputeStats is the repository's statistics for w (internal/stats): the
// per-issue/PR vector and the breakdowns, built on the shared activity
// derivation (ComputeActivity) so the timeline and the statistics cannot
// disagree. It adds the issues' size labels and milestones, and the merged
// PRs' diffstats for the measured size: the issue list is the one the
// activity read, and the diffstats not yet cached are read
// activity.Concurrency at a time.
func (s *Server) ComputeStats(ctx context.Context, w activity.Window) (*stats.Stats, error) {
	a, issues, err := s.computeActivity(ctx, w)
	if err != nil {
		return nil, err
	}
	f := stats.Facts{Issues: issues, DiffStats: map[int64][]diffstat.File{}}
	var (
		mu         sync.Mutex
		incomplete []string
		reads      []func()
	)
	for _, th := range a.Threads {
		if th.PR <= 0 || th.PRMerged == nil {
			continue
		}
		reads = append(reads, func() {
			files, err := s.diffs.get(ctx, th.PR, s.pullRequestDiffStat)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				incomplete = append(incomplete, fmt.Sprintf("diffstat of !%d: %v", th.PR, err))
				return
			}
			f.DiffStats[th.PR] = files
		})
	}
	activity.Parallel(activity.Concurrency, reads)
	slices.Sort(incomplete)
	st := stats.Compute(a, f, time.Now())
	st.Incomplete = append(st.Incomplete, incomplete...)
	return st, nil
}

// diffCache remembers merged PRs' diffstats between requests. A merged PR
// does not change, so its diffstat is read once; the tab's refresh on every status or
// history event then costs no forge reads. Like activity.IssueCache it is
// safe for concurrent use and to drop: it only saves reads.
type diffCache struct {
	mu    sync.Mutex
	diffs map[int64][]diffstat.File
}

// get is n's diffstat from the cache, reading it once with read on a miss.
func (c *diffCache) get(ctx context.Context, n int64, read func(context.Context, int64) ([]diffstat.File, error)) ([]diffstat.File, error) {
	c.mu.Lock()
	if d, ok := c.diffs[n]; ok {
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()
	d, err := read(ctx, n)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.diffs == nil {
		c.diffs = map[int64][]diffstat.File{}
	}
	c.diffs[n] = d
	c.mu.Unlock()
	return d, nil
}

// pullRequestDiffStat reads PR n's change by file
// (GET /v1/forge/pr/diffstat), its lock files marked Collapsed as
// diffstat.Parse marks them.
func (s *Server) pullRequestDiffStat(ctx context.Context, n int64) ([]diffstat.File, error) {
	_, repo := s.orgAndRepo()
	q := url.Values{"repo": {repo}, "number": {fmt.Sprint(n)}}
	var out serverapi.DiffStatOut
	if err := s.callServer(ctx, http.MethodGet, "/v1/forge/pr/diffstat?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	files := make([]diffstat.File, 0, len(out.Files))
	for _, f := range out.Files {
		files = append(files, diffstat.File{Path: f.Path, Added: f.Added, Deleted: f.Deleted, Binary: f.Binary, Collapsed: diffstat.IsLockFile(f.Path)})
	}
	return files, nil
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

// statsWindow is the window a statistics request asks for: thirty days to
// now, unless the request gives since or until.
func statsWindow(r *http.Request, now time.Time) (activity.Window, error) {
	return window(r, now, 30*24*time.Hour)
}
