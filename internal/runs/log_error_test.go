package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLastError(t *testing.T) {
	const kimi429 = "error: failed to run prompt: provider.api_error: 429 You've reached your 5-hour usage limit. Your quota will reset when the current 5-hour window ends."
	for _, tc := range []struct {
		name, harness, log string
		want               string
		toolCalls          bool
	}{
		{name: "kimi before its stream", harness: "kimi", log: kimi429 + "\n\n", want: kimi429},
		{name: "kimi after its version", harness: "kimi",
			log:  `{"role":"meta","type":"system.version","version":"2.4.1"}` + "\n" + kimi429 + "\n",
			want: kimi429},
		{name: "kimi tool failure is not the harness's", harness: "kimi",
			log: `{"role":"assistant","tool_calls":[{"type":"function","id":"t1","function":{"name":"Bash","arguments":"{\"command\":\"make\"}"}}]}
{"role":"tool","tool_call_id":"t1","content":"Command failed with exit code 2: make: *** [check] Error 1"}
{"role":"assistant","content":"The checks fail with a 429 in the test."}
`,
			toolCalls: true},
		{name: "claude error result", harness: "claude",
			log: `{"type":"system","subtype":"init","model":"opus","permissionMode":"default"}
{"type":"result","subtype":"success","is_error":true,"result":"API Error: 429 rate limited\nretry later","num_turns":1}
`,
			want: "API Error: 429 rate limited"},
		{name: "claude rejected rate limit", harness: "claude",
			log: `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1700000000}}
{"type":"result","subtype":"success","is_error":false,"result":"","num_turns":1}
`,
			want: "rate limit: rejected (five_hour), resets 2023-11-14 22:13 UTC"},
		{name: "claude agent text and denials are not errors", harness: "claude",
			log: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"rm -rf x"}}]}}
{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"t1","message":"denied"}
{"type":"assistant","message":{"content":[{"type":"text","text":"error: 429 from the provider"}]}}
`,
			toolCalls: true},
		{name: "claude stderr", harness: "claude", log: "Error: not signed in. Run claude login.\n", want: "Error: not signed in. Run claude login."},
		{name: "plain harness, launcher's line skipped", harness: "script",
			log:  "starting\nInvalid API key\n\nbottown: no transcript: exit status 1\n",
			want: "Invalid API key"},
		{name: "empty", harness: "kimi"},
		{name: "one line, bounded", harness: "script", log: strings.Repeat("ab ", 200) + "\n",
			want: strings.Repeat("ab ", 100)[:maxErrorLine-1] + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, tools := LastError(tc.harness, []byte(tc.log))
			if got != tc.want || tools != tc.toolCalls {
				t.Errorf("LastError = %q, %v; want %q, %v", got, tools, tc.want, tc.toolCalls)
			}
		})
	}
}

// A recorded run's failed tool calls are not the harness's errors.
func TestLastErrorGolden(t *testing.T) {
	for _, tc := range []struct{ harness, file string }{
		{"claude", "claude-2.1.316.stream.jsonl"},
		{"kimi", "kimi-2.4.1.stream.jsonl"},
		{"codex", "codex-0.166.0.stream.jsonl"},
		{"agy", "agy-1.4.12-tools.stream.jsonl"},
		{"crush", "crush-0.99.0-session.log"},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if line, tools := LastError(tc.harness, b); line != "" || !tools {
			t.Errorf("%s: LastError = %q, %v; want no error line, tool calls", tc.file, line, tools)
		}
	}
}
