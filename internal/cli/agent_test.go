package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/runs"
)

func TestRepoFromGitURL(t *testing.T) {
	for _, tc := range []struct {
		url, org, repo string
		ok             bool
	}{
		{"http://127.0.0.1:3031/git/bottown/bottown.git", "bottown", "bottown", true},
		{"http://127.0.0.1:3041/git/bottown/demo", "bottown", "demo", true},
		{"http://127.0.0.1:3041/git/bottown/demo.git/", "bottown", "demo", true},
		{"https://github.com/robertolupi/bottown.git", "", "", false},
		{"http://127.0.0.1:3031/git/bottown", "", "", false},
		{"http://127.0.0.1:3031/git/bottown/a/b.git", "", "", false},
	} {
		org, repo, ok := repoFromGitURL(tc.url)
		if org != tc.org || repo != tc.repo || ok != tc.ok {
			t.Errorf("repoFromGitURL(%q) = %q, %q, %v; want %q, %q, %v", tc.url, org, repo, ok, tc.org, tc.repo, tc.ok)
		}
	}
}

func TestHeadInfoString(t *testing.T) {
	for _, tc := range []struct {
		h    headInfo
		want string
	}{
		{headInfo{SHA: "c260e1c", Base: "bottown/main", Fetched: true}, "HEAD c260e1c (detached), at bottown/main"},
		{headInfo{SHA: "c260e1c", Base: "bottown/main", Behind: 2, Fetched: true}, "HEAD c260e1c (detached), 2 commits behind bottown/main"},
		{headInfo{SHA: "c260e1c", Branch: "issue/9", Base: "bottown/main", Ahead: 1, Fetched: true}, "HEAD c260e1c on issue/9, 1 commit ahead of bottown/main"},
		{headInfo{SHA: "c260e1c", Base: "bottown/main", Ahead: 3, Behind: 1, Fetched: true}, "HEAD c260e1c (detached), 3 commits ahead of and 1 behind bottown/main"},
		{headInfo{SHA: "c260e1c", Base: "bottown/main", Behind: 1}, "HEAD c260e1c (detached), 1 commit behind bottown/main (fetch failed; as of the last fetch)"},
		{headInfo{SHA: "c260e1c", Branch: "x"}, "HEAD c260e1c on x, default branch unknown"},
	} {
		if got := tc.h.String(); got != tc.want {
			t.Errorf("%+v:\n got %q\nwant %q", tc.h, got, tc.want)
		}
	}
}

func TestLastRunLine(t *testing.T) {
	ended := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	got := lastRunLine(runs.Record{ID: "r-1", Kind: "verify", Repo: "bottown/bottown", PR: 19, Status: "succeeded", Ended: &ended})
	if !strings.HasPrefix(got, "last run r-1: verify PR !19 in bottown/bottown, succeeded, ended ") {
		t.Errorf("verify: %q", got)
	}
	got = lastRunLine(runs.Record{ID: "r-2", Kind: "implement", Repo: "bottown/demo", Issue: 9, Status: "running"})
	if got != "last run r-2: implement issue #9 in bottown/demo, running" {
		t.Errorf("implement: %q", got)
	}
}
