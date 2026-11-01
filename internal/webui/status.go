package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// UIStatusResponse is returned by GET /api/status.
type UIStatusResponse struct {
	Instance   string          `json:"instance"`
	Repo       string          `json:"repo"`
	Upstream   UpstreamStatus  `json:"upstream"`
	Forge      ForgeStatus     `json:"forge"`
	Health     HealthChecklist `json:"health"`
	Links      RepoLinks       `json:"links"`
	Inbox      []InboxItem     `json:"inbox"`
	InboxError string          `json:"inbox_error,omitempty"`
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

func (s *Server) git(args ...string) (string, error) {
	return git(s.cfg.RepoTop, args...)
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// ComputeStatus calculates the Upstream and Health view data.
func (s *Server) ComputeStatus(ctx context.Context, forceFetch bool) (*UIStatusResponse, error) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

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
	if b, err := remoteDefaultBranch(s.cfg.RepoTop, remote); err == nil && b != "" {
		base = b
	}
	res.Forge.DefaultBranch = base

	// Fetch forge remote
	_, _ = s.git("fetch", "-q", remote)

	if checkoutSHA, err := s.git("rev-parse", "--short", "HEAD"); err == nil {
		res.Forge.CheckoutSHA = checkoutSHA
	}
	remoteRef := "refs/remotes/" + remote + "/" + base
	if forgeSHA, err := s.git("rev-parse", "--short", remoteRef); err == nil {
		res.Forge.ForgeSHA = forgeSHA
		if out, err := s.git("rev-list", "--count", "HEAD.."+remoteRef); err == nil {
			res.Forge.BehindCount, _ = strconv.Atoi(out)
		}
		if out, err := s.git("rev-list", "--count", remoteRef+"..HEAD"); err == nil {
			res.Forge.AheadCount, _ = strconv.Atoi(out)
		}
	}

	// 2. Upstream Status (evaluated against refs/heads/<base>, not HEAD)
	localRef := "refs/heads/" + base
	if headSHA, err := s.git("rev-parse", localRef); err == nil {
		res.Upstream.HeadSHA = headSHA
	}

	originURL, err := s.git("remote", "get-url", "origin")
	if err == nil && originURL != "" {
		res.Upstream.HasUpstream = true
		res.Upstream.OriginURL = originURL

		// Fetch origin if forced or never fetched
		if forceFetch || s.lastFetch.IsZero() {
			if _, err := s.git("fetch", "-q", "origin"); err == nil {
				now := time.Now()
				s.lastFetch = now
				res.Upstream.FetchedAt = &now
			} else {
				res.Upstream.Error = err.Error()
			}
		} else {
			res.Upstream.FetchedAt = &s.lastFetch
		}

		originRef := "refs/remotes/origin/" + base
		if _, err := s.git("rev-parse", "--verify", originRef); err == nil && res.Upstream.HeadSHA != "" {
			if out, err := s.git("rev-list", "--count", originRef+".."+localRef); err == nil {
				res.Upstream.AheadCount, _ = strconv.Atoi(out)
			}
			if out, err := s.git("rev-list", "--count", localRef+".."+originRef); err == nil {
				res.Upstream.BehindCount, _ = strconv.Atoi(out)
			}

			if res.Upstream.AheadCount > 0 {
				logOut, err := s.git("log", "--format=%H%x1f%s%x1f%an%x1f%ad", "--date=short", originRef+".."+localRef)
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
		path := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/branch_protections/%s", org, repoName, base)
		var bp map[string]any
		err := s.callServer(ctx, http.MethodGet, path, nil, &bp)
		if err == nil {
			res.Health.ProtectedMain.Protected = true
		} else {
			res.Health.ProtectedMain.Protected = false
			res.Health.ProtectedMain.Error = err.Error()
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

	return res, nil
}

func (s *Server) computeWorktrees(ctx context.Context) []ActorWorktreeInfo {
	out, err := s.git("worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}

	// Get active runs for this repository from server
	activeRuns := map[string]string{} // actor -> run ID
	var runsList []struct {
		ID    string `json:"id"`
		Actor string `json:"actor"`
	}
	repoName := s.cfg.FullRepo
	if _, repo, ok := strings.Cut(s.cfg.FullRepo, "/"); ok {
		repoName = repo
	}
	runsPath := "/v1/runs?state=live&repo=" + url.QueryEscape(repoName)
	if err := s.callServer(ctx, http.MethodGet, runsPath, nil, &runsList); err == nil {
		for _, r := range runsList {
			if r.Actor != "" {
				activeRuns[r.Actor] = r.ID
			}
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
		actor, _ := git(path, "config", "--worktree", "--get", "bottown.actor")
		if actor == "" {
			continue
		}
		lastRun, _ := git(path, "config", "--worktree", "--get", "bottown.run")
		if head == "(detached)" {
			if sha, err := git(path, "rev-parse", "--short", "HEAD"); err == nil {
				head = sha + " (detached)"
			}
		}

		statusOut, _ := git(path, "status", "--porcelain")
		isDirty := len(strings.TrimSpace(statusOut)) > 0

		info := ActorWorktreeInfo{
			Actor:     actor,
			Path:      path,
			Head:      head,
			IsDirty:   isDirty,
			ActiveRun: activeRuns[actor],
			LastRun:   lastRun,
		}
		wts = append(wts, info)
	}
	return wts
}

// ExecutePull runs `git pull --ff-only bottown <base>` in the main checkout.
func (s *Server) ExecutePull(ctx context.Context) (string, error) {
	remote := s.cfg.Instance.Remote()
	base := "main"
	if b, err := remoteDefaultBranch(s.cfg.RepoTop, remote); err == nil && b != "" {
		base = b
	}

	cmd := exec.CommandContext(ctx, "git", "-C", s.cfg.RepoTop, "pull", "--ff-only", remote, base)
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
	if b, err := remoteDefaultBranch(s.cfg.RepoTop, remote); err == nil && b != "" {
		base = b
	}

	localRef := "refs/heads/" + base
	currentHead, err := s.git("rev-parse", localRef)
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
	cmd := exec.CommandContext(ctx, "git", "-C", s.cfg.RepoTop, "push", "origin", refspec)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git push origin %s: %w: %s", refspec, err, strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (s *Server) callServer(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.Instance.ServerURL()+path, body)
	if err != nil {
		return err
	}
	if s.cfg.OwnerKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.OwnerKey)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func remoteDefaultBranch(top, remote string) (string, error) {
	if ref, err := git(top, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
		return strings.TrimPrefix(ref, remote+"/"), nil
	}
	out, err := git(top, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			return strings.Fields(ref)[0], nil
		}
	}
	return "main", nil
}
