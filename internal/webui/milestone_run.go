package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/robertolupi/bottown/internal/milestone"
)

// MilestoneRunRecord is persisted in .bottown/milestone-<slug>.json while a
// CLI milestone run is alive.
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
	ID        string    `json:"id"` // milestone-<slug>, milestone-<slug>
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

// getMilestoneRun is the record of the CLI milestone run for slug
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
		// As for flows: no PID, no process to check.
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
