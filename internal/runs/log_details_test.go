package runs

import (
	"strings"
	"testing"
)

func TestRenderLog_ToolDetails(t *testing.T) {
	tests := []struct {
		name       string
		harness    string
		log        string
		wantText   string
		parameters string
		output     string
	}{
		{
			name:    "claude",
			harness: "claude",
			log: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"path":"web/test/state.test.ts","offset":20}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"first line\nsecond line"}]}}`,
			wantText:   "Read: web/test/state.test.ts",
			parameters: `"offset": 20`,
			output:     "first line\nsecond line",
		},
		{
			name:       "agy",
			harness:    "agy",
			log:        `{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"a.go","StartLine":1},"output":"package a"}}}`,
			wantText:   "read_file: a.go:1-",
			parameters: `"StartLine": 1`,
			output:     "package a",
		},
		{
			name:    "kimi",
			harness: "kimi",
			log: `{"role":"assistant","tool_calls":[{"type":"function","id":"t1","function":{"name":"Read","arguments":"{\"path\":\"a.go\"}"}}]}
{"role":"tool","tool_call_id":"t1","content":"package a\npackage b"}`,
			wantText:   "Read: a.go",
			parameters: `"path": "a.go"`,
			output:     "package a\npackage b",
		},
		{
			name:    "codex updates a started item",
			harness: "codex",
			log: `{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"make check","status":"in_progress"}}
{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"make check","aggregated_output":"all passed","exit_code":0,"status":"completed"}}`,
			wantText:   "Bash: make check",
			parameters: `"command": "make check"`,
			output:     "all passed",
		},
		{
			name:    "crush",
			harness: "crush",
			log: `{"meta":{"id":"s1"}}
{"role":"assistant","parts":[{"type":"tool_call","tool_call_id":"t1","name":"view","input":"{\"file_path\":\"a.go\",\"offset\":2}"}]}
{"role":"tool","parts":[{"type":"tool_result","tool_call_id":"t1","content":"package a"}]}`,
			wantText:   "view: a.go",
			parameters: `"offset": 2`,
			output:     "package a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := RenderLog(tt.harness, []byte(tt.log))
			var tools []LogLine
			for _, l := range lines {
				if l.Kind == LineTool {
					tools = append(tools, l)
				}
			}
			if len(tools) != 1 {
				t.Fatalf("got %d tool lines, want 1:\n%s", len(tools), dumpLines(lines))
			}
			got := tools[0]
			if got.Text != tt.wantText || got.State != "completed" {
				t.Errorf("tool = %+v, want text %q and completed", got, tt.wantText)
			}
			if !strings.Contains(got.Parameters, tt.parameters) || !strings.Contains(got.Output, tt.output) {
				t.Errorf("tool details = parameters %q, output %q", got.Parameters, got.Output)
			}
		})
	}
}

func TestRenderLog_ToolFailureStates(t *testing.T) {
	logs := map[string]string{
		"claude": `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"false"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"failed"}]}}`,
		"agy": `{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"false"},"output":"Command exited with code 2.\n"}}}`,
		"kimi": `{"role":"assistant","tool_calls":[{"type":"function","id":"t1","function":{"name":"Bash","arguments":"{\"command\":\"false\"}"}}]}
{"role":"tool","tool_call_id":"t1","content":"Command failed with exit code 2"}`,
		"codex": `{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"false","aggregated_output":"failed","exit_code":2,"status":"failed"}}`,
		"crush": `{"meta":{"id":"s1"}}
{"role":"assistant","parts":[{"type":"tool_call","tool_call_id":"t1","name":"bash","input":"{\"command\":\"false\"}"}]}
{"role":"tool","parts":[{"type":"tool_result","tool_call_id":"t1","content":"failed\nExit code 2"}]}`,
	}
	for harness, log := range logs {
		t.Run(harness, func(t *testing.T) {
			lines := RenderLog(harness, []byte(log))
			found := false
			for _, l := range lines {
				if l.Kind == LineTool {
					found = true
					if l.State != "failed" {
						t.Errorf("tool = %+v, want failed", l)
					}
				}
			}
			if !found {
				t.Fatalf("no tool line in %+v", lines)
			}
		})
	}
}

func TestRenderLog_IncompleteRecordsFallBackToPlainText(t *testing.T) {
	const partial = `{"type":"assistant","message":`
	logs := map[string]string{
		"claude": `{"type":"result","result":"hello"}` + "\n" + partial,
		"agy":    `{"event":"init","init":{"model":"m"}}` + "\n" + partial,
		"kimi":   `{"role":"meta","type":"system.version","version":"1"}` + "\n" + partial,
		"codex":  `{"type":"thread.started","thread_id":"t1"}` + "\n" + partial,
		"crush":  `{"meta":{"id":"s1"}}` + "\n" + partial,
	}
	for harness, log := range logs {
		t.Run(harness, func(t *testing.T) {
			lines := RenderLog(harness, []byte(log))
			last := lines[len(lines)-1]
			if last.Kind != LinePlain || last.Text != partial {
				t.Fatalf("last line = %+v, want incomplete record as plain text", last)
			}
		})
	}
}

func TestRenderLog_UnknownJSONFallsBackToPlainText(t *testing.T) {
	const record = `{"future":"event"}`
	for _, harness := range []string{"claude", "agy", "kimi", "codex"} {
		t.Run(harness, func(t *testing.T) {
			lines := RenderLog(harness, []byte(record))
			if len(lines) != 1 || lines[0].Kind != LinePlain || lines[0].Text != record {
				t.Fatalf("lines = %+v, want the unknown record as plain text", lines)
			}
		})
	}
}

func TestRenderLog_RedactsExpandableDetails(t *testing.T) {
	encoded := strings.Repeat("a", 256)
	log := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"path":"` + encoded + `"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` + encoded + `"}]}}`
	lines := RenderLog("claude", []byte(log))
	if len(lines) != 1 || strings.Contains(lines[0].Parameters+lines[0].Output, encoded) {
		t.Fatalf("encoded payload survived in %+v", lines)
	}
	if !strings.Contains(lines[0].Parameters, "encoded bytes elided") || !strings.Contains(lines[0].Output, "encoded bytes elided") {
		t.Fatalf("details were not redacted: %+v", lines[0])
	}
}

func TestAssistantLineKeepsFullTextForExpansion(t *testing.T) {
	l, ok := assistantLine("First line.\n\nFull explanation.")
	if !ok || l.Text != "First line." || l.Detail != "First line.\n\nFull explanation." {
		t.Fatalf("assistantLine = %+v, %v", l, ok)
	}
}
