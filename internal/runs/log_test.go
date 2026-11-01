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
		{"claude", "claude-2.1.316.stream.jsonl", []string{"hello"}},
		{"agy", "agy-1.4.12.stream.jsonl", []string{"hello"}},
		{"kimi", "kimi-2.4.1.stream.jsonl", []string{"hello"}},
	}

	for _, tt := range tests {
		t.Run(tt.harness, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			got := RenderLog(tt.harness, b)
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
	got := RenderLog("claude", []byte(log))
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
		"Bash: echo hi",
		"DONE",
	}
	got := RenderLog("kimi", []byte(log))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRenderLog_Agy(t *testing.T) {
	log := `{"event":"step_update","step_update":{"step_type":"tool_call","tool_call":{"name":"Bash","args":{"command":"git diff"}}}}
{"event":"step_update","step_update":{"step_type":"tool_call","tool_call":{"name":"Edit","args":{"file_path":"main.go"}}}}
{"event":"result","result":{"status":"SUCCESS","response":"All done\n"}}
`
	want := []string{
		"Bash: git diff",
		"Edit: main.go",
		"All done",
	}
	got := RenderLog("agy", []byte(log))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
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
	got := RenderLog("script", []byte(log))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
