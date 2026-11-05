package runs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Store is one run record file, wherever it lives: <runs>/<id>.json for a
// launched run (Workspace.Store), <gitdir>/bottown/run.json for an actor
// worktree's lease. Every write holds an exclusive flock on the lock file
// next to it (the path with ".json" replaced by ".lock") and replaces the
// file atomically, so readers never see a partial record.
type Store struct{ Path string }

// LockPath is the file every Create, Save and Update locks in turn.
func (s Store) LockPath() string { return strings.TrimSuffix(s.Path, ".json") + ".lock" }

// Create writes a new record with 0600; it is os.ErrExist if there is one.
// Existing records must be changed with Update or replaced with Save.
func (s Store) Create(r Record) error {
	return s.withLock(func() error {
		if _, err := os.Stat(s.Path); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return os.ErrExist
		}
		return s.write(r)
	})
}

// Save writes r with 0600, replacing any record there: an actor worktree's
// run.json is the worktree's latest run, whichever that is.
func (s Store) Save(r Record) error {
	return s.withLock(func() error { return s.write(r) })
}

// Update serializes a read-modify-write across processes. The callback must
// not call Create, Save or Update for this record. An error leaves the
// record unchanged.
func (s Store) Update(change func(*Record) error) error {
	return s.withLock(func() error {
		r, err := s.Load()
		if err != nil {
			return err
		}
		if err := change(&r); err != nil {
			return err
		}
		return s.write(r)
	})
}

// Load reads the record.
func (s Store) Load() (Record, error) { return LoadFile(s.Path) }

func (s Store) withLock(fn func() error) error {
	f, err := lockFile(s.LockPath(), true)
	if err != nil {
		return err
	}
	defer f.Close()
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s Store) write(r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), filepath.Base(s.Path)+".tmp-*")
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
	return os.Rename(f.Name(), s.Path)
}
