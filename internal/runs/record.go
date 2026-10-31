package runs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Local run states. The server separately tracks whether the key is live.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusDead      = "dead" // recorded as running, but its process is gone
)

// Record is the host-side record of a run, written next to its worktree.
type Record struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Actor           string     `json:"actor"`
	Family          string     `json:"family"`
	Instance        string     `json:"instance"`
	Repo            string     `json:"repo"` // org/name
	Issue           int64      `json:"issue,omitempty"`
	PR              int64      `json:"pr,omitempty"`
	Branch          string     `json:"branch,omitempty"`
	SHA             string     `json:"sha,omitempty"`
	Interactive     bool       `json:"interactive,omitempty"`
	AllowSameFamily bool       `json:"allow_same_family,omitempty"`
	Harness         string     `json:"harness"`
	Status          string     `json:"status"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	PID             int        `json:"pid,omitempty"`
	Started         time.Time  `json:"started"`
	Ended           *time.Time `json:"ended,omitempty"`
	Worktree        string     `json:"worktree"`
	Key             string     `json:"key,omitempty"` // only while the run is live
	ServerURL       string     `json:"server_url"`
}

// Target is "#<issue>" or "!<pr>".
func (r Record) Target() string {
	if r.PR > 0 {
		return "!" + itoa(r.PR)
	}
	return "#" + itoa(r.Issue)
}

// Mode is "batch" or "interactive".
func (r Record) Mode() string {
	if r.Interactive {
		return "interactive"
	}
	return "batch"
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Path of a run's file with the given suffix (".json", ".log", ...).
func (w *Workspace) Path(id, suffix string) string { return filepath.Join(w.RunsDir, id+suffix) }

// Save writes the record with 0600.
func (w *Workspace) Save(r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := w.Path(r.ID, ".json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.Path(r.ID, ".json"))
}

// Load reads one record.
func (w *Workspace) Load(id string) (Record, error) {
	return LoadFile(w.Path(id, ".json"))
}

// LoadFile reads a record file.
func LoadFile(path string) (Record, error) {
	var r Record
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

// Records lists local runs, newest first, marking running records whose
// process is gone as dead.
func (w *Workspace) Records() ([]Record, error) {
	ents, err := os.ReadDir(w.RunsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".mcp.json") {
			continue
		}
		r, err := LoadFile(filepath.Join(w.RunsDir, name))
		if err != nil {
			continue
		}
		if r.Status == StatusRunning && !alive(r.PID) {
			r.Status = StatusDead
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Record) int { return b.Started.Compare(a.Started) })
	return out, nil
}

// RemoveFiles deletes a run's files (not its worktree).
func (w *Workspace) RemoveFiles(id string) {
	for _, s := range []string{".json", ".log", ".mcp.json", ".prompt.md"} {
		_ = os.Remove(w.Path(id, s))
	}
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// DirSize is the total size of regular files under dir.
func DirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
