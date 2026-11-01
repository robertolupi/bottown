package cli

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
)

func TestStoppedHandover(t *testing.T) {
	rec := runs.Record{ID: "r-stop", Kind: policy.Discuss, Actor: "claude", Family: "anthropic", SHA: "abc123", StopRequested: true}
	got := handoverText(rec, runs.StatusFailed, 0, "harness done")
	for _, want := range []string{"**failed**", "Stopped by the owner.", "harness done"} {
		if !strings.Contains(got, want) {
			t.Errorf("handover lacks %q:\n%s", want, got)
		}
	}
}

// startHarness starts a shell that spawns a long child, the way a harness
// spawns MCP servers, and returns the child's pid once it is running. The
// trap is set after the fork, so the child cannot inherit it.
func startHarness(t *testing.T, group bool) (*exec.Cmd, int) {
	t.Helper()
	c := exec.Command("sh", "-c", `sleep 300 & trap 'exit 0' TERM; echo $!; while :; do sleep 0.1; done`)
	if group {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(child, syscall.SIGKILL)
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	return c, child
}

func gone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestSignalHarnessGroup(t *testing.T) {
	c, child := startHarness(t, true)
	if err := signalHarness(c.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := c.Wait(); err != nil {
		t.Errorf("harness did not exit cleanly on TERM: %v", err)
	}
	if !gone(child) {
		t.Errorf("harness child %d survived the stop", child)
	}
}

func TestSignalHarnessNoGroup(t *testing.T) {
	c, child := startHarness(t, false)
	if err := signalHarness(c.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := c.Wait(); err != nil {
		t.Errorf("harness did not exit cleanly on TERM: %v", err)
	}
	if syscall.Kill(child, 0) != nil {
		t.Errorf("child %d outside a harness group was signalled", child)
	}
}
