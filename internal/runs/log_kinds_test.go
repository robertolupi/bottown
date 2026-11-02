package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func lineTexts(lines []LogLine) []string {
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.Text
	}
	return texts
}

func dumpLines(lines []LogLine) string {
	var s string
	for _, l := range lines {
		s += fmt.Sprintf("%+v\n", l)
	}
	return s
}

// Harnesses whose parts are not told apart yet render plain lines;
// crush's log is only its final message.
func TestRenderLog_Kinds(t *testing.T) {
	for _, tt := range []struct {
		harness, file string
		kind          LineKind
	}{
		{"agy", "agy-1.4.12.stream.jsonl", LinePlain},
		{"kimi", "kimi-2.4.1.stream.jsonl", LinePlain},
		{"codex", "codex-0.166.0.stream.jsonl", LinePlain},
		{"crush", "crush-0.99.0.log", LineText},
		{"script", "crush-0.99.0.log", LinePlain},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", tt.file))
		if err != nil {
			t.Fatal(err)
		}
		lines := RenderLog(tt.harness, b)
		if len(lines) == 0 {
			t.Errorf("%s: no lines", tt.harness)
		}
		for _, l := range lines {
			if l.Kind != tt.kind || l.Tool != "" || l.ToolClass != "" {
				t.Errorf("%s: line %+v, want kind %q and no tool", tt.harness, l, tt.kind)
			}
		}
	}
}

// The claude testdata: a session start, a denied call, a failed call and
// a background task, trimmed from a real run, then the "hello" run.
func TestRenderLog_ClaudeGolden(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.316.stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got := RenderLog("claude", b)
	want := []LogLine{
		{Kind: LineStatus, Text: "session started: claude-opus-5-5, acceptEdits"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolShell, Text: `Bash: node -e 'const p=require("./node_modules/svelte/package.json");console.log(p.version, JSON.stringify(p.exports["."]), JSON.stringify(p.exports["./internal/client"]))'`},
		{Kind: LineError, Text: "denied: Bash: This command requires approval"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolShell, Text: `Bash: grep -n '"version"' node_modules/svelte/package.json && grep -n -A12 '"exports"' node_modules/svelte/package.json`},
		{Kind: LineError, Text: "Exit code 2"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolShell, Text: "Bash: make -C /Users/rlupi/src/fams/bottown/main/.bottown/runs/r-0d2ce79f check 2>&1 | tail -15"},
		{Kind: LineStatus, Text: "background: Run repository checks — started"},
		{Kind: LineStatus, Text: "background: Run repository checks — completed"},
		{Kind: LineText, Text: "hello"},
		{Kind: LineStatus, Text: "done: 1 turn, $0.05, 3s"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}
}

func TestRenderLog_ClaudeEvents(t *testing.T) {
	log := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":""},{"type":"tool_use","name":"Bash","input":{"command":"bottown issue show 12"}},{"type":"tool_use","name":"mcp__bottown__whoami","input":{}},{"type":"tool_use","name":"Grep","input":{"pattern":"LogLine"}}]}}
{"type":"system","subtype":"thinking_tokens","estimated_tokens":50}
{"type":"system","subtype":"session_title_changed","title":"bottown r-1"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","is_error":true,"tool_use_id":"t1","content":[{"type":"text","text":"\nfile not found\nmore"}]}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"fine"}]}}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"}}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1794160800}}
{"type":"system","subtype":"task_notification","task_id":"bg2","status":"failed","summary":"Run e2e"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Checks pass."}]}}
{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":40,"total_cost_usd":1.234,"duration_ms":125400,"result":"Stopped."}
`
	want := []LogLine{
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolForge, Text: "Bash: bottown issue show 12"},
		{Kind: LineTool, Tool: "mcp__bottown__whoami", ToolClass: ToolForge, Text: "mcp__bottown__whoami"},
		{Kind: LineTool, Tool: "Grep", ToolClass: ToolSearch, Text: "Grep: LogLine"},
		{Kind: LineError, Text: "file not found"},
		{Kind: LineStatus, Text: "rate limit: rejected (five_hour), resets 2026-11-08 18:00 UTC"},
		{Kind: LineStatus, Text: "background: Run e2e — failed"},
		{Kind: LineText, Text: "Checks pass."},
		{Kind: LineError, Text: "failed (error_max_turns): 40 turns, $1.23, 2m5s"},
		{Kind: LineText, Text: "Stopped."},
	}
	got := RenderLog("claude", []byte(log))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}
}

func TestLogLineJSON(t *testing.T) {
	b, err := json.Marshal([]LogLine{
		{Kind: LineTool, Tool: "Read", ToolClass: ToolRead, Text: "Read: a.go"},
		{Kind: LinePlain, Text: "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"kind":"tool","tool":"Read","tool_class":"read","text":"Read: a.go"},{"kind":"plain","text":"x"}]`
	if string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}
