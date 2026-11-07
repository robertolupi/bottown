package runs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// A flow holds an exclusive flock on <runs>/flow-<repo>-<issue>.lock, with
// its pid written in it, for its whole life, so two supervisors never drive
// one issue at once. The kernel drops the lock when the flow exits, however
// it ends: a lock file whose holder died (its pid dead) is not held, and the
// next flow takes it over. The file is never removed, so every flow locks
// the same inode.

// FlowLockPath is the lock file of repo's issue's flow in the runs dir.
func FlowLockPath(dir, repo string, issue int64) string {
	return filepath.Join(dir, fmt.Sprintf("flow-%s-%d.lock", repo, issue))
}

// FlowBusyError is the refusal of a flow whose issue another flow holds.
type FlowBusyError struct {
	Repo  string
	Issue int64
	PID   int // 0 when the holder has not written it yet
}

func (e *FlowBusyError) Error() string {
	if e.PID <= 0 {
		return fmt.Sprintf("another flow is already driving %s#%d; wait for it to end", e.Repo, e.Issue)
	}
	return fmt.Sprintf("a flow is already driving %s#%d (pid %d); stop it with `kill %d` (or Ctrl-C in its terminal), or wait for it to end", e.Repo, e.Issue, e.PID, e.PID)
}

// HoldFlow takes the lock of repo's issue's flow in dir, until release is
// called or this process exits. It fails with *FlowBusyError when another
// live process holds it. The descriptor is close-on-exec, so the runs and
// checks the flow starts do not inherit the lock.
func HoldFlow(dir, repo string, issue int64) (release func(), err error) {
	path := FlowLockPath(dir, repo, issue)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pid, _ := readFlowPID(path)
			return nil, &FlowBusyError{Repo: repo, Issue: issue, PID: pid}
		}
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = f.Truncate(0)
		_ = f.Close()
	}, nil
}

// FlowHolder reports the pid of the live flow holding repo's issue's lock
// in dir, if one does (pid 0 when it has not written it yet).
func FlowHolder(dir, repo string, issue int64) (pid int, held bool) {
	path := FlowLockPath(dir, repo, issue)
	if !lockHeld(path) {
		return 0, false
	}
	pid, _ = readFlowPID(path)
	return pid, true
}

func readFlowPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}
