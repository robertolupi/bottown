package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// coreLines leaves the fields the original golden tests cover. Expandable
// details and tool state have focused tests in log_details_test.go.
func coreLines(lines []LogLine) []LogLine {
	out := append([]LogLine(nil), lines...)
	for i := range out {
		out[i].State = ""
		out[i].Detail = ""
		out[i].Parameters = ""
		out[i].Output = ""
	}
	return out
}

// Harnesses failing before their stream starts render plain lines;
// a crush log without a transcript is only its final message.
func TestRenderLog_Kinds(t *testing.T) {
	for _, tt := range []struct {
		harness, file string
		kind          LineKind
	}{
		{"crush", "crush-0.99.0.log", LineText},
		{"script", "crush-0.99.0.log", LinePlain},
		{"agy", "crush-0.99.0.log", LinePlain},
		{"codex", "crush-0.99.0.log", LinePlain},
		{"kimi", "crush-0.99.0.log", LinePlain},
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
	if !reflect.DeepEqual(coreLines(got), want) {
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
	if !reflect.DeepEqual(coreLines(got), want) {
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

// The agy tools testdata: trimmed from a real 1.4.12 run (r-c0500878),
// with an ERROR step of the shape agy writes. Steps that end show once;
// user_input and agent_response steps without text show nothing, and the
// result's response, which repeats the steps' text, is left out.
func TestRenderLog_AgyGolden(t *testing.T) {
	const wt = "/Users/rlupi/src/fams/bottown/main/.bottown/runs/r-c0500878"
	got := RenderLog("agy", agyTestdata(t, 0))
	want := []LogLine{
		{Kind: LineStatus, Text: "session started: gemini-3.8-flash-medium"},
		{Kind: LineTool, Tool: "run_command", ToolClass: ToolForge, Text: "run_command: bottown whoami"},
		{Kind: LineTool, Tool: "read_file", ToolClass: ToolRead, Text: "read_file: " + wt + "/internal/runs/harness.go:1-200"},
		{Kind: LineTool, Tool: "run_command", ToolClass: ToolShell, Text: `run_command: git grep -n "func runCmd" internal/cli/`},
		{Kind: LineError, Text: "Command exited with code 1."},
		{Kind: LineTool, Tool: "manage_task", ToolClass: ToolOther, Text: `manage_task: {"Action":"kill","TaskId":"cf46ae8d-515f-49c9-ba61-c242dcad006f/task-114"}`},
		{Kind: LineStatus, Text: "system message"},
		{Kind: LineTool, Tool: "edit_file", ToolClass: ToolEdit, Text: "edit_file: " + wt + "/internal/runs/harness.go"},
		{Kind: LineTool, Tool: "view_file", ToolClass: ToolRead, Text: "view_file: " + wt + "/internal/runs/tools.go"},
		{Kind: LineError, Text: "open " + wt + "/internal/runs/tools.go: no such file or directory"},
		{Kind: LineStatus, Text: "root agent idle; waiting up to 30m0s for 1 background task(s)"},
		{Kind: LineTool, Tool: "run_command", ToolClass: ToolShell, Text: "run_command: make e2e"},
		{Kind: LineText, Text: "I have implemented the requested changes and verified `make check` locally. I've now launched `make e2e` in the background and will wait for it to complete."},
		{Kind: LineStatus, Text: "system message"},
		{Kind: LineText, Text: "I have implemented the resolution for issue #160 and opened pull request [!183](http://127.0.0.1:3030/bottown/bottown/pulls/183)."},
		{Kind: LineStatus, Text: "result: SUCCESS, 1 turn, 533911 tokens, 16m43s"},
	}
	if !reflect.DeepEqual(coreLines(got), want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}
}

// agyTestdata is the agy tools testdata, or its first n lines.
func agyTestdata(t *testing.T, n int) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "agy-1.4.12-tools.stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return b
	}
	lines := strings.SplitAfter(string(b), "\n")
	return []byte(strings.Join(lines[:n], ""))
}

// While agy runs, its latest step shows before it ends: a running
// command, or the reply written so far.
func TestRenderLog_AgyLive(t *testing.T) {
	idle := LogLine{Kind: LineStatus, Text: "root agent idle; waiting up to 30m0s for 1 background task(s)"}
	running := LogLine{Kind: LineTool, Tool: "run_command", ToolClass: ToolShell, Text: "run_command: make e2e"}
	for _, tc := range []struct {
		name string
		n    int
		want []LogLine
	}{
		{"running command", 22, []LogLine{running}},
		{"waiting for it", 23, []LogLine{running, idle}},
		{"command done", 24, []LogLine{idle, running}},
		{"reply so far", 29, []LogLine{{Kind: LineStatus, Text: "system message"}, {Kind: LineText, Text: "I have implemented the resolution for issue #160 and opened pull request"}}},
	} {
		lines := RenderLog("agy", agyTestdata(t, tc.n))
		if got := coreLines(lines[len(lines)-len(tc.want):]); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got\n%s\nwant\n%s", tc.name, dumpLines(got), dumpLines(tc.want))
		}
	}
}

// agy's parameter names, an MCP call, a failed step without a message
// (shown once), a failed command, and a result that is not a success.
func TestRenderLog_AgyTools(t *testing.T) {
	log := `{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"bottown","ToolName":"issue_get","Arguments":{"number":12}}}}}
{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"grep_search","tool_info":{"name":"grep_search","parameters":{"Query":"LogLine","SearchPath":"internal"}}}}
{"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"tool","tool_name":"write_to_file","tool_info":{"name":"write_to_file","parameters":{"TargetFile":"a.go","CodeContent":"package a"}}}}
{"event":"step_update","step_update":{"step_index":4,"state":"ERROR","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"make check"}}}}
{"event":"step_update","step_update":{"step_index":4,"state":"ERROR","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"make check"}}}}
{"event":"step_update","step_update":{"step_index":5,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"false"},"output":"Command exited with code 2.\n"}}}
{"event":"result","result":{"status":"ERROR","response":"Stopped.\nmore","num_turns":3,"duration_seconds":2.5}}
`
	want := []LogLine{
		{Kind: LineTool, Tool: "call_mcp_tool", ToolClass: ToolForge, Text: "call_mcp_tool: bottown.issue_get"},
		{Kind: LineTool, Tool: "grep_search", ToolClass: ToolSearch, Text: "grep_search: LogLine"},
		{Kind: LineTool, Tool: "write_to_file", ToolClass: ToolEdit, Text: "write_to_file: a.go"},
		{Kind: LineTool, Tool: "run_command", ToolClass: ToolShell, Text: "run_command: make check"},
		{Kind: LineError, Text: "step failed"},
		{Kind: LineTool, Tool: "run_command", ToolClass: ToolShell, Text: "run_command: false"},
		{Kind: LineError, Text: "Command exited with code 2."},
		{Kind: LineError, Text: "result: ERROR, 3 turns, 3s"},
		{Kind: LineText, Text: "Stopped."},
	}
	if got := RenderLog("agy", []byte(log)); !reflect.DeepEqual(coreLines(got), want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}
}

func TestRenderLog_CodexGolden(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "codex-0.166.0.stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got := RenderLog("codex", b)
	want := []LogLine{
		{Kind: LineStatus, Text: "Reading additional input from stdin..."},
		{Kind: LineStatus, Text: "thread.started"},
		{Kind: LineText, Text: "hello"},
		{Kind: LineTool, Tool: "Edit", ToolClass: ToolEdit, Text: "Edit: internal/runs/log.go (update)"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolShell, Text: "Bash: /bin/zsh -lc 'git diff bottown/main...HEAD'"},
		{Kind: LineError, Text: "exit 128: fatal: bad revision 'bottown/main...HEAD'"},
		{Kind: LineStatus, Text: "turn done: 17722 in, 5 out"},
	}
	if !reflect.DeepEqual(coreLines(got), want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}
}

func TestRenderLog_CodexEvents(t *testing.T) {
	log := `{"type":"thread.started","thread_id":"t1"}
{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"bottown issue show 12","exit_code":0}}
{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"make test","exit_code":2,"aggregated_output":"FAIL: test\nmore"}}
{"type":"item.completed","item":{"id":"i3","type":"file_change","changes":[{"path":"a.go","kind":"add"},{"path":"b.go","kind":"delete"}]}}
{"type":"item.completed","item":{"id":"i4","type":"mcp_tool_call","server":"bottown","tool":"issue_create","arguments":{"title":"Bug"}}}
{"type":"item.completed","item":{"id":"i5","type":"agent_message","text":"All done."}}
{"type":"turn.completed","usage":{"input_tokens":500,"output_tokens":40}}
`
	want := []LogLine{
		{Kind: LineStatus, Text: "thread.started"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolForge, Text: "Bash: bottown issue show 12"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolShell, Text: "Bash: make test"},
		{Kind: LineError, Text: "exit 2: FAIL: test"},
		{Kind: LineTool, Tool: "Edit", ToolClass: ToolEdit, Text: "Edit: a.go (add)"},
		{Kind: LineTool, Tool: "Edit", ToolClass: ToolEdit, Text: "Edit: b.go (delete)"},
		{Kind: LineTool, Tool: "issue_create", ToolClass: ToolForge, Text: "issue_create: Bug"},
		{Kind: LineText, Text: "All done."},
		{Kind: LineStatus, Text: "turn done: 500 in, 40 out"},
	}
	if got := RenderLog("codex", []byte(log)); !reflect.DeepEqual(coreLines(got), want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}
}

func TestRenderLog_KimiGolden(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "kimi-2.4.1.stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got := RenderLog("kimi", b)
	want := []LogLine{
		{Kind: LineStatus, Text: "system.version: 2.4.1"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolForge, Text: "Bash: bottown issue show 176"},
		{Kind: LineOutput, Text: "#176 Live tail: typed log lines, palette and tool classes, with claude first [open] by rlupi"},
		{Kind: LineOutput, Text: "Make the live tail and the history log view show lines by kind and tool"},
		{Kind: LineOutput, Text: "class, and fill in the kinds for claude, the harness whose log is the"},
		{Kind: LineOutput, Text: "… (2 more lines)"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolShell, Text: "Bash: cat missing.txt"},
		{Kind: LineError, Text: "Command failed with exit code 1: cat: missing.txt: No such file or directory"},
		{Kind: LineOutput, Text: "cat: missing.txt: No such file or directory"},
		{Kind: LineStatus, Text: "Waiting 2s / 1 background task still running"},
		{Kind: LineText, Text: "hello"},
		{Kind: LineStatus, Text: "To resume this session: kimi -r session_6eb8c082-d72d-4405-969b-cae41b15d90e"},
	}
	if !reflect.DeepEqual(coreLines(got), want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}
}

func TestRenderLog_KimiEvents(t *testing.T) {
	log := `{"role":"meta","type":"system.version","version":"2.4.1"}
{"role":"assistant","tool_calls":[{"type":"function","id":"t1","function":{"name":"Bash","arguments":"{\"command\":\"bottown whoami\"}"}}]}
{"role":"assistant","tool_calls":[{"type":"function","id":"t2","function":{"name":"Read","arguments":"{\"path\":\"main.go\"}"}}]}
1	package main
2	import "fmt"
{"role":"tool","tool_call_id":"t3","content":"Failed to open file: access denied\nmore"}
{"role":"tool","tool_call_id":"t4","content":"Error: something went wrong"}
Waiting 5s / 2 background tasks still running
Waiting 10s / 2 background tasks still running
Waiting 15s / 1 background task still running
{"role":"assistant","content":"Finished."}
{"role":"meta","type":"session.resume_hint","session_id":"s1","command":"kimi -r s1","content":"To resume this session: kimi -r s1"}
`
	want := []LogLine{
		{Kind: LineStatus, Text: "system.version: 2.4.1"},
		{Kind: LineTool, Tool: "Bash", ToolClass: ToolForge, Text: "Bash: bottown whoami"},
		{Kind: LineTool, Tool: "Read", ToolClass: ToolRead, Text: "Read: main.go"},
		{Kind: LineOutput, Text: "1\tpackage main"},
		{Kind: LineOutput, Text: "2\timport \"fmt\""},
		{Kind: LineError, Text: "Failed to open file: access denied"},
		{Kind: LineError, Text: "Error: something went wrong"},
		{Kind: LineStatus, Text: "Waiting 15s / 1 background task still running"},
		{Kind: LineText, Text: "Finished."},
		{Kind: LineStatus, Text: "To resume this session: kimi -r s1"},
	}
	if got := RenderLog("kimi", []byte(log)); !reflect.DeepEqual(coreLines(got), want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}
}
