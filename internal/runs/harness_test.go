package runs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/secrets"
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
	for _, want := range []string{"**discussant**", "#7 in `o/r`", "Do not\nimplement it", "/b comment --body-file -", "/b run end --summary", "detached at `abc`, the head\n  of `main`"} {
		if !strings.Contains(p, want) {
			t.Errorf("discuss prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestClaudeReadOnlyRun(t *testing.T) {
	allowedOf := func(l Launch) (string, string) {
		c, _ := (claudeHarness{}).Command(t.Context(), l)
		var allowed, mode string
		for i, a := range c.Args {
			switch a {
			case "--allowedTools":
				allowed = c.Args[i+1]
			case "--permission-mode":
				mode = c.Args[i+1]
			}
		}
		return allowed, mode
	}
	l := Launch{Dir: t.TempDir(), Prompt: "p", MCPConfig: "/m.json", RunID: "r-1", Key: "k", Allow: []string{"Bash(cargo:*)"}}
	if allowed, mode := allowedOf(l); !strings.Contains(allowed, ",Edit,") || mode != "acceptEdits" {
		t.Errorf("default run: allowed %s, mode %s", allowed, mode)
	}
	l.ReadOnly = true
	allowed, mode := allowedOf(l)
	for _, tool := range strings.Split(allowed, ",") {
		if tool == "Edit" || tool == "Write" {
			t.Errorf("read-only run pre-approves %s: %s", tool, allowed)
		}
	}
	if mode != "default" || !strings.Contains(allowed, "Read") || !strings.Contains(allowed, "Bash(cargo:*)") {
		t.Errorf("read-only run: allowed %s, mode %s", allowed, mode)
	}
}

func TestDiscussPromptQuestion(t *testing.T) {
	d := PromptData{RunID: "r-1", Actor: "claude", Repo: "o/r", Issue: 7, Title: "T", SHA: "abc", Base: "main", Bottown: "/b"}
	without, _ := Prompt("discuss", d)
	d.Question = "Should it say blah or foo?"
	with, err := Prompt("discuss", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(with, "## Question to discuss\n\nShould it say blah or foo?\n\n## How this run works") || !strings.Contains(with, "- Answer the question above.") || strings.Contains(with, "words after the number") {
		t.Errorf("prompt with a question:\n%s", with)
	}
	if strings.Contains(without, "Question to discuss") || !strings.Contains(without, "words after the number") {
		t.Errorf("prompt without a question:\n%s", without)
	}
}

func TestKimiPrepareMCP(t *testing.T) {
	dir, runsDir, home := t.TempDir(), t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Setenv("KIMI_CODE_HOME", home)
	os.WriteFile(filepath.Join(home, "mcp.json"), []byte(`{"mcpServers": {
		"local": {"command": "/bin/srv", "args": ["--token", "s3cret"], "env": {"K": "s3cret"}},
		"remote": {"url": "https://mcp.example", "headers": {"Authorization": "s3cret"}},
		"bottown": {"url": "https://stale.example"}}}`), 0o600)
	l := Launch{Dir: dir, MCPURL: "http://127.0.0.1:3041/mcp", Key: "btk_x", RunID: "r-1", RecordFile: filepath.Join(runsDir, "r-1.json")}
	if err := (kimiHarness{}).PrepareMCP(&l, runsDir); err != nil {
		t.Fatal(err)
	}
	if l.MCPConfig != filepath.Join(dir, KimiMCPPath) {
		t.Fatalf("config path %s", l.MCPConfig)
	}
	b, _ := os.ReadFile(l.MCPConfig)
	if strings.Contains(string(b), "btk_x") || strings.Contains(string(b), "s3cret") {
		t.Errorf("config holds a secret:\n%s", b)
	}
	var cfg struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	bt := cfg.MCPServers["bottown"]
	if bt["url"] != l.MCPURL || bt["bearerTokenEnvVar"] != "BOTTOWN_RUN_KEY" || bt["enabled"] != nil {
		t.Errorf("bottown entry %v", bt)
	}
	if s := cfg.MCPServers["local"]; s["enabled"] != false || s["command"] != "/bin/srv" {
		t.Errorf("local entry %v", s)
	}
	if s := cfg.MCPServers["remote"]; s["enabled"] != false || s["url"] != "https://mcp.example" {
		t.Errorf("remote entry %v", s)
	}
	if fi, _ := os.Stat(l.MCPConfig); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if fileExists(kimiAgentFile(l)) {
		t.Error("a writing run got the read-only agent file")
	}

	l.ReadOnly = true
	if err := (kimiHarness{}).PrepareMCP(&l, runsDir); err != nil {
		t.Fatal(err)
	}
	if a, _ := os.ReadFile(kimiAgentFile(l)); !strings.Contains(string(a), "  - Write\n  - Edit\n") || !strings.Contains(string(a), "${base_prompt}") {
		t.Errorf("read-only agent file:\n%s", a)
	}

	exec.Command("git", "-C", dir, "add", KimiMCPPath).Run()
	if err := (kimiHarness{}).PrepareMCP(&l, runsDir); err == nil || !strings.Contains(err.Error(), "tracks") {
		t.Fatalf("tracked config: %v", err)
	}
}

func TestKimiCommand(t *testing.T) {
	l := Launch{Dir: t.TempDir(), Prompt: "do it", RecordFile: "/x/r-1.json", Key: "k"}
	c, err := (kimiHarness{}).Command(t.Context(), l)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Args[1:], " "); got != "--output-format stream-json -p do it" {
		t.Errorf("args %q", got)
	}
	env := strings.Join(c.Env, "\n")
	for _, want := range []string{"KIMI_CODE_TRUST_WORKSPACE=1", "BOTTOWN_RUN_KEY=k", "KIMI_CODE_BACKGROUND_PRINT_BACKGROUND_MODE=drain"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	l.ReadOnly = true
	c, _ = (kimiHarness{}).Command(t.Context(), l)
	if got := strings.Join(c.Args[1:], " "); got != "--output-format stream-json --agent-file /x/r-1.kimi-agent.md -p do it" {
		t.Errorf("read-only args %q", got)
	}
	l.Interactive = true
	if _, err := (kimiHarness{}).Command(t.Context(), l); err == nil || !strings.Contains(err.Error(), "/skill:bottown-") {
		t.Errorf("interactive: %v", err)
	}
}

// The log of a real kimi 2.3.0 run (stream-json on stdout).
func TestKimiSummary(t *testing.T) {
	log := `{"role":"meta","type":"system.version","version":"2.3.0"}
{"role":"assistant","tool_calls":[{"type":"function","id":"tool_1","function":{"name":"Bash","arguments":"{\"command\":\"echo hi\"}"}}]}
{"role":"tool","tool_call_id":"tool_1","content":"hi\n"}
• thinking goes to stderr, which shares the log
{"role":"assistant","content":"DONE"}
{"role":"meta","type":"session.resume_hint","session_id":"session_1","command":"kimi -r session_1","content":"To resume this session: kimi -r session_1"}
`
	if got := (kimiHarness{}).Summary([]byte(log)); got != "DONE" {
		t.Fatalf("summary = %q", got)
	}
	parts := `{"role":"assistant","content":[{"type":"text","text":"Opened "},{"type":"text","text":"PR !4."}]}`
	if got := (kimiHarness{}).Summary([]byte(parts)); got != "Opened PR !4." {
		t.Fatalf("parts summary = %q", got)
	}
}

// TestHarnessDigests reads real batch logs, one per harness (init events,
// which hold only local setup, removed).
func TestHarnessDigests(t *testing.T) {
	i64 := func(n int64) *int64 { return &n }
	cost := 0.0491618
	for _, tc := range []struct {
		h       Harness
		file    string
		summary string
		usage   *secrets.Usage
	}{
		// Tokens summed over both models the run used.
		{claudeHarness{}, "claude-2.1.316.stream.jsonl", "hello", &secrets.Usage{
			InputTokens: i64(1174), OutputTokens: i64(20), CacheReadTokens: i64(11383), CacheWriteTokens: i64(5834),
			CostUSD: &cost, Turns: i64(1), DurationMS: i64(2752),
		}},
		// No cost, and no cache writes.
		{agyHarness{}, "agy-1.4.12.stream.jsonl", "hello", &secrets.Usage{
			InputTokens: i64(15775), OutputTokens: i64(5), CacheReadTokens: i64(0), Turns: i64(1), DurationMS: i64(4991),
		}},
		{kimiHarness{}, "kimi-2.4.1.stream.jsonl", "hello", nil},
		{codexHarness{}, "codex-0.166.0.stream.jsonl", "hello", &secrets.Usage{
			InputTokens: i64(186), OutputTokens: i64(5), CacheReadTokens: i64(17536), CacheWriteTokens: i64(0),
			Turns: i64(1),
		}},
		{crushHarness{}, "crush-0.99.0.log", "hello", nil},
	} {
		log, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(tc.h.Summary(log)); got != tc.summary {
			t.Errorf("%s: summary %q", tc.file, got)
		}
		if got := tc.h.Usage(log); !reflect.DeepEqual(got, tc.usage) {
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(tc.usage)
			t.Errorf("%s: usage\n got %s\nwant %s", tc.file, g, w)
		}
	}
	// What a result event leaves out stays unknown.
	u := (claudeHarness{}).Usage([]byte(`{"type":"result","num_turns":3,"usage":{"input_tokens":100,"output_tokens":7}}`))
	if u == nil || *u.InputTokens != 100 || u.CacheReadTokens != nil || u.CacheWriteTokens != nil || u.CostUSD != nil {
		t.Errorf("partial claude usage: %+v", u)
	}
	// No result event: unknown, not zero.
	if u := (claudeHarness{}).Usage([]byte("{\"type\":\"assistant\"}\ncrashed\n")); u != nil {
		t.Errorf("claude without a result: %+v", u)
	}
	if u := (agyHarness{}).Usage([]byte("plain text\n")); u != nil {
		t.Errorf("agy without a result: %+v", u)
	}
	if u := (codexHarness{}).Usage([]byte("plain text\n")); u != nil {
		t.Errorf("codex without a result: %+v", u)
	}
}

func TestCodexPrepareMCP(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Setenv("CODEX_HOME", home)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte(`[mcp_servers.botfam]
url = "https://stale.example"
[mcp_servers."special.server"]
url = "https://stale.example"
[mcp_servers.bottown]
url = "https://stale.example"
`), 0o600)
	l := Launch{Dir: dir, MCPURL: "http://127.0.0.1:3041/mcp", Key: "btk_x", RunID: "r-1"}
	if err := (codexHarness{}).PrepareMCP(&l, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if l.MCPConfig != filepath.Join(dir, CodexMCPPath) {
		t.Fatalf("config path %s", l.MCPConfig)
	}
	b, _ := os.ReadFile(l.MCPConfig)
	s := string(b)
	if strings.Contains(s, "btk_x") {
		t.Errorf("config holds key: %s", s)
	}
	for _, want := range []string{
		`[mcp_servers.bottown]`,
		`url = "http://127.0.0.1:3041/mcp"`,
		`bearer_token_env_var = "BOTTOWN_RUN_KEY"`,
		`[mcp_servers.botfam]`,
		`enabled = false`,
		`[mcp_servers."special.server"]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("config lacks %q:\n%s", want, s)
		}
	}
	if fi, _ := os.Stat(l.MCPConfig); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	exec.Command("git", "-C", dir, "add", CodexMCPPath).Run()
	if err := (codexHarness{}).PrepareMCP(&l, t.TempDir()); err == nil || !strings.Contains(err.Error(), "tracks") {
		t.Fatalf("tracked config: %v", err)
	}
}

func TestCodexCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte(`[mcp_servers.botfam]
url = "https://stale.example"
[mcp_servers."special.server"]
url = "https://stale.example"
`), 0o600)

	l := Launch{Dir: t.TempDir(), Prompt: "do it", MCPURL: "http://127.0.0.1:3041/mcp", Key: "k"}
	c, err := (codexHarness{}).Command(t.Context(), l)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(c.Args[1:], " ")
	want := `exec --dangerously-bypass-approvals-and-sandbox --json --ephemeral --ignore-user-config -c mcp_servers.bottown.url="http://127.0.0.1:3041/mcp" -c mcp_servers.bottown.bearer_token_env_var="BOTTOWN_RUN_KEY" do it`
	if got != want {
		t.Errorf("batch args:\n got %s\nwant %s", got, want)
	}

	l.ReadOnly = true
	c, _ = (codexHarness{}).Command(t.Context(), l)
	got = strings.Join(c.Args[1:], " ")
	if got != want {
		t.Errorf("read-only args:\n got %s\nwant %s", got, want)
	}

	l.Interactive = true
	c, _ = (codexHarness{}).Command(t.Context(), l)
	got = strings.Join(c.Args[1:], " ")
	want = `-s danger-full-access -c mcp_servers.bottown.url="http://127.0.0.1:3041/mcp" -c mcp_servers.bottown.bearer_token_env_var="BOTTOWN_RUN_KEY" -c mcp_servers.botfam.enabled=false -c mcp_servers."special.server".enabled=false do it`
	if got != want {
		t.Errorf("interactive args:\n got %s\nwant %s", got, want)
	}
}

func TestCodexSummary(t *testing.T) {
	log := `{"type":"thread.started","thread_id":"1"}
{"type":"item.completed","item":{"type":"agent_message","text":"first message"}}
{"type":"item.completed","item":{"type":"command_execution","command":"echo hi"}}
{"type":"item.completed","item":{"type":"agent_message","text":"final message"}}
{"type":"turn.completed"}
`
	if got := (codexHarness{}).Summary([]byte(log)); got != "final message" {
		t.Fatalf("summary = %q", got)
	}
	if got := (codexHarness{}).Summary([]byte("plain text fallback\n")); got != "plain text fallback" {
		t.Fatalf("fallback summary = %q", got)
	}
}

func TestCrushPrepareMCP(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	l := Launch{Dir: dir, MCPURL: "http://127.0.0.1:3041/mcp", Key: "btk_x", RunID: "r-1"}
	if err := (crushHarness{}).PrepareMCP(&l, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if l.MCPConfig != filepath.Join(dir, CrushMCPPath) {
		t.Fatalf("config path %s", l.MCPConfig)
	}
	b, err := os.ReadFile(l.MCPConfig)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCP map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	headers, _ := cfg.MCP["bottown"]["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer $BOTTOWN_RUN_KEY" {
		t.Errorf("headers %v", headers)
	}
	if strings.Contains(string(b), "btk_x") {
		t.Error("config contains raw token")
	}

	exec.Command("git", "-C", dir, "add", CrushMCPPath).Run()
	if err := (crushHarness{}).PrepareMCP(&l, t.TempDir()); err == nil || !strings.Contains(err.Error(), "tracks") {
		t.Fatalf("tracked config: %v", err)
	}
}

func TestCrushCommand(t *testing.T) {
	l := Launch{Dir: t.TempDir(), Prompt: "do it", Key: "k"}
	c, err := (crushHarness{}).Command(t.Context(), l)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Args[1:], " "); got != "run --quiet do it" {
		t.Errorf("args %q", got)
	}
	env := strings.Join(c.Env, "\n")
	if !strings.Contains(env, "BOTTOWN_RUN_KEY=k") {
		t.Errorf("env lacks BOTTOWN_RUN_KEY")
	}

	l.Interactive = true
	if _, err := (crushHarness{}).Command(t.Context(), l); err == nil || !strings.Contains(err.Error(), "/bottown-") {
		t.Errorf("interactive: %v", err)
	}
}

func TestCrushSummary(t *testing.T) {
	if got := (crushHarness{}).Summary([]byte("All checks passed.\n")); got != "All checks passed." {
		t.Fatalf("summary = %q", got)
	}
}

func TestCrushUsage(t *testing.T) {
	if got := (crushHarness{}).Usage([]byte("hello")); got != nil {
		t.Fatalf("usage = %v, want nil", got)
	}
}
