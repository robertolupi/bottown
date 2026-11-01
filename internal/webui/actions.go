package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
)

// FlowRequest specifies parameters to start an issue flow.
type FlowRequest struct {
	Issue     int64  `json:"issue"`
	Implement string `json:"implement"`
	Verify    string `json:"verify"`
	Rounds    int    `json:"rounds,omitempty"`
	Timeout   string `json:"timeout,omitempty"`
}

// RunActionRequest specifies parameters to start a single run.
type RunActionRequest struct {
	Kind            string `json:"kind"` // implement, verify, discuss
	Target          int64  `json:"target"`
	Actor           string `json:"actor"`
	Question        string `json:"question,omitempty"`
	AllowSameFamily bool   `json:"allow_same_family,omitempty"`
}

// StopRequest specifies a run to stop.
type StopRequest struct {
	ID string `json:"id"`
}

// GCRequest specifies options for run cleanup.
type GCRequest struct {
	Failed bool `json:"failed,omitempty"`
}

// FlowRecord is persisted in .bottown/flow-<issue>.json when a flow is started.
type FlowRecord struct {
	Issue     int64     `json:"issue"`
	Implement string    `json:"implement"`
	Verify    string    `json:"verify"`
	Rounds    int       `json:"rounds"`
	Timeout   string    `json:"timeout"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// ExecuteFlow starts a background `bottown flow` process that outlives the UI.
func (s *Server) ExecuteFlow(ctx context.Context, req FlowRequest) (string, error) {
	if req.Issue <= 0 {
		return "", errors.New("issue number is required")
	}
	if req.Implement == "" {
		return "", errors.New("implementer actor is required")
	}
	if req.Verify == "" {
		return "", errors.New("verifier actor is required")
	}
	if req.Timeout != "" {
		if _, err := time.ParseDuration(req.Timeout); err != nil {
			return "", fmt.Errorf("invalid timeout %q: %w", req.Timeout, err)
		}
	}

	// Cross-family rule verification upfront. If fetching actors fails (e.g. server offline),
	// we proceed because the CLI will enforce the family rule independently.
	actorMap, err := s.fetchActorMap(ctx)
	if err == nil {
		impAct := actorMap[req.Implement]
		verAct := actorMap[req.Verify]
		if impAct != nil && verAct != nil && impAct.Family != "" && impAct.Family == verAct.Family {
			return "", fmt.Errorf("verifier %q is in the same model family (%s) as implementer %q", req.Verify, verAct.Family, req.Implement)
		}
	}

	args := []string{"-i", s.cfg.Instance.Name, "flow", fmt.Sprint(req.Issue),
		"--implement", req.Implement,
		"--verify", req.Verify,
	}
	rounds := req.Rounds
	if rounds <= 0 {
		rounds = 3
	}
	args = append(args, "--rounds", fmt.Sprint(rounds))
	if req.Timeout != "" {
		args = append(args, "--timeout", req.Timeout)
	}

	logName := fmt.Sprintf("flow-%d.log", req.Issue)
	logPath := filepath.Join(s.cfg.RepoTop, ".bottown", logName)
	pid, err := s.startBackground(args, logPath)
	if err != nil {
		return "", fmt.Errorf("start flow: %w", err)
	}

	record := FlowRecord{
		Issue:     req.Issue,
		Implement: req.Implement,
		Verify:    req.Verify,
		Rounds:    rounds,
		Timeout:   req.Timeout,
		PID:       pid,
		StartedAt: time.Now().UTC(),
	}
	if recBytes, err := json.Marshal(record); err == nil {
		infoPath := filepath.Join(s.cfg.RepoTop, ".bottown", fmt.Sprintf("flow-%d.json", req.Issue))
		_ = os.WriteFile(infoPath, recBytes, 0o600)
	}

	return fmt.Sprintf("Flow started for issue #%d in background (log: .bottown/%s)", req.Issue, logName), nil
}

// ExecuteRun starts a single run in the background.
func (s *Server) ExecuteRun(ctx context.Context, req RunActionRequest) (string, error) {
	if req.Target <= 0 {
		return "", errors.New("target issue or PR number is required")
	}
	if req.Actor == "" {
		return "", errors.New("actor is required")
	}
	switch req.Kind {
	case "implement", "verify", "discuss":
	default:
		return "", fmt.Errorf("unknown run kind %q (must be implement, verify, or discuss)", req.Kind)
	}

	// Verify cross-family rule for verify runs
	if req.Kind == "verify" && !req.AllowSameFamily {
		actorMap, _ := s.fetchActorMap(ctx)
		org, repo := s.orgAndRepo()
		pullPath := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s/pulls/%d", org, repo, req.Target)
		var pr sdk.PullRequest
		if err := s.callServer(ctx, http.MethodGet, pullPath, nil, &pr); err == nil && pr.Poster != nil {
			author := pr.Poster.UserName
			authorAct := actorMap[author]
			verAct := actorMap[req.Actor]
			if authorAct != nil && verAct != nil && authorAct.Family != "" && authorAct.Family == verAct.Family {
				return "", fmt.Errorf("verifier %q is in the same model family (%s) as PR author %q; check 'Allow same family' to bypass", req.Actor, verAct.Family, author)
			}
		}
	}

	args := []string{"-i", s.cfg.Instance.Name, "run", req.Kind, "-a", req.Actor}
	if req.Kind == "verify" && req.AllowSameFamily {
		args = append(args, "--allow-same-family")
	}
	if req.Kind == "discuss" && req.Question != "" {
		args = append(args, "--", fmt.Sprint(req.Target), req.Question)
	} else {
		args = append(args, fmt.Sprint(req.Target))
	}

	logName := fmt.Sprintf("run-%s-%d.log", req.Kind, req.Target)
	logPath := filepath.Join(s.cfg.RepoTop, ".bottown", logName)
	if _, err := s.startBackground(args, logPath); err != nil {
		return "", fmt.Errorf("start run: %w", err)
	}

	return fmt.Sprintf("Run (%s) started in background for #%d", req.Kind, req.Target), nil
}

// ExecuteStop stops a live run or a flow supervising it.
func (s *Server) ExecuteStop(ctx context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("run or flow ID is required")
	}

	if strings.HasPrefix(id, "flow-") {
		var issueNum int64
		if _, err := fmt.Sscanf(id, "flow-%d", &issueNum); err == nil && issueNum > 0 {
			infoPath := filepath.Join(s.cfg.RepoTop, ".bottown", fmt.Sprintf("flow-%d.json", issueNum))
			if data, err := os.ReadFile(infoPath); err == nil {
				var rec FlowRecord
				if err := json.Unmarshal(data, &rec); err == nil && rec.PID > 0 {
					_ = syscall.Kill(-rec.PID, syscall.SIGTERM)
				}
				_ = os.Remove(infoPath)
			}
			// Also stop any live run on this issue
			if live, err := s.ComputeLiveRuns(ctx); err == nil {
				for _, r := range live {
					if r.Flow != nil && r.Flow.Issue == issueNum {
						_, _ = s.runCommand(ctx, []string{"-i", s.cfg.Instance.Name, "run", "stop", r.ID})
					}
				}
			}
			return fmt.Sprintf("Flow for issue #%d stopped", issueNum), nil
		}
	}

	args := []string{"-i", s.cfg.Instance.Name, "run", "stop", id}
	out, err := s.runCommand(ctx, args)
	if err != nil {
		return "", fmt.Errorf("stop run: %w", err)
	}
	return out, nil
}

// ExecuteGC removes finished runs and cleans up state.
func (s *Server) ExecuteGC(ctx context.Context, failed bool) (string, error) {
	args := []string{"-i", s.cfg.Instance.Name, "run", "gc"}
	if failed {
		args = append(args, "--failed")
	}
	out, err := s.runCommand(ctx, args)
	if err != nil {
		return "", fmt.Errorf("run gc: %w", err)
	}
	return out, nil
}

func (s *Server) bottownBin() string {
	if s.cfg.BottownPath != "" {
		return s.cfg.BottownPath
	}
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return "bottown"
}

func (s *Server) startBackground(args []string, logPath string) (int, error) {
	bin := s.bottownBin()
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = s.cfg.RepoTop
	cmd.Env = os.Environ()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Outlive the UI server process
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, err
	}

	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		_ = logFile.Close()
	}()

	// Wait briefly to catch immediate startup failures (e.g. invalid flags, actor limits)
	select {
	case err := <-done:
		if err != nil {
			logBytes, _ := os.ReadFile(logPath)
			lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
			lastLine := ""
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.TrimSpace(lines[i]) != "" {
					lastLine = strings.TrimSpace(lines[i])
					break
				}
			}
			if lastLine != "" {
				return 0, fmt.Errorf("%w: %s", err, lastLine)
			}
			return 0, err
		}
	case <-time.After(300 * time.Millisecond):
	}

	return pid, nil
}

func (s *Server) runCommand(ctx context.Context, args []string) (string, error) {
	bin := s.bottownBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = s.cfg.RepoTop
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		if trimmed != "" {
			return "", fmt.Errorf("%w: %s", err, trimmed)
		}
		return "", err
	}
	return trimmed, nil
}
