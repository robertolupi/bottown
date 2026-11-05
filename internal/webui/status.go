package webui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/runs"
)

// UIStatusResponse is returned by GET /api/status.
type UIStatusResponse struct {
	Instance    string           `json:"instance"`
	Repo        string           `json:"repo"`
	Upstream    UpstreamStatus   `json:"upstream"`
	Forge       ForgeStatus      `json:"forge"`
	Health      HealthChecklist  `json:"health"`
	Links       RepoLinks        `json:"links"`
	Inbox       []InboxItem      `json:"inbox"`
	InboxError  string           `json:"inbox_error,omitempty"`
	LiveRuns    []LiveRunInfo    `json:"live_runs"`
	ActiveFlows []ActiveFlowInfo `json:"active_flows"`
	Actors      []ActorDetail    `json:"actors"`
}

type UpstreamStatus struct {
	HasUpstream     bool             `json:"has_upstream"`
	OriginURL       string           `json:"origin_url,omitempty"`
	FetchedAt       *time.Time       `json:"fetched_at,omitempty"`
	HeadSHA         string           `json:"head_sha,omitempty"`
	AheadCount      int              `json:"ahead_count"`
	BehindCount     int              `json:"behind_count"`
	UnpushedCommits []UnpushedCommit `json:"unpushed_commits"`
	Error           string           `json:"error,omitempty"`
}

type UnpushedCommit struct {
	SHA      string `json:"sha"`
	ShortSHA string `json:"short_sha"`
	Subject  string `json:"subject"`
	Author   string `json:"author"`
	Date     string `json:"date"`
	PRNumber int    `json:"pr_number,omitempty"`
}

type ForgeStatus struct {
	Remote        string `json:"remote"`
	Repo          string `json:"repo"`
	ForgeURL      string `json:"forge_url"`
	DefaultBranch string `json:"default_branch"`
	CheckoutSHA   string `json:"checkout_sha"`
	ForgeSHA      string `json:"forge_sha"`
	BehindCount   int    `json:"behind_count"`
	AheadCount    int    `json:"ahead_count"`
}

type HealthChecklist struct {
	AgentInstructions AgentInstructionsCheck `json:"agent_instructions"`
	ProtectedMain     ProtectedMainCheck     `json:"protected_main"`
	Worktrees         []ActorWorktreeInfo    `json:"worktrees"`
	HarnessCommands   []HarnessCommandInfo   `json:"harness_commands"`
	Version           VersionCheck           `json:"version"`
	RepoSettings      RepoSettingsCheck      `json:"repo_settings"`
}

type AgentInstructionsCheck struct {
	Found    bool   `json:"found"`
	Filename string `json:"filename,omitempty"`
}

type ProtectedMainCheck struct {
	Checked   bool   `json:"checked"`
	Protected bool   `json:"protected"`
	Error     string `json:"error,omitempty"`
}

type ActorWorktreeInfo struct {
	Actor     string `json:"actor"`
	Path      string `json:"path"`
	Head      string `json:"head"`
	IsDirty   bool   `json:"is_dirty"`
	ActiveRun string `json:"active_run,omitempty"`
	LastRun   string `json:"last_run,omitempty"`
	// Ephemeral marks a launched run's own worktree under .bottown/runs,
	// rather than an actor's permanent worktree.
	Ephemeral bool   `json:"ephemeral,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Target    string `json:"target,omitempty"`
}

type HarnessCommandInfo struct {
	Harness   string `json:"harness"`
	Installed bool   `json:"installed"`
	Current   bool   `json:"current"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

type VersionCheck struct {
	CLIVersion    string `json:"cli_version"`
	ServerVersion string `json:"server_version"`
	Matches       bool   `json:"matches"`
}

type RepoLinks struct {
	Repo    string `json:"repo"`
	Issues  string `json:"issues"`
	Pulls   string `json:"pulls"`
	Commits string `json:"commits"`
}

type ActionResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
}

var prRegex = regexp.MustCompile(`(?:Merge pull request '.*'|PR)\s+[#!](\d+)|\([#!](\d+)\)`)

// git runs git in the owner's checkout.
func (s *Server) git(ctx context.Context, args ...string) (string, error) {
	return runs.GitCtx(ctx, s.cfg.RepoTop, nil, args...)
}

// fetchTimeout bounds a git fetch: status polls and page loads wait on it.
var fetchTimeout = 5 * time.Second

func (s *Server) snapshot() *UIStatusResponse {
	s.lastStatusMu.RLock()
	defer s.lastStatusMu.RUnlock()
	return s.lastStatus
}

func (s *Server) setSnapshot(st *UIStatusResponse) {
	s.lastStatusMu.Lock()
	defer s.lastStatusMu.Unlock()
	s.lastStatus = st
}

// ComputeStatus calculates the Upstream and Health view data.
func (s *Server) ComputeStatus(ctx context.Context, forceFetch bool) (*UIStatusResponse, error) {
	if !forceFetch {
		if !s.statusMu.TryLock() {
			if snap := s.snapshot(); snap != nil {
				return snap, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			s.statusMu.Lock()
			if snap := s.snapshot(); snap != nil {
				s.statusMu.Unlock()
				return snap, nil
			}
			defer s.statusMu.Unlock()
			return s.computeStatusLocked(ctx, forceFetch)
		}
		defer s.statusMu.Unlock()
		return s.computeStatusLocked(ctx, forceFetch)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.computeStatusLocked(ctx, forceFetch)
}

func (s *Server) computeStatusLocked(ctx context.Context, forceFetch bool) (*UIStatusResponse, error) {
	res := &UIStatusResponse{
		Instance: s.cfg.Instance.Name,
		Repo:     s.cfg.FullRepo,
		Links: RepoLinks{
			Repo:    s.cfg.Instance.ForgejoURL() + "/" + s.cfg.FullRepo,
			Issues:  s.cfg.Instance.ForgejoURL() + "/" + s.cfg.FullRepo + "/issues",
			Pulls:   s.cfg.Instance.ForgejoURL() + "/" + s.cfg.FullRepo + "/pulls",
			Commits: s.cfg.Instance.ForgejoURL() + "/" + s.cfg.FullRepo + "/commits",
		},
	}

	// 1. Forge Status
	remote := s.cfg.Instance.Remote()
	res.Forge.Remote = remote
	res.Forge.Repo = s.cfg.FullRepo
	res.Forge.ForgeURL = s.cfg.Instance.ForgejoURL()

	base := "main"
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	if b, err := runs.RemoteDefaultBranch(fctx, s.cfg.RepoTop, remote); err == nil && b != "" {
		base = b
	}
	res.Forge.DefaultBranch = base

	// Fetch forge remote
	_, _ = s.git(fctx, "fetch", "-q", remote)
	cancel()

	if checkoutSHA, err := s.git(ctx, "rev-parse", "--short", "HEAD"); err == nil {
		res.Forge.CheckoutSHA = checkoutSHA
	}
	remoteRef := "refs/remotes/" + remote + "/" + base
	if forgeSHA, err := s.git(ctx, "rev-parse", "--short", remoteRef); err == nil {
		res.Forge.ForgeSHA = forgeSHA
		if out, err := s.git(ctx, "rev-list", "--count", "HEAD.."+remoteRef); err == nil {
			res.Forge.BehindCount, _ = strconv.Atoi(out)
		}
		if out, err := s.git(ctx, "rev-list", "--count", remoteRef+"..HEAD"); err == nil {
			res.Forge.AheadCount, _ = strconv.Atoi(out)
		}
	}

	// 2. Upstream Status (evaluated against refs/heads/<base>, not HEAD)
	localRef := "refs/heads/" + base
	if headSHA, err := s.git(ctx, "rev-parse", localRef); err == nil {
		res.Upstream.HeadSHA = headSHA
	}

	originURL, err := s.git(ctx, "remote", "get-url", "origin")
	if err == nil && originURL != "" {
		res.Upstream.HasUpstream = true
		res.Upstream.OriginURL = originURL

		// Fetch origin if forced or never fetched
		if forceFetch || s.lastFetch.IsZero() {
			now := time.Now()
			s.lastFetch = now
			fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
			if _, err := s.git(fctx, "fetch", "-q", "origin"); err == nil {
				s.lastSuccessfulFetch = now
				s.lastOriginErr = ""
				res.Upstream.FetchedAt = &now
			} else {
				s.lastOriginErr = err.Error()
				res.Upstream.Error = err.Error()
				if !s.lastSuccessfulFetch.IsZero() {
					res.Upstream.FetchedAt = &s.lastSuccessfulFetch
				}
			}
			cancel()
		} else {
			if !s.lastSuccessfulFetch.IsZero() {
				res.Upstream.FetchedAt = &s.lastSuccessfulFetch
			}
			res.Upstream.Error = s.lastOriginErr
		}

		originRef := "refs/remotes/origin/" + base
		if _, err := s.git(ctx, "rev-parse", "--verify", originRef); err == nil && res.Upstream.HeadSHA != "" {
			if out, err := s.git(ctx, "rev-list", "--count", originRef+".."+localRef); err == nil {
				res.Upstream.AheadCount, _ = strconv.Atoi(out)
			}
			if out, err := s.git(ctx, "rev-list", "--count", localRef+".."+originRef); err == nil {
				res.Upstream.BehindCount, _ = strconv.Atoi(out)
			}

			if res.Upstream.AheadCount > 0 {
				logOut, err := s.git(ctx, "log", "--format=%H%x1f%s%x1f%an%x1f%ad", "--date=short", originRef+".."+localRef)
				if err == nil && logOut != "" {
					lines := strings.Split(logOut, "\n")
					for _, line := range lines {
						parts := strings.Split(line, "\x1f")
						if len(parts) >= 4 {
							sha := parts[0]
							shortSHA := sha
							if len(sha) >= 12 {
								shortSHA = sha[:12]
							}
							subject := parts[1]
							author := parts[2]
							date := parts[3]

							prNum := 0
							if m := prRegex.FindStringSubmatch(subject); len(m) > 0 {
								raw := m[1]
								if raw == "" && len(m) > 2 {
									raw = m[2]
								}
								if n, err := strconv.Atoi(raw); err == nil {
									prNum = n
								}
							}

							res.Upstream.UnpushedCommits = append(res.Upstream.UnpushedCommits, UnpushedCommit{
								SHA:      sha,
								ShortSHA: shortSHA,
								Subject:  subject,
								Author:   author,
								Date:     date,
								PRNumber: prNum,
							})
						}
					}
				}
			}
		}
	} else {
		res.Upstream.HasUpstream = false
	}

	// 3. Health Checklist

	// 3a. Agent instructions
	if fileExists(filepath.Join(s.cfg.RepoTop, "AGENTS.md")) {
		res.Health.AgentInstructions.Found = true
		res.Health.AgentInstructions.Filename = "AGENTS.md"
	} else if fileExists(filepath.Join(s.cfg.RepoTop, "README.md")) {
		res.Health.AgentInstructions.Found = true
		res.Health.AgentInstructions.Filename = "README.md"
	}

	// 3b. Protected main on forge
	org, repoName, _ := strings.Cut(s.cfg.FullRepo, "/")
	if org != "" && repoName != "" {
		res.Health.ProtectedMain.Checked = true
		br, err := s.branch(ctx, base)
		switch {
		case err != nil:
			res.Health.ProtectedMain.Error = err.Error()
		case !br.Protected:
			res.Health.ProtectedMain.Error = "no branch protection rule covers " + base
		default:
			res.Health.ProtectedMain.Protected = true
		}
	}

	// 3c. Actor worktrees
	res.Health.Worktrees = s.computeWorktrees(ctx)

	// 3d. Harness slash commands
	if s.cfg.HarnessChecker != nil {
		home, _ := os.UserHomeDir()
		res.Health.HarnessCommands = s.cfg.HarnessChecker(home)
	}

	// 3e. Versions
	res.Health.Version.CLIVersion = s.cfg.CLIVersion
	var h struct {
		Version string `json:"version"`
	}
	if err := s.callServer(ctx, http.MethodGet, "/healthz", nil, &h); err == nil && h.Version != "" {
		res.Health.Version.ServerVersion = h.Version
		res.Health.Version.Matches = (s.cfg.CLIVersion == h.Version)
	} else {
		res.Health.Version.ServerVersion = "unreachable"
		res.Health.Version.Matches = false
	}

	// 3f. Repository settings (§4.4), from the forge's default branch
	res.Health.RepoSettings = s.LoadRepoSettings(ctx)

	// 4. Owner inbox (§4.1)
	inbox, err := s.ComputeInbox(ctx)
	if err != nil {
		res.Inbox = []InboxItem{}
		res.InboxError = err.Error()
	} else if inbox != nil {
		res.Inbox = inbox
	} else {
		res.Inbox = []InboxItem{}
	}

	// 5. Live runs and active flows (§4.2)
	liveRuns, err := s.ComputeLiveRuns(ctx)
	if err == nil && liveRuns != nil {
		res.LiveRuns = liveRuns
	} else {
		res.LiveRuns = []LiveRunInfo{}
	}
	res.ActiveFlows = s.ComputeActiveFlows(ctx, res.LiveRuns)

	// 6. Actors
	res.Actors = s.fetchActors(ctx)

	s.setSnapshot(res)
	return res, nil
}

// fetchActors lists the instance's actors, each with the host's default
// model ([actor_models]) for the run and flow forms; it is empty when the
// server cannot be reached.
func (s *Server) fetchActors(ctx context.Context) []ActorDetail {
	var actors []ActorDetail
	if err := s.callServer(ctx, http.MethodGet, "/v1/actors", nil, &actors); err != nil || actors == nil {
		return []ActorDetail{}
	}
	for i := range actors {
		actors[i].Model = s.cfg.Instance.ActorModel(actors[i].Name)
	}
	return actors
}

func (s *Server) computeWorktrees(ctx context.Context) []ActorWorktreeInfo {
	out, err := s.git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}

	// Live runs for this repository, from the server
	liveRuns := map[string]bool{}
	if runsList, err := s.serverRuns(ctx, "live", false); err == nil {
		for _, r := range runsList {
			liveRuns[r.ID] = true
		}
	}

	var wts []ActorWorktreeInfo
	for _, block := range strings.Split(out, "\n\n") {
		var path, head string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "branch "):
				head = strings.TrimPrefix(line, "branch refs/heads/")
			case line == "detached":
				head = "(detached)"
			}
		}
		if path == "" {
			continue
		}
		actor, _ := runs.GitCtx(ctx, path, nil, "config", "--worktree", "--get", "bottown.actor")
		if actor == "" {
			continue
		}
		lastRun, _ := runs.GitCtx(ctx, path, nil, "config", "--worktree", "--get", "bottown.run")
		if head == "(detached)" {
			if sha, err := runs.GitCtx(ctx, path, nil, "rev-parse", "--short", "HEAD"); err == nil {
				head = sha + " (detached)"
			}
		}

		statusOut, _ := runs.GitCtx(ctx, path, nil, "status", "--porcelain")
		isDirty := len(strings.TrimSpace(statusOut)) > 0

		// A leased worktree holds its run in bottown.run until `run end`.
		info := ActorWorktreeInfo{
			Actor:   actor,
			Path:    path,
			Head:    head,
			IsDirty: isDirty,
			LastRun: lastRun,
		}
		if lastRun != "" && liveRuns[lastRun] {
			info.ActiveRun = lastRun
		}
		wts = append(wts, info)
	}
	return append(wts, s.ephemeralWorktrees(ctx, liveRuns)...)
}

// ephemeralWorktrees lists the worktrees of launched runs (`bottown run
// implement/verify/discuss`) still present under .bottown/runs. They belong
// to the runs mirror, not the main checkout, so `git worktree list` in the
// checkout does not show them.
func (s *Server) ephemeralWorktrees(ctx context.Context, liveRuns map[string]bool) []ActorWorktreeInfo {
	runsDir := filepath.Join(s.cfg.RepoTop, ".bottown", "runs")
	ws := &runs.Workspace{RunsDir: runsDir}
	recs, err := ws.Records(io.Discard)
	if err != nil {
		return nil
	}
	var wts []ActorWorktreeInfo
	for _, r := range recs {
		if r.Repo != "" && r.Repo != s.cfg.FullRepo {
			continue
		}
		dir := r.Worktree
		if dir == "" {
			dir = ws.WorktreeDir(r.ID)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		head, _ := runs.GitCtx(ctx, dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
		if head == "HEAD" {
			if sha, err := runs.GitCtx(ctx, dir, nil, "rev-parse", "--short", "HEAD"); err == nil {
				head = sha + " (detached)"
			}
		}
		statusOut, _ := runs.GitCtx(ctx, dir, nil, "status", "--porcelain")
		info := ActorWorktreeInfo{
			Actor:     r.Actor,
			Path:      dir,
			Head:      head,
			IsDirty:   len(strings.TrimSpace(statusOut)) > 0,
			LastRun:   r.ID,
			Ephemeral: true,
			Kind:      r.Kind,
			Target:    r.Target(),
		}
		if r.Status == runs.StatusRunning || liveRuns[r.ID] {
			info.ActiveRun = r.ID
		}
		wts = append(wts, info)
	}
	return wts
}

// ExecutePull runs `git pull --ff-only bottown <base>` in the main checkout.
func (s *Server) ExecutePull(ctx context.Context) (string, error) {
	remote := s.cfg.Instance.Remote()
	base := "main"
	if b, err := runs.RemoteDefaultBranch(ctx, s.cfg.RepoTop, remote); err == nil && b != "" {
		base = b
	}

	cmd := runs.GitCommand(ctx, s.cfg.RepoTop, nil, "pull", "--ff-only", remote, base)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git pull: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// ExecutePush runs `git push origin <head>:refs/heads/<base>` from the main checkout,
// verifying that refs/heads/<base> has not moved if expectedHead is specified.
func (s *Server) ExecutePush(ctx context.Context, expectedHead string) (string, error) {
	remote := s.cfg.Instance.Remote()
	base := "main"
	if b, err := runs.RemoteDefaultBranch(ctx, s.cfg.RepoTop, remote); err == nil && b != "" {
		base = b
	}

	localRef := "refs/heads/" + base
	currentHead, err := s.git(ctx, "rev-parse", localRef)
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", localRef, err)
	}

	if expectedHead == "" {
		return "", fmt.Errorf("head commit SHA is required")
	}
	if currentHead != expectedHead {
		return "", fmt.Errorf("branch %s moved to %s (was %s); refresh and review commits before pushing", base, currentHead, expectedHead)
	}

	refspec := fmt.Sprintf("%s:refs/heads/%s", currentHead, base)
	cmd := runs.GitCommand(ctx, s.cfg.RepoTop, nil, "push", "origin", refspec)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git push origin %s: %w: %s", refspec, err, strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// serverTimeout bounds the web UI's calls to the bottown server: a page
// waits on them.
const serverTimeout = 10 * time.Second

// client is the bottown server as the owner.
func (s *Server) client() *client.Client {
	return client.New(s.cfg.Instance.ServerURL(), s.cfg.OwnerKey).WithTimeout(serverTimeout)
}

// callServer makes a JSON request to the bottown server as the owner. A
// refusal is a *client.APIError.
func (s *Server) callServer(ctx context.Context, method, path string, in, out any) error {
	return s.client().Call(ctx, method, path, in, out)
}
