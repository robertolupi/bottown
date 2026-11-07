package runs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHoldFlow(t *testing.T) {
	dir := t.TempDir()
	if _, held := FlowHolder(dir, "demo", 9); held {
		t.Fatal("no flow yet, but the lock is held")
	}
	release, err := HoldFlow(dir, "demo", 9)
	if err != nil {
		t.Fatal(err)
	}
	if pid, held := FlowHolder(dir, "demo", 9); !held || pid != os.Getpid() {
		t.Fatalf("holder = %d, %v", pid, held)
	}
	_, err = HoldFlow(dir, "demo", 9)
	var busy *FlowBusyError
	if !errors.As(err, &busy) || busy.PID != os.Getpid() || !strings.Contains(err.Error(), "demo#9") {
		t.Fatalf("second hold = %v", err)
	}
	// Another issue, or the same number in another repo, is its own flow.
	for _, other := range []struct {
		repo  string
		issue int64
	}{{"demo", 10}, {"other", 9}} {
		r, err := HoldFlow(dir, other.repo, other.issue)
		if err != nil {
			t.Fatalf("%s#%d: %v", other.repo, other.issue, err)
		}
		r()
	}
	release()
	if _, held := FlowHolder(dir, "demo", 9); held {
		t.Fatal("released, but the lock is held")
	}

	// A dead flow's pid is left in the file; the lock is not held.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FlowLockPath(dir, "demo", 9), fmt.Appendln(nil, dead.Process.Pid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, held := FlowHolder(dir, "demo", 9); held {
		t.Fatal("a dead pid's lock is held")
	}
	release, err = HoldFlow(dir, "demo", 9)
	if err != nil {
		t.Fatalf("taking over a stale lock: %v", err)
	}
	defer release()
	if pid, _ := FlowHolder(dir, "demo", 9); pid != os.Getpid() {
		t.Fatalf("holder after takeover = %d", pid)
	}
}
