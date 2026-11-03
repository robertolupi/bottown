package runs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRenderLog_TestdataFiles(t *testing.T) {
	tests := []struct {
		harness string
		file    string
		want    []string
	}{
		{"agy", "agy-1.4.12.stream.jsonl", []string{"hello", "result: SUCCESS, 1 turn, 15780 tokens, 5s"}},
		{"kimi", "kimi-2.4.1.stream.jsonl", []string{
			"system.version: 2.4.1",
			"Bash: bottown issue show 176",
			"#176 Live tail: typed log lines, palette and tool classes, with claude first [open] by rlupi",
			"Make the live tail and the history log view show lines by kind and tool",
			"class, and fill in the kinds for claude, the harness whose log is the",
			"… (2 more lines)",
			"Bash: cat missing.txt",
			"Command failed with exit code 1: cat: missing.txt: No such file or directory",
			"cat: missing.txt: No such file or directory",
			"Waiting 2s / 1 background task still running",
			"hello",
			"To resume this session: kimi -r session_6eb8c082-d72d-4405-969b-cae41b15d90e",
		}},
		{"codex", "codex-0.166.0.stream.jsonl", []string{
			"Reading additional input from stdin...",
			"thread.started",
			"hello",
			"Edit: internal/runs/log.go (update)",
			"Bash: /bin/zsh -lc 'git diff bottown/main...HEAD'",
			"exit 128: fatal: bad revision 'bottown/main...HEAD'",
			"turn done: 17722 in, 5 out",
		}},
		{"crush", "crush-0.99.0.log", []string{"hello"}},
		{"crush", "crush-0.99.0-session.log", []string{
			"session started: Verify PR 153",
			"I'll check the run and the pull request first.",
			"bash: bottown whoami",
			"mcp_bottown_pr_get: 153",
			"view: internal/runs/harness.go",
			"grep: func cleanSummary",
			"error parsing regex: missing argument to repetition operator: `*`",
			"bash: git diff --stat main...HEAD",
			"exit 128: fatal: ambiguous argument 'main...HEAD': unknown revision or path not in the working tree.",
			"The branch is `bottown/main` here. Running the checks.",
			"bash: make check",
			"bash: bottown review submit --event APPROVED --commit fbd7043 --body-file -",
			"APPROVED !153 at `fbd7043`. Checks pass.",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.harness, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			got := lineTexts(RenderLog(tt.harness, b))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RenderLog(%q) = %v, want %v", tt.harness, got, tt.want)
			}
		})
	}
}

func TestRenderLog_Claude(t *testing.T) {
	log := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I will check the git status."},{"type":"tool_use","name":"Bash","input":{"command":"git status"}}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"path":"AGENTS.md"}}]}}
{"type":"result","result":"All checks passed."}
`
	want := []string{
		"I will check the git status.",
		"Bash: git status",
		"Read: AGENTS.md",
		"All checks passed.",
	}
	got := lineTexts(RenderLog("claude", []byte(log)))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRenderLog_Kimi(t *testing.T) {
	log := `{"role":"meta","type":"system.version","version":"2.3.0"}
{"role":"assistant","tool_calls":[{"type":"function","id":"tool_1","function":{"name":"Bash","arguments":"{\"command\":\"echo hi\"}"}}]}
{"role":"tool","tool_call_id":"tool_1","content":"hi\n"}
• thinking goes to stderr, which shares the log
{"role":"assistant","content":"DONE"}
{"role":"meta","type":"session.resume_hint","session_id":"session_1","command":"kimi -r session_1","content":"To resume this session: kimi -r session_1"}
`
	want := []string{
		"system.version: 2.3.0",
		"Bash: echo hi",
		"DONE",
		"To resume this session: kimi -r session_1",
	}
	got := lineTexts(RenderLog("kimi", []byte(log)))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRenderLog_Codex(t *testing.T) {
	log := `{"type":"thread.started","thread_id":"01a1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I will check git status."}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"git status","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"git status","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_2","type":"mcp_tool_call","server":"bottown","tool":"issue_show","arguments":{"issue":67},"status":"completed"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Done with checks."}}
{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}
`
	want := []string{
		"thread.started",
		"I will check git status.",
		"Bash: git status",
		"issue_show: 67",
		"Done with checks.",
		"turn done: 100 in, 20 out",
	}
	got := lineTexts(RenderLog("codex", []byte(log)))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRenderLog_Crush(t *testing.T) {
	textLog := `I will check git status.
All checks passed.
`
	wantText := []string{
		"I will check git status.",
		"All checks passed.",
	}
	gotText := lineTexts(RenderLog("crush", []byte(textLog)))
	if !reflect.DeepEqual(gotText, wantText) {
		t.Fatalf("textLog got %v, want %v", gotText, wantText)
	}
}

// The transcript's kinds and tool classes; output the transcript does not
// repeat (crush's error) follows it, and a final message that is JSON, or
// a meta line inside it, is output.
func TestRenderLog_CrushTranscript(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "crush-0.99.0-session.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := RenderLog("crush", b)
	want := []LogLine{
		{Kind: LineStatus, Text: "session started: Verify PR 153"},
		{Kind: LineText, Text: "I'll check the run and the pull request first."},
		{Kind: LineTool, Tool: "bash", ToolClass: ToolForge, Text: "bash: bottown whoami"},
		{Kind: LineTool, Tool: "mcp_bottown_pr_get", ToolClass: ToolForge, Text: "mcp_bottown_pr_get: 153"},
		{Kind: LineTool, Tool: "view", ToolClass: ToolRead, Text: "view: internal/runs/harness.go"},
		{Kind: LineTool, Tool: "grep", ToolClass: ToolSearch, Text: "grep: func cleanSummary"},
		{Kind: LineError, Text: "error parsing regex: missing argument to repetition operator: `*`"},
		{Kind: LineTool, Tool: "bash", ToolClass: ToolShell, Text: "bash: git diff --stat main...HEAD"},
		{Kind: LineError, Text: "exit 128: fatal: ambiguous argument 'main...HEAD': unknown revision or path not in the working tree."},
		{Kind: LineText, Text: "The branch is `bottown/main` here. Running the checks."},
		{Kind: LineTool, Tool: "bash", ToolClass: ToolShell, Text: "bash: make check"},
		{Kind: LineTool, Tool: "bash", ToolClass: ToolForge, Text: "bash: bottown review submit --event APPROVED --commit fbd7043 --body-file -"},
		{Kind: LineText, Text: "APPROVED !153 at `fbd7043`. Checks pass."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}

	log := `Working on it
Error: agent run failed: context deadline exceeded

{"meta":{"id":"s1","title":"","created":"2026-11-02T14:03:11Z"}}
{"id":"m1","role":"assistant","parts":[{"type":"text","text":"Working on it"},{"type":"tool_call","name":"bash","input":"{\"command\":\"false\"}"},{"type":"finish","reason":"error"}]}
{"id":"m2","role":"tool","parts":[{"type":"tool_result","name":"bash","content":"Exit code 1\n\n<cwd>/w</cwd>"}]}
`
	want = []LogLine{
		{Kind: LineStatus, Text: "session started"},
		{Kind: LineText, Text: "Working on it"},
		{Kind: LineTool, Tool: "bash", ToolClass: ToolShell, Text: "bash: false"},
		{Kind: LineError, Text: "finish: error"},
		{Kind: LineError, Text: "exit 1"},
		{Kind: LinePlain, Text: "Error: agent run failed: context deadline exceeded"},
	}
	if got := RenderLog("crush", []byte(log)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", dumpLines(got), dumpLines(want))
	}

	for _, log := range []string{
		"{\"role\":\"assistant\",\"parts\":[]}\n",
		"{\"meta\":\"not an object\"}\nhello\n",
	} {
		if got := RenderLog("crush", []byte(log)); len(got) == 0 || got[0].Kind != LineText {
			t.Errorf("%q: got\n%s", log, dumpLines(got))
		}
	}
}

func TestRenderLog_Script(t *testing.T) {
	log := `starting run
running make check
tests passed
`
	want := []string{
		"starting run",
		"running make check",
		"tests passed",
	}
	got := lineTexts(RenderLog("script", []byte(log)))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
