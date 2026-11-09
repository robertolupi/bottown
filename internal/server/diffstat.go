package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/diffstat"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/secrets"
)

// DiffStatOut is a pull request's change by file, as its diff counts it
// (internal/diffstat), without the diff itself.
type DiffStatOut struct {
	Files []DiffStatFile `json:"files"`
}

// DiffStatFile is one file's added and deleted lines; a binary file has
// none.
type DiffStatFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary,omitempty"`
}

// opPRDiffStat counts PR n's change by file. It reads the counts from the
// repository's git (git diff --numstat, from the merge base to the head, the
// range Forgejo's diff shows), and downloads and parses the whole diff from
// Forgejo only when the repositories, or the PR's commits in them, are not
// available.
func (s *Server) opPRDiffStat(ctx context.Context, p secrets.Principal, in Target) (DiffStatOut, error) {
	org, repo, n, err := s.resolve(p, in, 0)
	if err != nil {
		return DiffStatOut{}, err
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return DiffStatOut{}, err
	}
	pr, resp, err := c.GetPullRequest(org, repo, n)
	if forgejo.IsNotFound(resp, err) {
		return DiffStatOut{}, ferr(http.StatusNotFound, "pull request #%d not found in %s", n, repo)
	} else if err != nil {
		return DiffStatOut{}, err
	}
	if files, err := s.gitDiffStat(ctx, org, repo, pr); err != nil {
		s.Log.Debug("pr diffstat from git", "repo", repo, "pr", n, "err", err)
	} else if files != nil {
		return DiffStatOut{Files: files}, nil
	}
	diff, _, err := c.GetPullRequestDiff(org, repo, n, sdk.PullRequestDiffOptions{})
	if err != nil {
		return DiffStatOut{}, err
	}
	out := DiffStatOut{Files: []DiffStatFile{}}
	for _, f := range diffstat.Parse(string(diff)) {
		out.Files = append(out.Files, DiffStatFile{Path: f.Path, Added: f.Added, Deleted: f.Deleted, Binary: f.Binary})
	}
	return out, nil
}

// gitDiffStat is pr's change by file from its git repository, or nil when
// the repositories are not available. An open PR's merge base is read from
// git, since Forgejo's can lag its head (pushsync.go).
func (s *Server) gitDiffStat(ctx context.Context, org, repo string, pr *sdk.PullRequest) ([]DiffStatFile, error) {
	if s.GitRoot == "" || pr.Head == nil || pr.Head.Sha == "" {
		return nil, nil
	}
	head, mergeBase := pr.Head.Sha, pr.MergeBase
	if pr.State == sdk.StateOpen && !pr.HasMerged && pr.Base != nil {
		g, err := s.openPRGit(ctx, org, repo, pr.Index, pr.Base.Ref, head)
		if err != nil || g == nil {
			return nil, err
		}
		mergeBase = g.mergeBase
	}
	if mergeBase == "" {
		return nil, nil
	}
	out, err := mergeGit(ctx, s.repoGitDir(org, repo), nil, nil, "diff", "--numstat", "-z", "-M", "--no-color", "--no-ext-diff", mergeBase, head, "--")
	if err != nil {
		return nil, err
	}
	return parseNumstat(out)
}

// parseNumstat reads git diff --numstat -z: per file "added\tdeleted\tpath\0",
// or for a rename "added\tdeleted\t\0old\0new\0"; a binary file's counts
// are "-".
func parseNumstat(out string) ([]DiffStatFile, error) {
	files := []DiffStatFile{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("numstat: unexpected record %q", rec)
		}
		f := DiffStatFile{Path: parts[2]}
		if f.Path == "" {
			// A rename: the old path, then the new.
			if i+2 >= len(fields) || fields[i+2] == "" {
				return nil, fmt.Errorf("numstat: truncated rename %q", rec)
			}
			f.Path = fields[i+2]
			i += 2
		}
		if parts[0] == "-" && parts[1] == "-" {
			f.Binary = true
		} else {
			var err error
			if f.Added, err = strconv.Atoi(parts[0]); err != nil {
				return nil, fmt.Errorf("numstat: %q: %w", rec, err)
			}
			if f.Deleted, err = strconv.Atoi(parts[1]); err != nil {
				return nil, fmt.Errorf("numstat: %q: %w", rec, err)
			}
		}
		files = append(files, f)
	}
	return files, nil
}
