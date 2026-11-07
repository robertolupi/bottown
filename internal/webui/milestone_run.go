package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/robertolupi/bottown/internal/agents"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/milestone"
)

// MilestoneRunRequest starts `bottown milestone run SLUG --agents SPEC
// --parallel N` for a plan's epic.
type MilestoneRunRequest struct {
	Slug     string `json:"slug"`
	Agents   string `json:"agents"`
	Parallel int    `json:"parallel"`
}

// MilestoneRunRecord is persisted in .bottown/milestone-<slug>.json while a
// milestone run the UI started is alive, as FlowRecord is for a flow.
type MilestoneRunRecord struct {
	Slug      string    `json:"slug"`
	Agents    string    `json:"agents"`
	Parallel  int       `json:"parallel"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// MilestoneScheduler is a group's running milestone run: its pool and
// parallelism. Its child flows are the group's nodes with a flow.
type MilestoneScheduler struct {
	ID        string    `json:"id"` // the stop action's id, milestone-<slug>
	Agents    string    `json:"agents"`
	Pool      []string  `json:"pool"` // the spec's entries, as given
	Parallel  int       `json:"parallel"`
	StartedAt time.Time `json:"started_at"`
	Elapsed   string    `json:"elapsed"`
	Log       string    `json:"log"`
}

const milestoneRunPrefix = "milestone-"

func (s *Server) milestoneRunPath(slug, ext string) string {
	return filepath.Join(s.cfg.RepoTop, ".bottown", milestoneRunPrefix+slug+ext)
}

// getMilestoneRun is the record of the UI-started milestone run for slug
// while its process is alive; a stale record is removed.
func (s *Server) getMilestoneRun(slug string) *MilestoneRunRecord {
	if !milestone.ValidSlug(slug) {
		return nil
	}
	path := s.milestoneRunPath(slug, ".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec MilestoneRunRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.PID <= 0 {
		// As for flows: no PID, nothing to check or stop.
		return nil
	}
	if err := syscall.Kill(rec.PID, 0); err != nil {
		_ = os.Remove(path)
		return nil
	}
	return &rec
}

// milestoneScheduler is the group view of slug's running milestone run.
func (s *Server) milestoneScheduler(slug string, now time.Time) *MilestoneScheduler {
	rec := s.getMilestoneRun(slug)
	if rec == nil {
		return nil
	}
	return &MilestoneScheduler{
		ID:        milestoneRunPrefix + slug,
		Agents:    rec.Agents,
		Pool:      strings.Split(rec.Agents, ","),
		Parallel:  rec.Parallel,
		StartedAt: rec.StartedAt,
		Elapsed:   formatDuration(now.Sub(rec.StartedAt)),
		Log:       ".bottown/" + milestoneRunPrefix + slug + ".log",
	}
}

// ExecuteMilestoneRun validates the pool with the agents parser that
// `milestone run` uses, so the owner sees its message here, and starts the
// scheduler as a background process that outlives the UI.
func (s *Server) ExecuteMilestoneRun(ctx context.Context, req MilestoneRunRequest) (string, error) {
	req.Slug = strings.TrimSpace(req.Slug)
	req.Agents = strings.TrimSpace(req.Agents)
	if !milestone.ValidSlug(req.Slug) {
		return "", fmt.Errorf("invalid plan slug %q", req.Slug)
	}
	if req.Parallel < 1 {
		return "", errors.New("parallel must be at least 1")
	}
	actorMap, err := s.fetchActorMap(ctx)
	if err != nil {
		return "", fmt.Errorf("listing actors: %w", err)
	}
	actors := make([]config.Actor, 0, len(actorMap))
	for _, a := range actorMap {
		actors = append(actors, config.Actor{Name: a.Name, Family: a.Family, Harness: a.Harness})
	}
	if _, err := agents.Parse(req.Agents, actors); err != nil {
		return "", err
	}
	if rec := s.getMilestoneRun(req.Slug); rec != nil {
		return "", fmt.Errorf("a milestone run for %s is already running (pid %d)", req.Slug, rec.PID)
	}

	args := []string{"-i", s.cfg.Instance.Name, "milestone", "run", req.Slug,
		"--agents", req.Agents, "--parallel", fmt.Sprint(req.Parallel)}
	logPath := s.milestoneRunPath(req.Slug, ".log")
	pid, err := s.startBackground(args, logPath)
	if err != nil {
		return "", fmt.Errorf("start milestone run: %w", err)
	}
	rec := MilestoneRunRecord{Slug: req.Slug, Agents: req.Agents, Parallel: req.Parallel, PID: pid, StartedAt: time.Now().UTC()}
	if b, err := json.Marshal(rec); err == nil {
		_ = os.WriteFile(s.milestoneRunPath(req.Slug, ".json"), b, 0o600)
	}
	return fmt.Sprintf("Milestone run started for %s in background (log: .bottown/%s%s.log)", req.Slug, milestoneRunPrefix, req.Slug), nil
}

// stopMilestoneRun ends slug's milestone run as ExecuteStop ends a flow:
// SIGTERM to its process group. The scheduler interrupts its flows, each
// in a group of its own, and exits once they have stopped.
func (s *Server) stopMilestoneRun(slug string) (string, error) {
	rec := s.getMilestoneRun(slug)
	if rec == nil {
		return "", fmt.Errorf("no active milestone run for %s", slug)
	}
	_ = syscall.Kill(-rec.PID, syscall.SIGTERM)
	_ = os.Remove(s.milestoneRunPath(slug, ".json"))
	return fmt.Sprintf("Milestone run for %s stopped", slug), nil
}
