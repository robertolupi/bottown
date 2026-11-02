package runs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
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
		return renderAgyLog(log)
	case "kimi":
		return renderKimiLog(log)
	case "codex":
		return renderCodexLog(log)
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

// agyEvent is one line of agy's --output-format stream-json (1.4.12).
type agyEvent struct {
	Event string `json:"event"`
	Init  *struct {
		Model string `json:"model"`
	} `json:"init"`
	Step *struct {
		Index     int          `json:"step_index"`
		State     string       `json:"state"` // ACTIVE, DONE or ERROR
		Type      string       `json:"step_type"`
		TextDelta string       `json:"text_delta"`
		ToolName  string       `json:"tool_name"`
		ToolInfo  *agyToolInfo `json:"tool_info"`
	} `json:"step_update"`
	Result *struct {
		Status          string  `json:"status"`
		Response        string  `json:"response"`
		NumTurns        int     `json:"num_turns"`
		DurationSeconds float64 `json:"duration_seconds"`
		Usage           *struct {
			Total int64 `json:"total_tokens"`
		} `json:"usage"`
	} `json:"result"`
}

type agyToolInfo struct {
	Name       string          `json:"name"`
	Parameters map[string]any  `json:"parameters"`
	Output     string          `json:"output"`
	Error      json.RawMessage `json:"error"`
}

// renderAgyLog renders agy's stream-json. Steps arrive as step_update
// events, several per step_index: an agent_response streams its text as
// text_delta pieces on ACTIVE updates, and a tool step is ACTIVE while it
// runs. A step is shown once, when it ends (DONE or ERROR), or while it is
// the latest step, so a running command or a reply being written shows.
// user_input steps are left out.
func renderAgyLog(log []byte) []LogLine {
	// The last step_update, by line number: the step that may show early.
	lastStep, n, events := -1, 0, false
	eachJSONLine(log, func(line []byte) {
		var ev agyEvent
		if json.Unmarshal(line, &ev) == nil {
			events = true
			if ev.Step != nil {
				lastStep = n
			}
		}
		n++
	})
	// Not agy's stream (agy failed before it started): plain lines.
	if !events {
		return kindLines(LinePlain, renderScriptLog(log))
	}

	var lines []LogLine
	add := func(kind LineKind, text string) {
		lines = append(lines, LogLine{Kind: kind, Text: text})
	}
	text := map[int]string{} // agent_response step -> its text so far
	shown := map[int]bool{}
	n = 0
	eachJSONLine(log, func(line []byte) {
		i := n
		n++
		var ev agyEvent
		if json.Unmarshal(line, &ev) != nil {
			s := strings.TrimSpace(string(line))
			if s != "" && !strings.HasPrefix(s, "{") {
				add(LineStatus, s)
			}
			return
		}
		switch {
		case ev.Event == "init" && ev.Init != nil:
			add(LineStatus, "session started: "+ev.Init.Model)
		case ev.Event == "step_update" && ev.Step != nil:
			st := ev.Step
			text[st.Index] += st.TextDelta
			ended := st.State == "DONE" || st.State == "ERROR"
			if shown[st.Index] || !ended && i != lastStep {
				return
			}
			if ended {
				shown[st.Index] = true
			}
			switch st.Type {
			case "agent_response":
				if t := formatAssistantText(text[st.Index]); t != "" {
					add(LineText, t)
				}
			case "tool":
				lines = append(lines, agyToolLine(st.ToolName, st.ToolInfo))
				if st.ToolInfo != nil && st.State == "DONE" {
					if out := formatAssistantText(st.ToolInfo.Output); agyFailedCommand(out) {
						add(LineError, out)
					}
				}
			case "system_message":
				if ended {
					s := formatAssistantText(text[st.Index])
					if s == "" {
						s = "system message"
					}
					add(LineStatus, s)
				}
			}
			if st.State == "ERROR" {
				msg := "step failed"
				if st.ToolInfo != nil {
					if m := agyErrorMessage(st.ToolInfo.Error); m != "" {
						msg = m
					}
				}
				add(LineError, msg)
			}
		case ev.Event == "result" && ev.Result != nil:
			r := ev.Result
			kind := LineStatus
			if r.Status != "SUCCESS" {
				kind = LineError
			}
			turns := "turns"
			if r.NumTurns == 1 {
				turns = "turn"
			}
			s := fmt.Sprintf("result: %s, %d %s", r.Status, r.NumTurns, turns)
			if r.Usage != nil {
				s += fmt.Sprintf(", %d tokens", r.Usage.Total)
			}
			s += ", " + formatLogDuration(int64(r.DurationSeconds*1000))
			add(kind, s)
			// The response repeats the steps' text: show it only when no
			// text step did.
			hasText := false
			for _, l := range lines {
				hasText = hasText || l.Kind == LineText
			}
			if t := formatAssistantText(r.Response); t != "" && !hasText {
				add(LineText, t)
			}
		}
	})
	return lines
}

// agyToolLine is a tool step's line. Its argument comes from agy's
// parameter names; an MCP call shows, and is classed by, the server and
// tool it calls.
func agyToolLine(toolName string, info *agyToolInfo) LogLine {
	name := toolName
	var p map[string]any
	if info != nil {
		if info.Name != "" {
			name = info.Name
		}
		p = info.Parameters
	}
	str := func(key string) string {
		if v, ok := p[key]; ok && v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}
	first := func(keys ...string) string {
		for _, k := range keys {
			if s := str(k); s != "" {
				return s
			}
		}
		return ""
	}
	arg, class := "", ""
	switch {
	case name == "call_mcp_tool":
		tool := str("ToolName")
		arg = tool
		if srv := str("ServerName"); srv != "" {
			arg = srv + "." + tool
		}
		class = toolClass(tool, "")
	case first("CommandLine") != "":
		arg = str("CommandLine")
	case first("AbsolutePath", "Path", "TargetFile") != "":
		arg = first("AbsolutePath", "Path", "TargetFile")
		if name == "read_file" && (str("StartLine") != "" || str("EndLine") != "") {
			arg += ":" + str("StartLine") + "-" + str("EndLine")
		}
	case first("Query", "SearchPath") != "":
		arg = first("Query", "SearchPath")
	case len(p) > 0:
		arg = toolArg(p)
	}
	if class == "" {
		class = toolClass(name, arg)
	}
	return LogLine{Kind: LineTool, Tool: name, ToolClass: class, Text: joinToolCall(name, arg)}
}

// agyFailedCommand reports whether a run_command output's first line is
// "Command exited with code N." with N other than 0.
func agyFailedCommand(first string) bool {
	var code int
	if _, err := fmt.Sscanf(first, "Command exited with code %d", &code); err != nil {
		return false
	}
	return code != 0
}

// agyErrorMessage is the first line of a tool_info.error: an object with
// a message, or a string.
func agyErrorMessage(raw json.RawMessage) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return formatAssistantText(e.Message)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return formatAssistantText(s)
	}
	return ""
}

var kimiWaitingRE = regexp.MustCompile(`^Waiting \d+s / .* background tasks? still running$`)

func renderKimiLog(log []byte) []LogLine {
	var lines []LogLine
	events := false

	var rawOutputRun []string
	waitingLine := ""

	flushRawOutput := func() {
		if len(rawOutputRun) == 0 {
			return
		}
		const maxLines = 3
		if len(rawOutputRun) <= maxLines {
			for _, s := range rawOutputRun {
				lines = append(lines, LogLine{Kind: LineOutput, Text: s})
			}
		} else {
			for _, s := range rawOutputRun[:maxLines] {
				lines = append(lines, LogLine{Kind: LineOutput, Text: s})
			}
			more := len(rawOutputRun) - maxLines
			lines = append(lines, LogLine{Kind: LineOutput, Text: fmt.Sprintf("… (%d more lines)", more)})
		}
		rawOutputRun = nil
	}

	flushWaiting := func() {
		if waitingLine == "" {
			return
		}
		lines = append(lines, LogLine{Kind: LineStatus, Text: waitingLine})
		waitingLine = ""
	}

	eachJSONLine(log, func(line []byte) {
		var m struct {
			Role      string          `json:"role"`
			Type      string          `json:"type"`
			Version   string          `json:"version"`
			Command   string          `json:"command"`
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
			if s == "" || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "•") {
				return
			}
			if kimiWaitingRE.MatchString(s) {
				flushRawOutput()
				waitingLine = s
			} else {
				flushWaiting()
				rawOutputRun = append(rawOutputRun, s)
			}
			return
		}
		if m.Role != "assistant" && m.Role != "meta" && m.Role != "tool" {
			return
		}
		events = true
		flushRawOutput()
		flushWaiting()

		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				if tc.Function.Name != "" {
					arg := toolArg(tc.Function.Arguments)
					lines = append(lines, LogLine{
						Kind:      LineTool,
						Tool:      tc.Function.Name,
						ToolClass: toolClass(tc.Function.Name, arg),
						Text:      joinToolCall(tc.Function.Name, arg),
					})
				}
			}
			if t := kimiText(m.Content); t != "" {
				if s := formatAssistantText(t); s != "" {
					lines = append(lines, LogLine{Kind: LineText, Text: s})
				}
			}
		case "meta":
			switch m.Type {
			case "system.version":
				s := "system.version"
				if m.Version != "" {
					s += ": " + m.Version
				}
				lines = append(lines, LogLine{Kind: LineStatus, Text: s})
			case "session.resume_hint":
				t := kimiText(m.Content)
				if s := formatAssistantText(t); s != "" {
					lines = append(lines, LogLine{Kind: LineStatus, Text: s})
				} else if m.Command != "" {
					lines = append(lines, LogLine{Kind: LineStatus, Text: m.Command})
				}
			}
		case "tool":
			t := kimiText(m.Content)
			if first := formatAssistantText(t); first != "" {
				if strings.HasPrefix(first, "Command failed with exit code") ||
					strings.HasPrefix(first, "Failed to ") ||
					strings.HasPrefix(first, "Error:") {
					lines = append(lines, LogLine{Kind: LineError, Text: first})
				}
			}
		}
	})

	flushRawOutput()
	flushWaiting()

	if !events {
		return kindLines(LinePlain, renderScriptLog(log))
	}
	return lines
}

func renderCodexLog(log []byte) []LogLine {
	var lines []LogLine
	events := false
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Type  string `json:"type"`
			Usage *struct {
				InputTokens  *int64 `json:"input_tokens"`
				OutputTokens *int64 `json:"output_tokens"`
			} `json:"usage"`
			Item *struct {
				Type             string `json:"type"`
				Text             string `json:"text"`
				Command          string `json:"command"`
				Tool             string `json:"tool"`
				Arguments        any    `json:"arguments"`
				ExitCode         *int   `json:"exit_code"`
				AggregatedOutput string `json:"aggregated_output"`
				Changes          []struct {
					Path string `json:"path"`
					Kind string `json:"kind"`
				} `json:"changes"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &ev) != nil {
			s := strings.TrimSpace(string(line))
			if s != "" && !strings.HasPrefix(s, "{") {
				if s == "Reading additional input from stdin..." {
					lines = append(lines, LogLine{Kind: LineStatus, Text: s})
				} else {
					lines = append(lines, LogLine{Kind: LinePlain, Text: s})
				}
			}
			return
		}
		events = true
		switch ev.Type {
		case "thread.started":
			lines = append(lines, LogLine{Kind: LineStatus, Text: "thread.started"})
		case "turn.completed":
			s := "turn done"
			if ev.Usage != nil && ev.Usage.InputTokens != nil && ev.Usage.OutputTokens != nil {
				s = fmt.Sprintf("turn done: %d in, %d out", *ev.Usage.InputTokens, *ev.Usage.OutputTokens)
			}
			lines = append(lines, LogLine{Kind: LineStatus, Text: s})
		case "item.completed":
			if ev.Item == nil {
				return
			}
			switch ev.Item.Type {
			case "agent_message":
				if t := formatAssistantText(ev.Item.Text); t != "" {
					lines = append(lines, LogLine{Kind: LineText, Text: t})
				}
			case "command_execution":
				if ev.Item.Command != "" {
					cmd := ev.Item.Command
					lines = append(lines, LogLine{
						Kind:      LineTool,
						Tool:      "Bash",
						ToolClass: toolClass("Bash", cmd),
						Text:      joinToolCall("Bash", cmd),
					})
				}
				if ev.Item.ExitCode != nil && *ev.Item.ExitCode != 0 {
					first := formatAssistantText(ev.Item.AggregatedOutput)
					text := fmt.Sprintf("exit %d", *ev.Item.ExitCode)
					if first != "" {
						text = fmt.Sprintf("exit %d: %s", *ev.Item.ExitCode, first)
					}
					lines = append(lines, LogLine{
						Kind: LineError,
						Text: text,
					})
				}
			case "file_change":
				for _, ch := range ev.Item.Changes {
					if ch.Path != "" {
						lines = append(lines, LogLine{
							Kind:      LineTool,
							Tool:      "Edit",
							ToolClass: ToolEdit,
							Text:      joinToolCall("Edit", ch.Path),
						})
					}
				}
			case "mcp_tool_call":
				if ev.Item.Tool != "" {
					arg := toolArg(ev.Item.Arguments)
					lines = append(lines, LogLine{
						Kind:      LineTool,
						Tool:      ev.Item.Tool,
						ToolClass: toolClass(ev.Item.Tool, arg),
						Text:      joinToolCall(ev.Item.Tool, arg),
					})
				}
			}
		}
	})
	if !events {
		return kindLines(LinePlain, renderScriptLog(log))
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
