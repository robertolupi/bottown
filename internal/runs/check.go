package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// A flow's post-implement checks (bottown flow's e2e commands) are not a
// run, but the live tail shows them like one. While they run, the flow
// holds a flock on <id>.check, a CheckRecord in JSON, next to their log,
// <id>.log; the id is flow-<issue>-<sha>.e2e. The lock, not a pid, marks
// them live, so a check whose flow died is not "running", and the file
// that is left only goes stale.

// CheckRecordSuffix names a live check's record.
const CheckRecordSuffix = ".check"

// CheckRecord is what the live tail shows of a flow's running checks.
type CheckRecord struct {
	ID       string    `json:"id"` // flow-<issue>-<sha>.e2e: the log is <id>.log
	Repo     string    `json:"repo"`
	Issue    int64     `json:"issue"`
	PR       int64     `json:"pr"`
	SHA      string    `json:"sha"`
	Commands []string  `json:"commands"`
	Started  time.Time `json:"started"`
	Expires  time.Time `json:"expires,omitzero"` // the flow's --check-timeout, if any
}

// HoldCheck writes rec to <dir>/<rec.ID>.check and holds it locked, marking
// the check live, until release, which removes it. The record is written
// and locked under a temporary name and then renamed into place, so
// LiveChecks never sees it unlocked or half written. The descriptor is
// close-on-exec, so the check's commands do not inherit the lock.
func HoldCheck(dir string, rec CheckRecord) (release func(), err error) {
	path := filepath.Join(dir, rec.ID+CheckRecordSuffix)
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) {
		f.Close()
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(err)
	}
	if _, err := f.Write(b); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return func() {
		_ = os.Remove(path)
		_ = f.Close()
	}, nil
}

// LiveChecks lists the checks running in dir, oldest first. A record whose
// lock nobody holds is a check whose flow died: it is removed.
func LiveChecks(dir string) []CheckRecord {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []CheckRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), CheckRecordSuffix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !lockHeld(path) {
			_ = os.Remove(path)
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec CheckRecord
		if json.Unmarshal(b, &rec) != nil || rec.ID+CheckRecordSuffix != e.Name() {
			continue // not one of ours
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}
