package runs

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/robertolupi/bottown/internal/secrets"
)

//go:embed prompts/*.md
var promptFS embed.FS

var prompts = template.Must(template.ParseFS(promptFS, "prompts/*.md"))

// PromptData fills the implement and verify templates.
type PromptData struct {
	RunID, Actor, Family, Repo string
	Issue, PR                  int64
	Title, Body                string
	Branch, Base, SHA, Author  string
	AllowSameFamily            bool
	Bottown                    string // absolute path of the bottown CLI
	Remote                     string // the bottown remote in the worktree
	SelfEnd                    bool   // the agent ends the run itself (worktree runs)
	Question                   string // discuss: the owner's question, if given to the command
}

// Prompt renders the goal prompt for kind ("implement" or "verify").
func Prompt(kind string, d PromptData) (string, error) {
	var b bytes.Buffer
	if err := prompts.ExecuteTemplate(&b, kind+".md", d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Launch is everything a harness needs to start a run.
type Launch struct {
	Dir         string // worktree, the agent's cwd
	Prompt      string
	PromptFile  string
	MCPConfig   string // path to the MCP config file
	MCPURL      string
	Key         string
	RecordFile  string
	RunID       string
	Interactive bool
	Allow       []string // extra allowed tools (claude syntax)
	// ReadOnly runs (discuss) must not change the worktree: no file-editing
	// tools are pre-approved, and edits are not auto-accepted.
	ReadOnly bool
}

// Harness starts an agent CLI for a run.
type Harness interface {
	// PrepareMCP writes the harness's MCP config for the run and sets
	// l.MCPConfig to its path; the launcher deletes that file when the run
	// ends.
	PrepareMCP(l *Launch, filesDir string) error
	Command(ctx context.Context, l Launch) (*exec.Cmd, error)
	// Summary extracts the agent's final message from a batch log, for the
	// handover comment. Empty if unknown.
	Summary(log []byte) string
	// Usage extracts what the harness reported the run used from a batch
	// log, for the run's digest. Nil if it reports nothing; fields it does
	// not report stay nil.
	Usage(log []byte) *secrets.Usage
}

// HarnessCmdEnv names a testing hook: when set, every run executes this
// shell command instead of the actor's harness.
const HarnessCmdEnv = "BOTTOWN_HARNESS_CMD"

// LookupHarness returns the binding for a harness name.
func LookupHarness(name string) (Harness, error) {
	if cmd := os.Getenv(HarnessCmdEnv); cmd != "" {
		return scriptHarness{cmd: cmd}, nil
	}
	switch name {
	case "claude":
		return claudeHarness{}, nil
	case "antigravity":
		return agyHarness{}, nil
	case "kimi":
		return kimiHarness{}, nil
	case "codex":
		return codexHarness{}, nil
	}
	return nil, fmt.Errorf("no harness binding for %q yet (available: claude, antigravity, kimi, codex)", name)
}

// RunEnv is the environment of a harness process: the user's, minus forge
// credentials an agent could fall back to, plus the run's coordinates.
func RunEnv(l Launch) []string {
	var env []string
	// Launched from inside a Claude Code session, the environment carries
	// that host's session and routing variables; a run must not inherit
	// them (it would talk to the host's proxy, or think it is a child).
	insideClaude := os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" || os.Getenv("CLAUDECODE") != ""
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch {
		case k == "GH_TOKEN", k == "GITHUB_TOKEN", k == "GITEA_TOKEN", k == "FORGEJO_TOKEN",
			k == "BOTTOWN_INSTANCE", k == HarnessCmdEnv:
			continue
		case strings.HasPrefix(k, "CLAUDE_CODE_"), k == "CLAUDECODE":
			continue
		case insideClaude && (k == "ANTHROPIC_BASE_URL" || k == "USE_STAGING_OAUTH" || k == "USE_LOCAL_OAUTH" || strings.HasPrefix(k, "MCP_")):
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"BOTTOWN_RUN_ID="+l.RunID,
		"BOTTOWN_RUN_FILE="+l.RecordFile,
		"BOTTOWN_MCP_URL="+l.MCPURL,
		"BOTTOWN_RUN_KEY="+l.Key,
		"BOTTOWN_PROMPT_FILE="+l.PromptFile,
	)
}

// defaultAllowed are the tools a batch claude run may use without asking:
// bottown's MCP tools, file tools, and common build/test/git commands.
var defaultAllowed = []string{
	"mcp__bottown", "Bash(bottown:*)", "Read", "Edit", "Write", "Glob", "Grep", "TodoWrite",
	"Bash(git:*)", "Bash(go:*)", "Bash(gofmt:*)", "Bash(make:*)",
	"Bash(npm:*)", "Bash(npx:*)", "Bash(cargo:*)", "Bash(python3:*)",
	"Bash(ls:*)", "Bash(cat:*)", "Bash(grep:*)", "Bash(rg:*)", "Bash(find:*)",
	"Bash(sed:*)", "Bash(head:*)", "Bash(tail:*)", "Bash(wc:*)", "Bash(diff:*)",
}

type claudeHarness struct{}

// PrepareMCP writes the config next to the worktree (--mcp-config).
func (claudeHarness) PrepareMCP(l *Launch, filesDir string) error {
	return writeMCP(l, filepath.Join(filesDir, l.RunID+".mcp.json"), "type")
}

// writeMCP writes an mcpServers config with bottown over HTTP and the run
// key in a header. typeKey is "type" (claude: "type": "http") or "" (none).
func writeMCP(l *Launch, path, typeKey string) error {
	srv := map[string]any{
		"url":     l.MCPURL,
		"headers": map[string]string{"Authorization": "Bearer " + l.Key},
	}
	if typeKey != "" {
		srv[typeKey] = "http"
	}
	b, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"bottown": srv}}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	l.MCPConfig = path
	return nil
}

// claudeIsolation keeps a run independent of the owner's personal Claude
// setup: user-level settings (and so the owner's plugins, hooks and
// permissions) are not loaded, auto-memory is off, and only bottown's MCP
// server is configured. The repository's own CLAUDE.md and project settings
// still apply. (--bare would isolate more but cannot use subscription
// OAuth; --safe-mode drops the repository's CLAUDE.md.)
var claudeIsolation = []string{
	"--setting-sources", "project,local",
	"--settings", `{"autoMemoryEnabled": false}`,
	"--strict-mcp-config",
}

func (claudeHarness) Command(ctx context.Context, l Launch) (*exec.Cmd, error) {
	allowed := append([]string{}, defaultAllowed...)
	mode := "acceptEdits"
	if l.ReadOnly {
		allowed = slices.DeleteFunc(allowed, func(t string) bool { return t == "Edit" || t == "Write" })
		mode = "default"
	}
	if self, err := os.Executable(); err == nil {
		allowed = append(allowed, "Bash("+self+":*)") // the agent CLI, by absolute path
	}
	args := append([]string{"--mcp-config", l.MCPConfig}, claudeIsolation...)
	args = append(args,
		"--permission-mode", mode,
		"--allowedTools", strings.Join(append(allowed, l.Allow...), ","),
		"-n", "bottown "+l.RunID,
	)
	if !l.Interactive {
		// The run log keeps the transcript; don't add batch sessions to the
		// owner's session list.
		args = append(args, "-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence")
	}
	args = append(args, l.Prompt)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir, cmd.Env = l.Dir, RunEnv(l)
	return cmd, nil
}

// Summary returns the "result" of the last stream-json result event.
func (claudeHarness) Summary(log []byte) string {
	var last string
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type   string `json:"type"`
			Result string `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Type == "result" && ev.Result != "" {
			last = ev.Result
		}
	}
	return last
}

// Usage reads the last stream-json result event: its turns, duration and
// total cost, and tokens summed over its per-model usage (which counts
// helper models too), else its main usage.
func (claudeHarness) Usage(log []byte) *secrets.Usage {
	type tokens struct {
		Input       *int64 `json:"input_tokens"`
		Output      *int64 `json:"output_tokens"`
		CacheRead   *int64 `json:"cache_read_input_tokens"`
		CacheCreate *int64 `json:"cache_creation_input_tokens"`
	}
	type model struct {
		Input       *int64 `json:"inputTokens"`
		Output      *int64 `json:"outputTokens"`
		CacheRead   *int64 `json:"cacheReadInputTokens"`
		CacheCreate *int64 `json:"cacheCreationInputTokens"`
	}
	var last *secrets.Usage
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Type       string           `json:"type"`
			NumTurns   *int64           `json:"num_turns"`
			DurationMS *int64           `json:"duration_ms"`
			CostUSD    *float64         `json:"total_cost_usd"`
			Usage      *tokens          `json:"usage"`
			ModelUsage map[string]model `json:"modelUsage"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Type != "result" {
			return
		}
		u := &secrets.Usage{Turns: ev.NumTurns, DurationMS: ev.DurationMS, CostUSD: ev.CostUSD}
		if len(ev.ModelUsage) > 0 {
			var t tokens
			for _, m := range ev.ModelUsage {
				addTokens(&t.Input, m.Input)
				addTokens(&t.Output, m.Output)
				addTokens(&t.CacheRead, m.CacheRead)
				addTokens(&t.CacheCreate, m.CacheCreate)
			}
			ev.Usage = &t
		}
		if t := ev.Usage; t != nil {
			u.InputTokens, u.OutputTokens = t.Input, t.Output
			u.CacheReadTokens, u.CacheWriteTokens = t.CacheRead, t.CacheCreate
		}
		last = u
	})
	return last
}

// addTokens adds n to *sum, if n is known; *sum stays nil until one is.
func addTokens(sum **int64, n *int64) {
	if n == nil {
		return
	}
	if *sum == nil {
		*sum = new(int64)
	}
	**sum += *n
}

// eachJSONLine calls fn with each line of log, which may be long.
func eachJSONLine(log []byte, fn func([]byte)) {
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		fn(sc.Bytes())
	}
}

// agyHarness runs Antigravity's CLI. Its only per-run MCP config is the
// workspace file <worktree>/.agents/mcp_config.json, which the launcher
// excludes from git (in the mirror's info/exclude) and deletes at the end.
type agyHarness struct{}

// AgyMCPPath is the workspace MCP config path inside a worktree.
const AgyMCPPath = ".agents/mcp_config.json"

func (agyHarness) PrepareMCP(l *Launch, _ string) error {
	if out, err := Git(l.Dir, nil, "ls-files", "--", AgyMCPPath); err == nil && out != "" {
		return fmt.Errorf("the repository tracks %s; bottown will not overwrite it for an agy run", AgyMCPPath)
	}
	return writeMCP(l, filepath.Join(l.Dir, AgyMCPPath), "")
}

func (agyHarness) Command(ctx context.Context, l Launch) (*exec.Cmd, error) {
	var args []string
	if l.Interactive {
		// The owner approves tool use in the TUI.
		args = []string{"-i", l.Prompt}
	} else {
		// agy has no allowlist tier: unattended runs must skip prompts. Forge
		// access is still bounded by the run key.
		args = []string{
			"--dangerously-skip-permissions",
			"--log-file", strings.TrimSuffix(l.RecordFile, ".json") + ".agy.log",
			"--output-format", "stream-json",
			"--print", l.Prompt, // --print takes the next argument: keep it last
		}
	}
	cmd := exec.CommandContext(ctx, "agy", args...)
	cmd.Dir, cmd.Env = l.Dir, RunEnv(l)
	return cmd, nil
}

// Summary takes the response of agy's final stream-json event
// ({"event":"result","result":{"response":...}}), else the last non-JSON
// output line.
func (agyHarness) Summary(log []byte) string {
	var last string
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Event  string `json:"event"`
			Result struct {
				Response string `json:"response"`
			} `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Event == "result" && ev.Result.Response != "" {
			last = ev.Result.Response
		}
	}
	if last != "" {
		return last
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "{") {
			return l
		}
	}
	return ""
}

// Usage reads agy's final result event: tokens (thinking counted as
// output), turns and duration. agy reports no cost.
func (agyHarness) Usage(log []byte) *secrets.Usage {
	var last *secrets.Usage
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Event  string `json:"event"`
			Result *struct {
				NumTurns        *int64   `json:"num_turns"`
				DurationSeconds *float64 `json:"duration_seconds"`
				Usage           *struct {
					Input     *int64 `json:"input_tokens"`
					Output    *int64 `json:"output_tokens"`
					Thinking  *int64 `json:"thinking_tokens"`
					CacheRead *int64 `json:"cache_read_tokens"`
				} `json:"usage"`
			} `json:"result"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Event != "result" || ev.Result == nil {
			return
		}
		u := &secrets.Usage{Turns: ev.Result.NumTurns}
		if d := ev.Result.DurationSeconds; d != nil {
			ms := int64(*d * 1000)
			u.DurationMS = &ms
		}
		if t := ev.Result.Usage; t != nil {
			u.InputTokens, u.CacheReadTokens = t.Input, t.CacheRead
			if t.Output != nil {
				out := *t.Output
				if t.Thinking != nil {
					out += *t.Thinking
				}
				u.OutputTokens = &out
			}
		}
		last = u
	})
	return last
}

// kimiHarness runs Kimi Code CLI. Like agy it has no per-run MCP flag, so
// the launcher writes the workspace file <worktree>/.kimi-code/mcp.json
// (excluded from git like agy's). That file names the variable holding the
// run key instead of the key, and disables the owner's own servers by name:
// kimi lets a project entry override a same-named user one.
type kimiHarness struct{}

// KimiMCPPath is the workspace MCP config path inside a worktree.
const KimiMCPPath = ".kimi-code/mcp.json"

func (kimiHarness) PrepareMCP(l *Launch, _ string) error {
	if out, err := Git(l.Dir, nil, "ls-files", "--", KimiMCPPath); err == nil && out != "" {
		return fmt.Errorf("the repository tracks %s; bottown will not overwrite it for a kimi run", KimiMCPPath)
	}
	servers, err := kimiUserServers()
	if err != nil {
		return err
	}
	servers["bottown"] = map[string]any{"url": l.MCPURL, "bearerTokenEnvVar": "BOTTOWN_RUN_KEY"}
	b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(l.Dir, KimiMCPPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	l.MCPConfig = path
	if l.ReadOnly {
		return os.WriteFile(kimiAgentFile(*l), []byte(kimiReadOnlyAgent), 0o600)
	}
	return nil
}

// kimiUserServers returns the owner's MCP servers (from
// $KIMI_CODE_HOME/mcp.json, default ~/.kimi-code/mcp.json), each reduced to
// what identifies it plus "enabled": false. Their env, headers and args stay
// out of the worktree. Servers declared by kimi plugins are not in that file
// and stay enabled.
func kimiUserServers() (map[string]any, error) {
	home := os.Getenv("KIMI_CODE_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(h, ".kimi-code")
	}
	out := map[string]any{}
	b, err := os.ReadFile(filepath.Join(home, "mcp.json"))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	} else if err != nil {
		return nil, err
	}
	var cfg struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("kimi user MCP config: %w", err)
	}
	for name, s := range cfg.MCPServers {
		off := map[string]any{"enabled": false}
		for _, k := range []string{"command", "url", "transport"} {
			if v, ok := s[k]; ok {
				off[k] = v
			}
		}
		out[name] = off
	}
	return out, nil
}

// kimiReadOnlyAgent is the main agent of read-only (discuss) runs: kimi's
// default prompt, without the file-editing tools. kimi -p approves every
// other tool, so this is the only tier there is.
const kimiReadOnlyAgent = `---
name: bottown-read-only
description: A bottown run that must not change the worktree
disallowedTools:
  - Write
  - Edit
---
${base_prompt}
`

func kimiAgentFile(l Launch) string {
	return strings.TrimSuffix(l.RecordFile, ".json") + ".kimi-agent.md"
}

// kimiBinary is kimi on PATH, else where its installer puts it.
func kimiBinary() string {
	if _, err := exec.LookPath("kimi"); err == nil {
		return "kimi"
	}
	if h, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(h, ".kimi-code", "bin", "kimi"); fileExists(p) {
			return p
		}
	}
	return "kimi"
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (kimiHarness) Command(ctx context.Context, l Launch) (*exec.Cmd, error) {
	if l.Interactive {
		return nil, errors.New("kimi cannot start its TUI with a prompt: start kimi in the actor worktree and type /skill:bottown-<kind> N (bottown harness setup kimi installs them)")
	}
	// kimi -p never asks: it approves tool calls itself, and no allowlist
	// flag exists. Forge access is still bounded by the run key.
	args := []string{"--output-format", "stream-json"}
	if l.ReadOnly {
		args = append(args, "--agent-file", kimiAgentFile(l))
	}
	args = append(args, "-p", l.Prompt) // -p takes the next argument: keep it last
	cmd := exec.CommandContext(ctx, kimiBinary(), args...)
	cmd.Dir = l.Dir
	cmd.Env = append(RunEnv(l),
		"KIMI_CODE_TRUST_WORKSPACE=1", // else -p ignores the workspace MCP config
		"KIMI_CODE_NO_AUTO_UPDATE=1",
		// Wait for background tasks (a backgrounded make e2e) before exiting.
		"KIMI_CODE_BACKGROUND_PRINT_BACKGROUND_MODE=drain",
	)
	return cmd, nil
}

// Summary is the content of the last assistant message that calls no tools.
// kimi's stream-json lines are chat messages ({"role":"assistant",
// "content":...}); "meta" lines, such as the closing resume hint, also carry
// a content field and are skipped.
func (kimiHarness) Summary(log []byte) string {
	var last string
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m struct {
			Role      string            `json:"role"`
			Content   json.RawMessage   `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Role != "assistant" || len(m.ToolCalls) > 0 {
			continue
		}
		if s := kimiText(m.Content); s != "" {
			last = s
		}
	}
	return last
}

// Usage is nil: kimi's stream-json reports no usage.
func (kimiHarness) Usage([]byte) *secrets.Usage { return nil }

// kimiText reads message content: a string, or a list of text parts.
func kimiText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct{ Type, Text string }
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// codexHarness runs OpenAI's Codex CLI. Like agy and kimi, the launcher writes
// the workspace file <worktree>/.codex/config.toml (excluded from git), which
// names the variable holding the run key instead of the key. Batch runs execute
// non-interactively via `codex exec`.
type codexHarness struct{}

// CodexMCPPath is the workspace config path inside a worktree.
const CodexMCPPath = ".codex/config.toml"

func (codexHarness) PrepareMCP(l *Launch, _ string) error {
	if out, err := Git(l.Dir, nil, "ls-files", "--", CodexMCPPath); err == nil && out != "" {
		return fmt.Errorf("the repository tracks %s; bottown will not overwrite it for a codex run", CodexMCPPath)
	}
	content := fmt.Sprintf("[mcp_servers.bottown]\nurl = %q\nbearer_token_env_var = %q\n", l.MCPURL, "BOTTOWN_RUN_KEY")
	path := filepath.Join(l.Dir, CodexMCPPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	l.MCPConfig = path
	return nil
}

// codexBinary is codex on PATH, else where its installer puts it.
func codexBinary() string {
	if _, err := exec.LookPath("codex"); err == nil {
		return "codex"
	}
	if h, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(h, ".local", "bin", "codex"); fileExists(p) {
			return p
		}
	}
	return "codex"
}

func (codexHarness) Command(ctx context.Context, l Launch) (*exec.Cmd, error) {
	sandbox := "workspace-write"
	if l.ReadOnly {
		sandbox = "read-only"
	}
	mcpURL := fmt.Sprintf("mcp_servers.bottown.url=%q", l.MCPURL)
	mcpKey := `mcp_servers.bottown.bearer_token_env_var="BOTTOWN_RUN_KEY"`
	var args []string
	if l.Interactive {
		args = []string{
			"-s", sandbox,
			"-c", mcpURL,
			"-c", mcpKey,
			l.Prompt,
		}
	} else {
		args = []string{
			"exec",
			"--json",
			"--ephemeral",
			"--ignore-user-config",
			"--sandbox", sandbox,
			"-c", `approval_policy="never"`,
			"-c", mcpURL,
			"-c", mcpKey,
			l.Prompt,
		}
	}
	cmd := exec.CommandContext(ctx, codexBinary(), args...)
	cmd.Dir, cmd.Env = l.Dir, RunEnv(l)
	return cmd, nil
}

// Summary is the text of the last completed agent_message item, else the last
// non-JSON output line.
func (codexHarness) Summary(log []byte) string {
	var last string
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Type string `json:"type"`
			Item *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &ev) == nil && ev.Type == "item.completed" && ev.Item != nil && ev.Item.Type == "agent_message" && ev.Item.Text != "" {
			last = strings.TrimSpace(ev.Item.Text)
		}
	})
	if last != "" {
		return last
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "{") {
			return l
		}
	}
	return ""
}

// Usage sums usage from all turn.completed events.
func (codexHarness) Usage(log []byte) *secrets.Usage {
	var (
		u     *secrets.Usage
		turns int64
	)
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Type  string `json:"type"`
			Usage *struct {
				InputTokens           *int64 `json:"input_tokens"`
				CachedInputTokens     *int64 `json:"cached_input_tokens"`
				CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
				OutputTokens          *int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Type != "turn.completed" || ev.Usage == nil {
			return
		}
		turns++
		if u == nil {
			u = &secrets.Usage{}
		}
		addTokens(&u.InputTokens, ev.Usage.InputTokens)
		addTokens(&u.OutputTokens, ev.Usage.OutputTokens)
		addTokens(&u.CacheReadTokens, ev.Usage.CachedInputTokens)
		addTokens(&u.CacheWriteTokens, ev.Usage.CacheWriteInputTokens)
	})
	if u != nil {
		u.Turns = &turns
	}
	return u
}

type scriptHarness struct{ cmd string }

// PrepareMCP for scripts writes the claude-style file; scripts usually use
// $BOTTOWN_MCP_URL and $BOTTOWN_RUN_KEY directly.
func (scriptHarness) PrepareMCP(l *Launch, filesDir string) error {
	return claudeHarness{}.PrepareMCP(l, filesDir)
}

func (h scriptHarness) Command(ctx context.Context, l Launch) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", h.cmd)
	cmd.Dir, cmd.Env = l.Dir, RunEnv(l)
	return cmd, nil
}

// Summary is the script's last output line.
func (scriptHarness) Summary(log []byte) string {
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Usage reads claude-style result events, so tests can script usage.
func (scriptHarness) Usage(log []byte) *secrets.Usage { return claudeHarness{}.Usage(log) }
