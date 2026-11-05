package runs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// StatusUnknown marks a record file named for a run that could not be
	// read or parsed (see Records). Nothing about the run is known, so gc
	// keeps it.
	StatusUnknown = "unknown"
)

// Record is the host-side record of a run, written next to its worktree.
type Record struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Actor           string `json:"actor"`
	Family          string `json:"family"`
	Instance        string `json:"instance"`
	Repo            string `json:"repo"` // org/name
	Issue           int64  `json:"issue,omitempty"`
	PR              int64  `json:"pr,omitempty"`
	Branch          string `json:"branch,omitempty"`
	SHA             string `json:"sha,omitempty"`
	Interactive     bool   `json:"interactive,omitempty"`
	AllowSameFamily bool   `json:"allow_same_family,omitempty"`
	Question        string `json:"question,omitempty"` // discuss: the owner's question
	Message         string `json:"message,omitempty"`  // the owner's note for the prompt (--message)
	Harness         string `json:"harness"`
	Spec            string `json:"spec,omitempty"` // the harness's model choice, from ACTOR:SPEC
	Status          string `json:"status"`
	StopRequested   bool   `json:"stop_requested,omitempty"`
	ExitCode        *int   `json:"exit_code,omitempty"`
	PID             int    `json:"pid,omitempty"`
	// LauncherLock is the file the launcher holds locked while it lives
	// (see HoldLauncher); empty for runs started before it existed.
	LauncherLock string     `json:"launcher_lock,omitempty"`
	HarnessPID   int        `json:"harness_pid,omitempty"`
	Started      time.Time  `json:"started"`
	Ended        *time.Time `json:"ended,omitempty"`
	Worktree     string     `json:"worktree"`
	Key          string     `json:"key,omitempty"` // only while the run is live
	ServerURL    string     `json:"server_url"`
	// Attachments is the run's directory of issue attachments (see
	// attachments.go); empty for runs started before it existed.
	Attachments string `json:"attachments,omitempty"`
	// TmpDir records the launcher's scratch path so gc need not share its TMPDIR.
	TmpDir string `json:"tmp_dir,omitempty"`
	// FlowNote is bottown flow's note for the prompt: the failed checks an
	// implement round fixes, or the passed checks a verifier relies on.
	FlowNote string `json:"flow_note,omitempty"`
	// ChecksPassed: the flow ran the post-implement checks at the verify
	// run's SHA, and they passed.
	ChecksPassed bool `json:"checks_passed,omitempty"`
	// LoadError is why Records could not read the record file of a run it
	// lists as StatusUnknown; never saved.
	LoadError string `json:"-"`
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

// validRunID reports whether id names a run: "r-" and one or more hex
// digits, the form the server's run IDs take. A record or caller with
// anything else (an empty id, say) must not be turned into a path under the
// runs directory: WorktreeDir("") is that directory itself.
func validRunID(id string) bool {
	rest, ok := strings.CutPrefix(id, "r-")
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// Path of a run's file with the given suffix (".json", ".log", ...).
func (w *Workspace) Path(id, suffix string) string { return filepath.Join(w.RunsDir, id+suffix) }

// Save creates a record with 0600. Existing records must be changed with Update.
func (w *Workspace) Save(r Record) error { return w.Store(r.ID).Create(r) }

// Update serializes a read-modify-write of run id's record (see Store.Update).
func (w *Workspace) Update(id string, change func(*Record) error) error {
	return w.Store(id).Update(change)
}

// Store is run id's record in this workspace.
func (w *Workspace) Store(id string) Store { return Store{Path: w.Path(id, ".json")} }

// lockFile opens and exclusively locks a lock file, creating it with
// create; without create, a missing lock file is (nil, nil). Removing a
// run unlinks its lock file (see removeLock), so a lock taken on a file
// since unlinked or replaced is dropped and taken again: every holder locks
// the inode the path names.
func lockFile(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	for {
		f, err := os.OpenFile(path, flags, 0o600)
		if errors.Is(err, os.ErrNotExist) && !create {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return nil, err
		}
		held, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		named, err := os.Stat(path)
		if err == nil && os.SameFile(held, named) {
			return f, nil
		}
		f.Close() // releases the lock too
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
}

// removeLock unlinks a run's lock file once its record is gone. It holds
// the lock while it checks and unlinks, so no Save or Update is midway,
// and once the record is gone none takes it again for long: Update fails
// to load the record, and a run's id is never saved twice.
func (w *Workspace) removeLock(id string) error {
	f, err := lockFile(w.Path(id, ".lock"), false)
	if err != nil || f == nil {
		return err
	}
	defer f.Close()
	if _, err := os.Stat(w.Path(id, ".json")); !errors.Is(err, os.ErrNotExist) {
		return err // nil while the record exists
	}
	return os.Remove(w.Path(id, ".lock"))
}

// RemoveOrphanLocks removes the lock files of runs whose record is gone:
// those of runs removed before RemoveFiles removed locks, and any a late
// Update recreated.
func (w *Workspace) RemoveOrphanLocks() {
	ents, err := os.ReadDir(w.RunsDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".lock")
		if !ok || e.IsDir() || !validRunID(id) {
			continue
		}
		if _, err := os.Stat(w.Path(id, ".json")); errors.Is(err, os.ErrNotExist) {
			_ = w.removeLock(id)
		}
	}
}

// Load reads one record.
func (w *Workspace) Load(id string) (Record, error) { return w.Store(id).Load() }

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
// records whose launcher is gone as dead. A file that is not a run record
// (a check result, say) unmarshals into a record with no id, or one naming a
// different run; both are skipped, with one warning naming the file, so no
// caller treats it as a run and removes paths derived from its empty id.
// A file named for a run that cannot be read or parsed is listed as that
// run with StatusUnknown and the error in LoadError, rather than dropped:
// its worktree and key may still exist. Any other unreadable file is
// skipped with a warning.
func (w *Workspace) Records(warn io.Writer) ([]Record, error) {
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
			if id := strings.TrimSuffix(name, ".json"); validRunID(id) {
				out = append(out, Record{ID: id, Status: StatusUnknown, LoadError: err.Error()})
			} else if warn != nil {
				fmt.Fprintf(warn, "warning: %s: not a run record (%v); skipped\n", name, err)
			}
			continue
		}
		if !validRunID(r.ID) || name != r.ID+".json" {
			if warn != nil {
				fmt.Fprintf(warn, "warning: %s: not a run record; skipped\n", name)
			}
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

// RemoveFiles deletes a run's files (not its worktree), its lock file last.
func (w *Workspace) RemoveFiles(id string) {
	if !validRunID(id) {
		return
	}
	if r, err := w.Load(id); err == nil && r.TmpDir != "" {
		_ = os.RemoveAll(r.TmpDir)
	}
	for _, s := range []string{".json", ".log", LiveTranscriptSuffix, ".agy.log", ".kimi-agent.md", ".mcp.json", ".prompt.md", ".check.log", ".check.json", LauncherLockSuffix} {
		_ = os.Remove(w.Path(id, s))
	}
	_ = RemoveAttachments(w.AttachmentsDir(id))
	// Clean up scratch directories from runs created before TmpDir was recorded.
	_ = os.RemoveAll(w.Path(id, ".tmp"))
	// Last, once the record is gone.
	_ = w.removeLock(id)
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
