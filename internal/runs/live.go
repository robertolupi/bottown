package runs

import (
	"errors"
	"os"
	"syscall"
)

// LauncherLockSuffix names the file a run's launcher holds locked while it
// lives (see HoldLauncher). It is not the record's ".lock", which every
// Save and Update takes in turn.
const LauncherLockSuffix = ".live"

// HoldLauncher takes the lock that marks run id's launcher alive: an
// exclusive flock on <id>.live, held until release is called or this
// process exits, when the kernel drops it. It returns the file's path,
// for the record's LauncherLock. The descriptor is close-on-exec, so the
// harness and its tools do not inherit the lock and keep a dead launcher
// "alive".
func (w *Workspace) HoldLauncher(id string) (path string, release func(), err error) {
	path = w.Path(id, LauncherLockSuffix)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return "", nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return "", nil, err
	}
	return path, func() { _ = f.Close() }, nil
}

// LauncherAlive reports whether the process that launched the run, and
// owns its record, is still running. A run that records its launcher's
// lock (see HoldLauncher) is alive exactly while that lock is held, so a
// pid reused after the launcher died does not keep the run "running". An
// older record has only its pid to go by.
func (r Record) LauncherAlive() bool {
	if !ProcessAlive(r.PID) {
		return false
	}
	if r.LauncherLock == "" {
		return true
	}
	return lockHeld(r.LauncherLock)
}

// lockHeld reports whether some process holds a flock on path. A missing
// file is a run whose files were removed, so not held; any other error
// counts as held, the safe answer for gc and run stop.
func lockHeld(path string) bool {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return true
}

// ProcessAlive reports whether a process with pid exists. EPERM means it
// exists but belongs to someone else: alive.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
