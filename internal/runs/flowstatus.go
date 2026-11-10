package runs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A flow writes a status file, flow-<repo>-<issue>.json, next to its lock
// while it lives, one step per change, so any reader can tell what it is
// doing, whoever started it. The file is a view for readers alone: the
// flow never reads it back, its state still comes from the forge and the
// run records, and a missing or stale file costs a reader the detail and
// nothing else. A status file whose lock is not held is stale (a flow
// that died) and ignored.

// The steps a flow reports, for FlowStatusView.Step.
const (
	StepImplementing = "implementing"
	StepChecks       = "checks"
	StepVerifying    = "verifying"
	StepWaitingActor = "waiting for actor"
	StepBetweenRuns  = "between runs"
)

// FlowStatusView is a flow's own account of what it is doing now, the
// status file's content. Implement and Verify are the ACTOR[:SPEC]
// strings the flow was given, so a reader sees the model spec too. Detail
// carries a wait's actor and the run it waits for, and is empty otherwise.
type FlowStatusView struct {
	PID       int       `json:"pid"`
	Issue     int64     `json:"issue"`
	Implement string    `json:"implement"`
	Verify    string    `json:"verify"`
	Rounds    int       `json:"rounds"`
	Round     int       `json:"round"`
	Step      string    `json:"step"`
	Detail    string    `json:"detail,omitempty"`
	Since     time.Time `json:"since"`
}

// FlowStatusPath is the status file of repo's issue's flow in dir, beside
// its lock.
func FlowStatusPath(dir, repo string, issue int64) string {
	return filepath.Join(dir, fmt.Sprintf("flow-%s-%d.json", repo, issue))
}

// WriteFlowStatus writes st at repo's issue's status path, atomically: a
// temporary file in the same directory, renamed over the target, so a
// reader sees the old file or the new one, never a partial write.
func WriteFlowStatus(dir, repo string, issue int64, st FlowStatusView) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, fmt.Sprintf(".flow-%s-%d-*.tmp", repo, issue))
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, FlowStatusPath(dir, repo, issue))
}

// RemoveFlowStatus removes repo's issue's status file in dir, if any. A
// flow calls it when it ends normally; a flow that died leaves its file
// for a reader to ignore as stale.
func RemoveFlowStatus(dir, repo string, issue int64) error {
	err := os.Remove(FlowStatusPath(dir, repo, issue))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// FlowStatus reads repo's issue's status file in dir, with a zero view
// when it is missing or unreadable. It does not look at the lock: a caller
// that needs only held flows uses HeldFlows.
func FlowStatus(dir, repo string, issue int64) FlowStatusView {
	return readFlowStatus(dir, repo, issue)
}

// readFlowStatus is FlowStatus's body, for HeldFlows to set the flow's
// identity from its lock after.
func readFlowStatus(dir, repo string, issue int64) FlowStatusView {
	b, err := os.ReadFile(FlowStatusPath(dir, repo, issue))
	if err != nil {
		return FlowStatusView{}
	}
	var st FlowStatusView
	if err := json.Unmarshal(b, &st); err != nil {
		return FlowStatusView{}
	}
	return st
}

// HeldFlows lists the status view of every flow whose lock is held in
// dir: the flows a reader can see live. A held flow with no status file,
// or with an unreadable one, still appears, a zero view carrying only the
// issue and the lock's pid; a status file whose lock is free is a flow
// that died and does not appear. The list is by issue.
func HeldFlows(dir, repo string) []FlowStatusView {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := fmt.Sprintf("flow-%s-", repo)
	var out []FlowStatusView
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || !strings.HasSuffix(rest, ".lock") {
			continue
		}
		issue, err := strconv.ParseInt(strings.TrimSuffix(rest, ".lock"), 10, 64)
		if err != nil {
			continue
		}
		pid, held := FlowHolder(dir, repo, issue)
		if !held {
			continue
		}
		st := readFlowStatus(dir, repo, issue)
		st.Issue = issue
		if st.PID == 0 {
			st.PID = pid
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Issue < out[j].Issue })
	return out
}
