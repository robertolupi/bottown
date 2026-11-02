package runs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LineKind is what a rendered log line is, for the live tail's palette.
type LineKind string

const (
	LinePlain  LineKind = "plain"  // a harness whose parts are not told apart yet
	LineText   LineKind = "text"   // assistant text
	LineTool   LineKind = "tool"   // a tool call
	LineOutput LineKind = "output" // a tool's output
	LineError  LineKind = "error"  // a denial or a failed tool call
	LineStatus LineKind = "status" // session, background task and result events
)

// LogLine is one rendered line of a batch log.
type LogLine struct {
	Kind      LineKind `json:"kind"`
	Tool      string   `json:"tool,omitempty"`       // tool name, for kind tool
	ToolClass string   `json:"tool_class,omitempty"` // see toolClass, for kind tool
	Text      string   `json:"text"`
}

// RenderLog parses a batch log into readable lines (one line per assistant
// message or tool call), for the live tail in the web UI. Encoded payloads
// are elided, also from logs written before the launcher redacted them.
func RenderLog(harness string, log []byte) []LogLine {
	lines := renderLog(harness, log)
	for i := range lines {
		lines[i].Text = string(RedactEncoded([]byte(lines[i].Text)))
	}
	return lines
}

func renderLog(harness string, log []byte) []LogLine {
	switch harness {
	case "claude":
		return renderClaudeLog(log)
	case "antigravity", "agy":
		return kindLines(LinePlain, renderAgyLog(log))
	case "kimi":
		return kindLines(LinePlain, renderKimiLog(log))
	case "codex":
		return kindLines(LinePlain, renderCodexLog(log))
	case "crush":
		// crush's log is only its final message.
		return kindLines(LineText, renderScriptLog(log))
	default:
		return kindLines(LinePlain, renderScriptLog(log))
	}
}

func kindLines(kind LineKind, texts []string) []LogLine {
	lines := make([]LogLine, len(texts))
	for i, t := range texts {
		lines[i] = LogLine{Kind: kind, Text: t}
	}
	return lines
}

// renderClaudeLog renders claude's stream-json, which names every part:
// no heuristics needed. Thinking blocks and events not named here are
// left out.
func renderClaudeLog(log []byte) []LogLine {
	var lines []LogLine
	add := func(kind LineKind, text string) {
		lines = append(lines, LogLine{Kind: kind, Text: text})
	}
	lastText := ""
	denied := map[string]bool{}  // tool_use IDs whose denial is shown
	tasks := map[string]string{} // background task ID -> description
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Type    string          `json:"type"`
			Subtype string          `json:"subtype"`
			Message json.RawMessage `json:"message"` // an object, or a denial's string
			// system init
			Model          string `json:"model"`
			PermissionMode string `json:"permissionMode"`
			// system permission_denied
			ToolName  string `json:"tool_name"`
			ToolUseID string `json:"tool_use_id"`
			// system task_started, task_notification
			TaskID      string `json:"task_id"`
			Description string `json:"description"`
			Summary     string `json:"summary"`
			Status      string `json:"status"`
			// rate_limit_event
			RateLimit *struct {
				Status   string `json:"status"`
				Type     string `json:"rateLimitType"`
				ResetsAt int64  `json:"resetsAt"`
			} `json:"rate_limit_info"`
			// result
			Result     string  `json:"result"`
			IsError    bool    `json:"is_error"`
			NumTurns   int     `json:"num_turns"`
			Cost       float64 `json:"total_cost_usd"`
			DurationMS int64   `json:"duration_ms"`
		}
		if json.Unmarshal(line, &ev) != nil {
			s := strings.TrimSpace(string(line))
			if s != "" && !strings.HasPrefix(s, "{") {
				add(LinePlain, s)
			}
			return
		}
		switch ev.Type {
		case "assistant":
			var m struct {
				Content []struct {
					Type  string         `json:"type"`
					Text  string         `json:"text"`
					Name  string         `json:"name"`
					Input map[string]any `json:"input"`
				} `json:"content"`
			}
			if json.Unmarshal(ev.Message, &m) != nil {
				return
			}
			for _, c := range m.Content {
				switch c.Type {
				case "text":
					if t := formatAssistantText(c.Text); t != "" {
						add(LineText, t)
						lastText = t
					}
				case "tool_use":
					arg := toolArg(c.Input)
					lines = append(lines, LogLine{
						Kind:      LineTool,
						Tool:      c.Name,
						ToolClass: toolClass(c.Name, arg),
						Text:      joinToolCall(c.Name, arg),
					})
				}
			}
		case "user":
			var m struct {
				Content []struct {
					Type      string          `json:"type"`
					ToolUseID string          `json:"tool_use_id"`
					IsError   bool            `json:"is_error"`
					Content   json.RawMessage `json:"content"`
				} `json:"content"`
			}
			if json.Unmarshal(ev.Message, &m) != nil {
				return
			}
			for _, c := range m.Content {
				// A denied call's result repeats its denial.
				if c.Type != "tool_result" || !c.IsError || denied[c.ToolUseID] {
					continue
				}
				if t := formatAssistantText(toolResultText(c.Content)); t != "" {
					add(LineError, t)
				}
			}
		case "system":
			switch ev.Subtype {
			case "init":
				add(LineStatus, fmt.Sprintf("session started: %s, %s", ev.Model, ev.PermissionMode))
			case "permission_denied":
				var msg string
				_ = json.Unmarshal(ev.Message, &msg)
				denied[ev.ToolUseID] = true
				add(LineError, fmt.Sprintf("denied: %s: %s", ev.ToolName, formatAssistantText(msg)))
			case "task_started", "task_notification":
				desc := ev.Description
				if desc == "" {
					desc = tasks[ev.TaskID]
				}
				if desc == "" {
					desc = ev.Summary
				}
				tasks[ev.TaskID] = desc
				status := ev.Status
				if status == "" {
					status = "started"
				}
				add(LineStatus, fmt.Sprintf("background: %s — %s", formatAssistantText(desc), status))
			}
		case "rate_limit_event":
			if rl := ev.RateLimit; rl != nil && rl.Status != "" && rl.Status != "allowed" {
				s := "rate limit: " + rl.Status
				if rl.Type != "" {
					s += " (" + rl.Type + ")"
				}
				if rl.ResetsAt > 0 {
					s += ", resets " + time.Unix(rl.ResetsAt, 0).UTC().Format("2006-01-02 15:04 UTC")
				}
				add(LineStatus, s)
			}
		case "result":
			if ev.Subtype != "" {
				kind, s := LineStatus, "done"
				if ev.IsError {
					kind, s = LineError, "failed ("+ev.Subtype+")"
				}
				turns := "turns"
				if ev.NumTurns == 1 {
					turns = "turn"
				}
				add(kind, fmt.Sprintf("%s: %d %s, $%.2f, %s", s, ev.NumTurns, turns, ev.Cost, formatLogDuration(ev.DurationMS)))
			}
			// The result is usually the last assistant text again.
			if t := formatAssistantText(ev.Result); t != "" && t != lastText {
				add(LineText, t)
				lastText = t
			}
		}
	})
	return lines
}

// toolResultText is a tool_result's content: a string, or text blocks.
func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return b.Text
		}
	}
	return ""
}

func formatLogDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d >= time.Second {
		d = d.Round(time.Second)
	}
	return d.String()
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
	return joinToolCall(name, toolArg(input))
}

func joinToolCall(name, arg string) string {
	if arg == "" {
		return name
	}
	return name + ": " + arg
}

// toolArg is the part of a tool call's input worth showing: its command,
// path or query, or the input itself.
func toolArg(input any) string {
	if input == nil {
		return ""
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
			return strings.TrimSpace(s)
		}
	}
	if m, ok := input.(map[string]any); ok {
		if len(m) == 0 {
			return ""
		}
		for _, key := range []string{"command", "cmd"} {
			if v, ok := m[key]; ok {
				return fmt.Sprint(v)
			}
		}
		for _, key := range []string{"path", "file_path", "filename", "file"} {
			if v, ok := m[key]; ok {
				return fmt.Sprint(v)
			}
		}
		for _, key := range []string{"query", "pattern", "search"} {
			if v, ok := m[key]; ok {
				return fmt.Sprint(v)
			}
		}
		if len(m) == 1 {
			for _, v := range m {
				return fmt.Sprint(v)
			}
		}
		b, err := json.Marshal(m)
		if err == nil {
			return string(b)
		}
	}
	return fmt.Sprint(input)
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
