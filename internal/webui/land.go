package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/robertolupi/bottown/internal/activity"
	"github.com/robertolupi/bottown/internal/runs"
)

// LandRequest pins the candidate the owner chose in the inbox.
type LandRequest struct {
	Number  int64  `json:"number"`
	HeadSHA string `json:"head_sha"`
}

// LandingRecord tracks the UI-started worker, as FlowRecord tracks a flow.
type LandingRecord struct {
	Number    int64     `json:"number"`
	Issue     int64     `json:"issue"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

func (s *Server) landingPath(n int64, ext string) string {
	return filepath.Join(s.cfg.RepoTop, ".bottown", "runs", fmt.Sprintf("land-%d%s", n, ext))
}

func (s *Server) getLanding(n int64) *LandingRecord {
	b, err := os.ReadFile(s.landingPath(n, ".json"))
	if err != nil {
		return nil
	}
	var rec LandingRecord
	if json.Unmarshal(b, &rec) != nil || rec.PID <= 0 || rec.Number != n {
		return nil
	}
	if syscall.Kill(rec.PID, 0) != nil {
		_ = os.Remove(s.landingPath(n, ".json"))
		return nil
	}
	return &rec
}

func (s *Server) ExecuteLand(ctx context.Context, req LandRequest) (string, error) {
	if req.Number <= 0 || req.HeadSHA == "" {
		return "", errors.New("PR number and head_sha are required")
	}
	// Serialize launches so two clicks cannot overwrite a live worker's record.
	s.landingMu.Lock()
	defer s.landingMu.Unlock()
	if s.getLanding(req.Number) != nil {
		return "", errors.New("landing already running")
	}
	pr, err := s.pullRequest(ctx, req.Number)
	if err != nil {
		return "", err
	}
	if pr.HeadSHA != req.HeadSHA {
		return "", errors.New("PR head moved; reload and try again")
	}
	pid, err := s.startBackground([]string{"-i", s.cfg.Instance.Name, "land", fmt.Sprint(req.Number), "--head-sha", req.HeadSHA}, s.landingPath(req.Number, ".log"))
	if err != nil {
		return "", err
	}
	rec := LandingRecord{Number: req.Number, Issue: activity.BranchIssue(pr.HeadBranch), PID: pid, StartedAt: time.Now().UTC()}
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(s.landingPath(req.Number, ".json"), b, 0o600); err != nil {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		return "", err
	}
	return fmt.Sprintf("Landing !%d started in background", req.Number), nil
}

func (s *Server) stopLanding(id string) (string, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(id, "land-"), 10, 64)
	if err != nil || n <= 0 {
		return "", errors.New("invalid landing ID")
	}
	rec := s.getLanding(n)
	if rec == nil {
		return "", errors.New("no active landing")
	}
	if err := syscall.Kill(-rec.PID, syscall.SIGTERM); err != nil {
		return "", err
	}
	return fmt.Sprintf("Landing !%d stopped", n), nil
}

// liveLandings are the UI-started landings still running, each with its
// log's last lines rendered.
func (s *Server) liveLandings(lines int) []LiveRunInfo {
	paths, _ := filepath.Glob(filepath.Join(s.cfg.RepoTop, ".bottown", "runs", "land-*.json"))
	live := []LiveRunInfo{}
	for _, path := range paths {
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "land-"), ".json"), 10, 64)
		if err != nil {
			continue
		}
		rec := s.getLanding(n)
		if rec == nil {
			continue
		}
		id := fmt.Sprintf("land-%d", n)
		b, _ := runs.ReadLog(s.landingPath(n, ".log"))
		live = append(live, LiveRunInfo{ID: id, Target: fmt.Sprintf("!%d", n), TargetURL: fmt.Sprintf("%s/%s/pulls/%d", s.cfg.Instance.ForgejoURL(), s.cfg.FullRepo, n), PR: n, Issue: rec.Issue, Kind: "land", Actor: "bottown", Created: rec.StartedAt, Elapsed: formatDuration(time.Since(rec.StartedAt)), LogTail: tail(runs.RenderLog("", b), lines), LastOutput: lastOutput(s.landingPath(n, ".log")), Denials: []Denial{}})
	}
	return live
}
