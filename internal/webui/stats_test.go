package webui

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/config"
)

// ComputeStats lists the issues once, through the activity, and a warm load
// reads only the open PR: the merged PR's reviews, gate and diff are cached.
func TestComputeStatsReads(t *testing.T) {
	fix := defaultFixture()
	fix.requests = map[string]int{}
	_, port := newFixtureServer(t, fix)
	day := time.Now().Add(-24 * time.Hour)
	mergedAt := day.Add(time.Hour)
	fix.prs = []*sdk.PullRequest{
		{Index: 12, Title: "Merged", State: sdk.StateClosed, HasMerged: true, Merged: &mergedAt, Closed: &mergedAt, Updated: &mergedAt,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/2", Sha: "aaaa"}, Created: &day},
		{Index: 13, Title: "Open", State: sdk.StateOpen, Updated: &day,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/3", Sha: "bbbb"}, Created: &day},
	}
	fix.issues = []*sdk.Issue{
		{Index: 2, Title: "two", State: sdk.StateClosed, Created: day, Updated: mergedAt, Closed: &mergedAt},
		{Index: 3, Title: "three", State: sdk.StateOpen, Created: day, Updated: day},
	}
	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  t.TempDir(), FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	win := activity.Window{Since: time.Now().Add(-7 * 24 * time.Hour)}
	load := func() map[string]int {
		t.Helper()
		fix.mu.Lock()
		clear(fix.requests)
		fix.mu.Unlock()
		st, err := s.ComputeStats(context.Background(), win)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Incomplete) != 0 {
			t.Errorf("incomplete = %q", st.Incomplete)
		}
		fix.mu.Lock()
		defer fix.mu.Unlock()
		return maps.Clone(fix.requests)
	}
	// Cold: PR and gate reads for both PRs, and the merged PR's diff.
	if got := load(); got["/v1/forge/issues"] != 1 || got["/v1/forge/pr"] != 3 || got["/v1/forge/pr/gate"] != 2 {
		t.Errorf("cold load requests = %v", got)
	}
	if got := load(); got["/v1/forge/issues"] != 1 || got["/v1/forge/pr"] != 1 || got["/v1/forge/pr/gate"] != 1 {
		t.Errorf("warm load requests = %v, want only the open PR read", got)
	}
}

func TestDiffCacheReadsOnce(t *testing.T) {
	var c diffCache
	calls := 0
	read := func(context.Context, int64) (string, error) {
		calls++
		return "diff", nil
	}
	for i := 0; i < 3; i++ {
		d, err := c.get(context.Background(), 7, read)
		if err != nil || d != "diff" {
			t.Fatalf("get: %q, %v", d, err)
		}
	}
	if calls != 1 {
		t.Errorf("read the diff %d times, want once: a merged PR does not change", calls)
	}
}

func TestDiffCacheDoesNotCacheErrors(t *testing.T) {
	var c diffCache
	calls := 0
	read := func(context.Context, int64) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("boom")
		}
		return "diff", nil
	}
	if _, err := c.get(context.Background(), 3, read); err == nil {
		t.Fatal("first get: want the read's error")
	}
	d, err := c.get(context.Background(), 3, read)
	if err != nil || d != "diff" {
		t.Fatalf("second get: %q, %v", d, err)
	}
	if calls != 2 {
		t.Errorf("read the diff %d times, want twice: a failed read is not cached", calls)
	}
}
