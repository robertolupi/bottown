package runs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// RenderLog parses a batch log into readable lines (one line per assistant
// message or tool call), for the live tail in the web UI. Encoded payloads
// are elided, also from logs written before the launcher redacted them.
func RenderLog(harness string, log []byte) []string {
	lines := renderLog(harness, log)
	for i, l := range lines {
		lines[i] = string(RedactEncoded([]byte(l)))
	}
	return lines
}

func renderLog(harness string, log []byte) []string {
	switch harness {
	case "claude":
		return renderClaudeLog(log)
	case "antigravity", "agy":
		return renderAgyLog(log)
	case "kimi":
		return renderKimiLog(log)
	case "codex":
		return renderCodexLog(log)
	case "crush":
		return renderScriptLog(log)
	default:
		return renderScriptLog(log)
	}
}

func renderClaudeLog(log []byte) []string {
	var lines []string
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Type    string `json:"type"`
			Message *struct {
				Role    string `json:"role"`
				Content []struct {
					Type  string         `json:"type"`
					Text  string         `json:"text"`
					Name  string         `json:"name"`
					Input map[string]any `json:"input"`
				} `json:"content"`
			} `json:"message"`
			Result string `json:"result"`
		}
		if json.Unmarshal(line, &ev) != nil {
			s := strings.TrimSpace(string(line))
			if s != "" && !strings.HasPrefix(s, "{") {
				lines = append(lines, s)
			}
			return
		}
		if ev.Type == "assistant" && ev.Message != nil {
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "text":
					if t := formatAssistantText(c.Text); t != "" {
						lines = append(lines, t)
					}
				case "tool_use":
					lines = append(lines, formatToolCall(c.Name, c.Input))
				}
			}
		} else if ev.Type == "result" && ev.Result != "" {
			if t := formatAssistantText(ev.Result); t != "" {
				if len(lines) == 0 || lines[len(lines)-1] != t {
					lines = append(lines, t)
				}
			}
		}
	})
	return lines
}

func renderAgyLog(log []byte) []string {
	var lines []string
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Event      string `json:"event"`
			StepUpdate *struct {
				StepType string `json:"step_type"`
				State    string `json:"state"`
				ToolCall *struct {
					Name string `json:"name"`
					Args any    `json:"args"`
				} `json:"tool_call"`
				ToolCalls []struct {
					Name string `json:"name"`
					Args any    `json:"args"`
				} `json:"tool_calls"`
			} `json:"step_update"`
			Result *struct {
				Response string `json:"response"`
			} `json:"result"`
		}
		if json.Unmarshal(line, &ev) != nil {
			s := strings.TrimSpace(string(line))
			if s != "" && !strings.HasPrefix(s, "{") {
				lines = append(lines, s)
			}
			return
		}
		if ev.Event == "step_update" && ev.StepUpdate != nil {
			su := ev.StepUpdate
			if su.ToolCall != nil && su.ToolCall.Name != "" {
				lines = append(lines, formatToolCall(su.ToolCall.Name, su.ToolCall.Args))
			}
			for _, tc := range su.ToolCalls {
				if tc.Name != "" {
					lines = append(lines, formatToolCall(tc.Name, tc.Args))
				}
			}
		} else if ev.Event == "result" && ev.Result != nil && ev.Result.Response != "" {
			if t := formatAssistantText(ev.Result.Response); t != "" {
				lines = append(lines, t)
			}
		}
	})
	if len(lines) == 0 {
		return renderScriptLog(log)
	}
	return lines
}

func renderKimiLog(log []byte) []string {
	var lines []string
	eachJSONLine(log, func(line []byte) {
		var m struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(line, &m) != nil {
			s := strings.TrimSpace(string(line))
			if s != "" && !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "•") {
				lines = append(lines, s)
			}
			return
		}
		if m.Role != "assistant" {
			return
		}
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != "" {
				lines = append(lines, formatToolCall(tc.Function.Name, tc.Function.Arguments))
			}
		}
		if t := kimiText(m.Content); t != "" {
			if s := formatAssistantText(t); s != "" {
				lines = append(lines, s)
			}
		}
	})
	return lines
}

func renderCodexLog(log []byte) []string {
	var lines []string
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Type string `json:"type"`
			Item *struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				Command   string `json:"command"`
				Tool      string `json:"tool"`
				Arguments any    `json:"arguments"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &ev) != nil {
			s := strings.TrimSpace(string(line))
			if s != "" && !strings.HasPrefix(s, "{") {
				lines = append(lines, s)
			}
			return
		}
		if ev.Type != "item.completed" || ev.Item == nil {
			return
		}
		switch ev.Item.Type {
		case "agent_message":
			if t := formatAssistantText(ev.Item.Text); t != "" {
				lines = append(lines, t)
			}
		case "command_execution":
			if ev.Item.Command != "" {
				lines = append(lines, formatToolCall("Bash", ev.Item.Command))
			}
		case "mcp_tool_call":
			if ev.Item.Tool != "" {
				lines = append(lines, formatToolCall(ev.Item.Tool, ev.Item.Arguments))
			}
		}
	})
	if len(lines) == 0 {
		return renderScriptLog(log)
	}
	return lines
}

func renderScriptLog(log []byte) []string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s != "" {
			lines = append(lines, s)
		}
	}
	return lines
}

func formatToolCall(name string, input any) string {
	if input == nil {
		return name
	}
	if raw, ok := input.(json.RawMessage); ok {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			input = m
		} else {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				input = s
			} else {
				input = string(raw)
			}
		}
	}
	if s, ok := input.(string); ok {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			input = m
		} else {
			return fmt.Sprintf("%s: %s", name, strings.TrimSpace(s))
		}
	}
	if m, ok := input.(map[string]any); ok {
		if len(m) == 0 {
			return name
		}
		for _, key := range []string{"command", "cmd"} {
			if v, ok := m[key]; ok {
				return fmt.Sprintf("%s: %v", name, v)
			}
		}
		for _, key := range []string{"path", "file_path", "filename", "file"} {
			if v, ok := m[key]; ok {
				return fmt.Sprintf("%s: %v", name, v)
			}
		}
		for _, key := range []string{"query", "pattern", "search"} {
			if v, ok := m[key]; ok {
				return fmt.Sprintf("%s: %v", name, v)
			}
		}
		if len(m) == 1 {
			for _, v := range m {
				return fmt.Sprintf("%s: %v", name, v)
			}
		}
		b, err := json.Marshal(m)
		if err == nil {
			return fmt.Sprintf("%s: %s", name, string(b))
		}
	}
	return fmt.Sprintf("%s: %v", name, input)
}

func formatAssistantText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			return l
		}
	}
	return text
}
