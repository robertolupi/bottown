package runs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgyPrepareMCP(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	l := Launch{Dir: dir, MCPURL: "http://127.0.0.1:3041/mcp", Key: "btk_x", RunID: "r-1"}
	if err := (agyHarness{}).PrepareMCP(&l, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if l.MCPConfig != filepath.Join(dir, AgyMCPPath) {
		t.Fatalf("config path %s", l.MCPConfig)
	}
	b, _ := os.ReadFile(l.MCPConfig)
	var cfg struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.MCPServers["bottown"].Headers["Authorization"] != "Bearer btk_x" {
		t.Fatalf("config %s (%v)", b, err)
	}
	if fi, _ := os.Stat(l.MCPConfig); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// A repository that tracks its own .agents/mcp_config.json is not overwritten.
	exec.Command("git", "-C", dir, "add", AgyMCPPath).Run()
	if err := (agyHarness{}).PrepareMCP(&l, t.TempDir()); err == nil || !strings.Contains(err.Error(), "tracks") {
		t.Fatalf("tracked config: %v", err)
	}
}

func TestAgyCommandKeepsPromptLast(t *testing.T) {
	c, _ := (agyHarness{}).Command(t.Context(), Launch{Dir: t.TempDir(), Prompt: "do it", RecordFile: "/x/r-1.json"})
	if c.Args[len(c.Args)-2] != "--print" || c.Args[len(c.Args)-1] != "do it" {
		t.Fatalf("args %v", c.Args)
	}
	c, _ = (agyHarness{}).Command(t.Context(), Launch{Dir: t.TempDir(), Prompt: "do it", Interactive: true})
	if strings.Join(c.Args[1:], " ") != "-i do it" {
		t.Fatalf("interactive args %v", c.Args)
	}
}

func TestAgySummary(t *testing.T) {
	log := `{"event":"init","conversation_id":"c"}
{"event":"step_update","step_update":{"text_delta":"partial"}}
{"event":"result","result":{"status":"SUCCESS","response":"Approved PR !4."}}
`
	if got := (agyHarness{}).Summary([]byte(log)); got != "Approved PR !4." {
		t.Fatalf("summary = %q", got)
	}
	if got := (agyHarness{}).Summary([]byte("plain text answer\n")); got != "plain text answer" {
		t.Fatalf("text summary = %q", got)
	}
}
