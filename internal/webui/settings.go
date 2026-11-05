package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
)

// afterPullTimeout bounds one run of the after-pull steps. bottown's own
// step, `make install`, can rebuild and upgrade prod.
const afterPullTimeout = 30 * time.Minute

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

type afterPullRequest struct {
	Steps []string `json:"steps"`
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

// ExecuteAfterPull runs the after-pull steps in the main checkout, one at a
// time with `sh -c`, stopping at the first that fails. steps is what the
// owner was shown and confirmed: the steps are read again from the forge's
// default branch, and nothing runs unless they are exactly those.
func (s *Server) ExecuteAfterPull(ctx context.Context, steps []string) (string, error) {
	if !s.afterPullMu.TryLock() {
		return "", fmt.Errorf("after-pull steps are already running")
	}
	defer s.afterPullMu.Unlock()

	st := s.LoadRepoSettings(ctx)
	switch {
	case st.Error != "":
		return "", fmt.Errorf("read %s: %s", st.File, st.Error)
	case !st.Found || len(st.AfterPull) == 0:
		return "", fmt.Errorf("%s on %s declares no after-pull steps", st.File, st.Branch)
	case !slices.Equal(steps, st.AfterPull):
		return "", fmt.Errorf("the after-pull steps in %s on %s changed; refresh and review them", st.File, st.Branch)
	}

	var out bytes.Buffer
	short := st.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	fmt.Fprintf(&out, "# %s at %s %s\n", st.File, st.Branch, short)
	for _, step := range st.AfterPull {
		fmt.Fprintf(&out, "$ %s\n", step)
		cmd := exec.CommandContext(ctx, "sh", "-c", step)
		cmd.Dir = s.cfg.RepoTop
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return strings.TrimSpace(out.String()), fmt.Errorf("%s: %w", step, err)
		}
	}
	return strings.TrimSpace(out.String()), nil
}

func (s *Server) handleActionAfterPull(w http.ResponseWriter, r *http.Request) {
	var req afterPullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{Error: "invalid JSON body: " + err.Error()})
		return
	}
	if len(req.Steps) == 0 {
		writeJSON(w, http.StatusBadRequest, ActionResult{Error: "the confirmed steps are required"})
		return
	}
	// The steps can outlast the server's write timeout, and must not be
	// killed halfway (`make install` upgrades prod) if the page goes away.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(afterPullTimeout + time.Minute))
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), afterPullTimeout)
	defer cancel()

	out, err := s.ExecuteAfterPull(ctx, req.Steps)
	res := ActionResult{Success: err == nil, Output: out}
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusInternalServerError, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
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
