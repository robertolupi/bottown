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

func TestClaudeIsolationAndEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "desktop")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s")
	t.Setenv("ANTHROPIC_BASE_URL", "http://host-proxy")
	t.Setenv("GH_TOKEN", "x")
	t.Setenv("KEEP_ME", "1")
	c, _ := (claudeHarness{}).Command(t.Context(), Launch{Dir: t.TempDir(), Prompt: "p", MCPConfig: "/m.json", RunID: "r-1", Key: "k"})
	args := strings.Join(c.Args, " ")
	for _, want := range []string{"--setting-sources project,local", `"autoMemoryEnabled": false`, "--strict-mcp-config", "--mcp-config /m.json", "--no-session-persistence"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	env := strings.Join(c.Env, "\n")
	for _, gone := range []string{"CLAUDE_CODE_ENTRYPOINT=", "CLAUDE_CODE_SESSION_ID=", "ANTHROPIC_BASE_URL=", "GH_TOKEN="} {
		if strings.Contains(env, gone) {
			t.Errorf("env still has %s", gone)
		}
	}
	if !strings.Contains(env, "KEEP_ME=1") || !strings.Contains(env, "BOTTOWN_RUN_KEY=k") {
		t.Errorf("env lost a variable it should keep")
	}
}

// Implementer and verifier must mean the same checks: both prompts point at
// where the repository defines them.
func TestPromptsNameWhereChecksAre(t *testing.T) {
	for _, kind := range []string{"implement", "verify"} {
		p, err := Prompt(kind, PromptData{RunID: "r-1", Actor: "claude", Repo: "o/r", Issue: 1, PR: 2, SHA: "abc"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p, "repository's checks") || !strings.Contains(p, "AGENTS.md (or README)") {
			t.Errorf("%s prompt does not say where the checks are:\n%s", kind, p)
		}
	}
}

func TestDiscussPrompt(t *testing.T) {
	p, err := Prompt("discuss", PromptData{RunID: "r-1", Actor: "claude", Family: "anthropic", Repo: "o/r", Issue: 7, Title: "T", SHA: "abc", Base: "main", Bottown: "/b", SelfEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"**discussant**", "#7 in `o/r`", "Do not\nimplement it", "/b issue comment --body-file -", "/b run end --summary", "detached at `abc`, the head\n  of `main`"} {
		if !strings.Contains(p, want) {
			t.Errorf("discuss prompt lacks %q:\n%s", want, p)
		}
	}
}
