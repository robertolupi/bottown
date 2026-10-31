package runs

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
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
	}
	return nil, fmt.Errorf("no harness binding for %q yet (available: claude, antigravity)", name)
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
