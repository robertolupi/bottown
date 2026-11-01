package runs

import (
	"os"
	"os/exec"
	"testing"
)

func TestLauncherAlive(t *testing.T) {
	if !(Record{PID: os.Getpid()}).LauncherAlive() {
		t.Error("own process reported dead")
	}
	if (Record{}).LauncherAlive() {
		t.Error("record without a launcher pid reported alive")
	}
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if (Record{PID: c.Process.Pid}).LauncherAlive() {
		t.Errorf("exited launcher %d reported alive", c.Process.Pid)
	}
}
