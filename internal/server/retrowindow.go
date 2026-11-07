package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
)

// A windowed retro (#383) looks back on everything that landed on the
// default branch in a window, not on one issue's flow: the commits after
// the window's base, first-parent, up to the default branch's head when
// the run is created. The server resolves the window in Forgejo's own
// repository, so the launcher and the record agree on it, and names the
// one wiki page the run may write after the head it covers. The next
// window starts there (--since last).

// retroPageRe is a windowed retro's page in the wiki: the day the run
// started and its head's short SHA.
var retroPageRe = regexp.MustCompile(`^([0-9]{8})-([0-9a-f]{4,40})-retrospective\.md$`)

// RetroPage is the wiki page name of a windowed retro started at t that
// covers up to short, its head's abbreviated SHA.
func RetroPage(t time.Time, short string) string {
	return t.UTC().Format("20060102") + "-" + short + "-retrospective"
}

// retroSince is a window's start, parsed: a git ref (Ref), the last
// windowed retro's head (Last), or the default branch as it was at Before.
type retroSince struct {
	Ref    string
	Last   bool
	Before time.Time
}

var ageRe = regexp.MustCompile(`^([1-9][0-9]{0,3})([dw])$`)

// parseRetroSince parses --since: git:<ref>, last, a date (YYYY-MM-DD,
// from its start in UTC) or an age (Nd or Nw before now).
func parseRetroSince(since string, now time.Time) (retroSince, error) {
	switch {
	case since == "last":
		return retroSince{Last: true}, nil
	case strings.HasPrefix(since, "git:"):
		ref := strings.TrimPrefix(since, "git:")
		if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\n\x00") {
			return retroSince{}, fmt.Errorf("--since %q: git: needs a ref, such as a SHA or a tag", since)
		}
		return retroSince{Ref: ref}, nil
	}
	if m := ageRe.FindStringSubmatch(since); m != nil {
		n, _ := strconv.Atoi(m[1])
		days := n
		if m[2] == "w" {
			days = 7 * n
		}
		return retroSince{Before: now.Add(-time.Duration(days) * 24 * time.Hour)}, nil
	}
	if t, err := time.Parse("2006-01-02", since); err == nil {
		return retroSince{Before: t}, nil
	}
	return retroSince{}, fmt.Errorf("--since %q: want git:<ref>, last, a date (YYYY-MM-DD) or an age (such as 3d or 2w)", since)
}

// retroWindow resolves since against the default branch at head, in
// org/repo's repository, for a windowed retro created at now.
func (s *Server) retroWindow(ctx context.Context, org, repo, branch, head, since string, now time.Time) (*secrets.RetroWindow, error) {
	sp, err := parseRetroSince(since, now)
	if err != nil {
		return nil, runErr(http.StatusBadRequest, "%v", err)
	}
	if s.GitRoot == "" {
		return nil, runErr(http.StatusNotImplemented, "forge git repositories are not available, so a retro window cannot be resolved")
	}
	gitDir := s.repoGitDir(org, repo)
	if _, err := os.Stat(gitDir); err != nil {
		return nil, fmt.Errorf("repository %s/%s: %w", org, repo, err)
	}
	commit := func(rev string) (string, bool) {
		out, err := mergeGit(ctx, gitDir, nil, nil, "rev-parse", "--verify", "-q", rev+"^{commit}")
		return strings.TrimSpace(out), err == nil
	}
	if _, ok := commit(head); !ok {
		return nil, fmt.Errorf("%s's head %s is not in the repository", branch, head)
	}
	// onBranch is how many first-parent commits of the default branch
	// lie after c, when c is one of them: a commit an issue branch merged
	// in is an ancestor of the head, but not on the branch.
	onBranch := func(c string) (int, bool) {
		if _, err := mergeGit(ctx, gitDir, nil, nil, "merge-base", "--is-ancestor", c, head); err != nil {
			return 0, false
		}
		out, err := mergeGit(ctx, gitDir, nil, nil, "rev-list", "--first-parent", "--count", c+".."+head)
		if err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			return 0, false
		}
		at, ok := commit(fmt.Sprintf("%s~%d", head, n))
		return n, ok && at == c
	}
	var base string
	switch {
	case sp.Ref != "":
		c, ok := commit(sp.Ref)
		if !ok {
			return nil, runErr(http.StatusNotFound, "--since %s: no such commit in %s/%s", since, org, repo)
		}
		if _, ok := onBranch(c); !ok {
			return nil, runErr(http.StatusConflict, "--since %s: %s is not on %s (first-parent)", since, shortCommit(c), branch)
		}
		base = c
	case sp.Last:
		st, err := s.readWiki(ctx, org, repo)
		if err != nil {
			return nil, err
		}
		// The newest page's head: the one furthest along the default
		// branch, the fewest commits before its head, of those on it.
		best := -1
		for _, f := range st.files() {
			m := retroPageRe.FindStringSubmatch(f)
			if m == nil {
				continue
			}
			c, ok := commit(m[2])
			if !ok {
				continue
			}
			if n, ok := onBranch(c); ok && (best < 0 || n < best) {
				best, base = n, c
			}
		}
		if base == "" {
			return nil, runErr(http.StatusNotFound, "--since last: the wiki has no windowed retro page (YYYYMMDD-<sha>-retrospective) whose head is on %s; give --since git:<ref> or a date", branch)
		}
	default:
		out, err := mergeGit(ctx, gitDir, nil, nil, "rev-list", "--first-parent", "-1",
			"--before="+strconv.FormatInt(sp.Before.Unix(), 10), head)
		if err != nil {
			return nil, err
		}
		if base = strings.TrimSpace(out); base == "" {
			return nil, runErr(http.StatusNotFound, "--since %s: %s has no commit from before %s; give --since git:<ref>", since, branch, sp.Before.UTC().Format(time.RFC3339))
		}
	}
	if base == head {
		return nil, runErr(http.StatusConflict, "--since %s: nothing landed on %s after %s", since, branch, shortCommit(base))
	}
	out, err := mergeGit(ctx, gitDir, nil, nil, "rev-parse", "--short", head)
	if err != nil {
		return nil, err
	}
	return &secrets.RetroWindow{Since: since, Base: base, Page: RetroPage(now, strings.TrimSpace(out))}, nil
}
