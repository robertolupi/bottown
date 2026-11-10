package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
)

// RepoSettingsCheck is the repository's .bottown.toml as read from the
// forge's default branch (§4.4). Without the file the repository gets the
// defaults, and Found is false.
type RepoSettingsCheck struct {
	Checked   bool     `json:"checked"`
	Found     bool     `json:"found"`
	File      string   `json:"file"`
	Branch    string   `json:"branch"`
	Commit    string   `json:"commit,omitempty"`
	AfterPull []string `json:"after_pull"`
	// MilestonesDir is the effective milestones_dir: the file's, or the
	// default without the file. It is empty when the file is invalid.
	MilestonesDir string `json:"milestones_dir,omitempty"`
	Error         string `json:"error,omitempty"`
}

// LoadRepoSettings reads .bottown.toml from the forge's default branch,
// through the server's API proxy as the owner. It pins the branch's head
// commit first and reads the file at that commit, so neither a PR branch
// nor the checkout's working tree can supply it.
func (s *Server) LoadRepoSettings(ctx context.Context) RepoSettingsCheck {
	base := "main"
	if b, err := runs.RemoteDefaultBranch(ctx, s.cfg.RepoTop, s.cfg.Instance.Remote()); err == nil && b != "" {
		base = b
	}
	res := RepoSettingsCheck{File: config.RepoSettingsFile, Branch: base, AfterPull: []string{}}
	org, repo, ok := strings.Cut(s.cfg.FullRepo, "/")
	if !ok || org == "" || repo == "" {
		res.Error = "unknown repository " + s.cfg.FullRepo
		return res
	}

	branch, err := s.branch(ctx, base)
	if err != nil {
		res.Error = fmt.Sprintf("branch %s: %v", base, err)
		return res
	}
	if branch.Commit == "" {
		res.Error = fmt.Sprintf("branch %s: no head commit", base)
		return res
	}
	res.Commit = branch.Commit

	q := url.Values{"repo": {repo}, "ref": {res.Commit}, "path": {config.RepoSettingsFile}}
	code, body, err := s.getServerRaw(ctx, "/v1/forge/file?"+q.Encode())
	switch {
	case err != nil:
		res.Error = err.Error()
		return res
	case code == http.StatusNotFound:
		res.Checked = true
		res.MilestonesDir = config.DefaultRepoSettings().MilestonesDir
		return res
	case code < 200 || code > 299:
		res.Error = fmt.Sprintf("server error %d: %s", code, strings.TrimSpace(string(body)))
		return res
	}
	res.Checked, res.Found = true, true
	st, err := config.ParseRepoSettings(body)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if st.AfterPull != nil {
		res.AfterPull = st.AfterPull
	}
	res.MilestonesDir = st.MilestonesDir
	return res
}

// getServerRaw is callServer for a response that is not JSON: it returns
// the status and body rather than failing on a non-2xx status.
func (s *Server) getServerRaw(ctx context.Context, path string) (int, []byte, error) {
	resp, err := s.client().Do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}
