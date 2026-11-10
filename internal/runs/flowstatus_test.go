package runs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func statusFixture(issue int64) FlowStatusView {
	return FlowStatusView{
		PID: os.Getpid(), Issue: issue, Implement: "claude", Verify: "agy:fast",
		Rounds: 3, Round: 2, Step: StepVerifying, Since: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
	}
}

func TestFlowStatusWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	if got := FlowStatus(dir, "demo", 9); got != (FlowStatusView{}) {
		t.Fatalf("a missing status = %+v, want zero", got)
	}
	st := statusFixture(9)
	if err := WriteFlowStatus(dir, "demo", 9, st); err != nil {
		t.Fatal(err)
	}
	if got := FlowStatus(dir, "demo", 9); got != st {
		t.Fatalf("status = %+v, want %+v", got, st)
	}
	// A reader sees the whole file: it is valid JSON of the status.
	b, err := os.ReadFile(FlowStatusPath(dir, "demo", 9))
	if err != nil {
		t.Fatal(err)
	}
	var back FlowStatusView
	if err := json.Unmarshal(b, &back); err != nil || back.Step != st.Step {
		t.Fatalf("the file is not the status: %v, %q", err, b)
	}
	// Rewriting replaces it, leaving no temporary file behind.
	st.Step, st.Detail = StepWaitingActor, "actor claude already has a live batch run: r-1"
	if err := WriteFlowStatus(dir, "demo", 9, st); err != nil {
		t.Fatal(err)
	}
	if got := FlowStatus(dir, "demo", 9); got != st {
		t.Fatalf("rewritten status = %+v, want %+v", got, st)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "flow-demo-9.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("runs dir holds %v, want only the status file", names)
	}
}

func TestRemoveFlowStatus(t *testing.T) {
	dir := t.TempDir()
	if err := RemoveFlowStatus(dir, "demo", 9); err != nil {
		t.Fatalf("removing a missing status: %v", err)
	}
	if err := WriteFlowStatus(dir, "demo", 9, statusFixture(9)); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFlowStatus(dir, "demo", 9); err != nil {
		t.Fatal(err)
	}
	if got := FlowStatus(dir, "demo", 9); got != (FlowStatusView{}) {
		t.Fatalf("status after removal = %+v, want zero", got)
	}
}

// TestHeldFlows covers the reader's view of the live flows: a held flow
// with and without a status file, a stale file whose lock is free (a flow
// that died), and a corrupt file, which survives as a zero view.
func TestHeldFlows(t *testing.T) {
	dir := t.TempDir()
	if got := HeldFlows(dir, "demo"); len(got) != 0 {
		t.Fatalf("HeldFlows = %+v, want none", got)
	}

	// A held flow with a status file.
	release9, err := HoldFlow(dir, "demo", 9)
	if err != nil {
		t.Fatal(err)
	}
	defer release9()
	st9 := statusFixture(9)
	st9.Step = StepChecks
	if err := WriteFlowStatus(dir, "demo", 9, st9); err != nil {
		t.Fatal(err)
	}

	// A held flow whose status file is missing.
	release11, err := HoldFlow(dir, "demo", 11)
	if err != nil {
		t.Fatal(err)
	}
	defer release11()

	// A held flow with a corrupt status file.
	release13, err := HoldFlow(dir, "demo", 13)
	if err != nil {
		t.Fatal(err)
	}
	defer release13()
	if err := os.WriteFile(FlowStatusPath(dir, "demo", 13), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A stale status file: its flow died, so its lock is free.
	if err := WriteFlowStatus(dir, "demo", 12, statusFixture(12)); err != nil {
		t.Fatal(err)
	}
	// Another repository's flow is not demo's, even with a lower issue.
	releaseOther, err := HoldFlow(dir, "other", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOther()
	if err := WriteFlowStatus(dir, "other", 1, statusFixture(1)); err != nil {
		t.Fatal(err)
	}

	got := HeldFlows(dir, "demo")
	if len(got) != 3 {
		t.Fatalf("HeldFlows = %+v, want issues 9, 11 and 13 only", got)
	}
	want := []FlowStatusView{
		st9,
		{Issue: 11, PID: os.Getpid()},
		{Issue: 13, PID: os.Getpid()},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("HeldFlows[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if other := HeldFlows(dir, "other"); len(other) != 1 || other[0].Issue != 1 || other[0].Step != StepVerifying {
		t.Errorf("another repo's held flow = %+v", other)
	}
}

// TestHeldFlowsIgnoresNonFlows: other files in the runs dir (run records,
// run logs, a flow's temporary file) are not flows.
func TestHeldFlowsIgnoresNonFlows(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"r-1.json", "flow-demo.json", "flow-demo-9.tmp", "flow-demo-.lock", "flow-demo-x.lock", ".flow-demo-9-123.tmp"} {
		if err := os.WriteFile(dir+"/"+name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := HeldFlows(dir, "demo"); len(got) != 0 {
		t.Fatalf("HeldFlows = %+v, want none", got)
	}
	if got := HeldFlows(t.TempDir()+"/missing", "demo"); got != nil {
		t.Fatalf("HeldFlows on a missing dir = %+v, want nil", got)
	}
	// A held lock whose name is not flow-<repo>-<issue>.lock is skipped.
	if _, err := HoldFlow(dir, "demo", 9); err != nil {
		t.Fatal(err)
	}
	if got := HeldFlows(dir, "demo"); len(got) != 1 || got[0].Issue != 9 {
		t.Fatalf("HeldFlows = %+v, want issue 9", got)
	}
}

// TestFlowStatusPath: the status file sits beside the lock, named for the
// repo and issue.
func TestFlowStatusPath(t *testing.T) {
	dir, path := t.TempDir(), ""
	path = FlowStatusPath(dir, "demo", 9)
	if !strings.HasSuffix(path, "/flow-demo-9.json") || !strings.HasPrefix(path, dir) {
		t.Fatalf("FlowStatusPath = %q", path)
	}
	if got := strings.TrimSuffix(FlowLockPath(dir, "demo", 9), ".lock") + ".json"; got != path {
		t.Fatalf("the status file %q is not beside the lock %q", path, FlowLockPath(dir, "demo", 9))
	}
}
