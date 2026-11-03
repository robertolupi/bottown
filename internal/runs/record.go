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
	StatusPreparing = "preparing"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusDead      = "dead" // recorded as preparing or running, but its process is gone
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
	Question        string     `json:"question,omitempty"` // discuss: the owner's question
	Message         string     `json:"message,omitempty"`  // the owner's note for the prompt (--message)
	Harness         string     `json:"harness"`
	Spec            string     `json:"spec,omitempty"` // the harness's model choice, from ACTOR:SPEC
	Status          string     `json:"status"`
	StopRequested   bool       `json:"stop_requested,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	PID             int        `json:"pid,omitempty"`
	HarnessPID      int        `json:"harness_pid,omitempty"`
	Started         time.Time  `json:"started"`
	Ended           *time.Time `json:"ended,omitempty"`
	Worktree        string     `json:"worktree"`
	Key             string     `json:"key,omitempty"` // only while the run is live
	ServerURL       string     `json:"server_url"`
	// Attachments is the run's directory of issue attachments (see
	// attachments.go); empty for runs started before it existed.
	Attachments string `json:"attachments,omitempty"`
	// TmpDir records the launcher's scratch path so gc need not share its TMPDIR.
	TmpDir string `json:"tmp_dir,omitempty"`
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

// Save creates a record with 0600. Existing records must be changed with Update.
func (w *Workspace) Save(r Record) error {
	return w.withRecordLock(r.ID, func() error {
		if _, err := os.Stat(w.Path(r.ID, ".json")); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return os.ErrExist
		}
		return w.writeRecord(r)
	})
}

// Update serializes a read-modify-write across processes. The callback must not
// call Save or Update for this record. An error leaves the record unchanged.
func (w *Workspace) Update(id string, change func(*Record) error) error {
	return w.withRecordLock(id, func() error {
		r, err := w.Load(id)
		if err != nil {
			return err
		}
		if err := change(&r); err != nil {
			return err
		}
		return w.writeRecord(r)
	})
}

func (w *Workspace) withRecordLock(id string, fn func() error) error {
	// Keep the lock file: unlinking it could give waiters different inodes.
	f, err := os.OpenFile(w.Path(id, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func (w *Workspace) writeRecord(r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(w.RunsDir, r.ID+".json.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), w.Path(r.ID, ".json"))
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

// Records lists local runs, newest first, marking preparing and running
// records whose launcher is gone as dead.
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
		if (r.Status == StatusPreparing || r.Status == StatusRunning) && !r.LauncherAlive() {
			r.Status = StatusDead
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Record) int { return b.Started.Compare(a.Started) })
	return out, nil
}

// RemoveFiles deletes a run's files (not its worktree).
func (w *Workspace) RemoveFiles(id string) {
	if r, err := w.Load(id); err == nil && r.TmpDir != "" {
		_ = os.RemoveAll(r.TmpDir)
	}
	for _, s := range []string{".json", ".log", LiveTranscriptSuffix, ".agy.log", ".kimi-agent.md", ".mcp.json", ".prompt.md"} {
		_ = os.Remove(w.Path(id, s))
	}
	_ = RemoveAttachments(w.AttachmentsDir(id))
	// Clean up scratch directories from runs created before TmpDir was recorded.
	_ = os.RemoveAll(w.Path(id, ".tmp"))
}

// LauncherAlive reports whether the process that launched the run, and
// owns its record, is still running.
func (r Record) LauncherAlive() bool { return alive(r.PID) }

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
