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

// LogLine is one rendered line of a batch log. Tool calls carry their full
// parameters and output for the UI's expandable details; Detail holds the
// full text when Text is only its first line.
type LogLine struct {
	Kind       LineKind `json:"kind"`
	Tool       string   `json:"tool,omitempty"`       // tool name, for kind tool
	ToolClass  string   `json:"tool_class,omitempty"` // see toolClass, for kind tool
	State      string   `json:"state,omitempty"`      // running, completed or failed, for kind tool
	Text       string   `json:"text"`
	Detail     string   `json:"detail,omitempty"`
	Parameters string   `json:"parameters,omitempty"`
	Output     string   `json:"output,omitempty"`

	toolError bool // for kind error: a failed or denied tool call, not the harness's own error
}

// RenderLog parses a batch log into readable lines (one line per assistant
// message or tool call), for the live tail in the web UI. Encoded payloads
// are elided, also from logs written before the launcher redacted them.
func RenderLog(harness string, log []byte) []LogLine {
	lines := renderLog(harness, log)
	for i := range lines {
		lines[i].Text = string(RedactEncoded([]byte(lines[i].Text)))
		lines[i].Detail = string(RedactEncoded([]byte(lines[i].Detail)))
		lines[i].Parameters = string(RedactEncoded([]byte(lines[i].Parameters)))
		lines[i].Output = string(RedactEncoded([]byte(lines[i].Output)))
	}
	return lines
}

// FailedRunError is a launcher's error for a run that did not succeed.
// Log and LastError are set for a batch run: its log's path, and what
// LastError found there.
type FailedRunError struct {
	ID, Status string
	Log        string
	LastError  string
	ToolCalls  bool
}

func (e *FailedRunError) Error() string {
	return fmt.Sprintf("run %s %s", e.ID, e.Status)
}

// maxErrorLine bounds LastError's line.
const maxErrorLine = 300

// LastError is what a batch log says about how the run ended, for a run
// that failed: the harness's last error line, and whether the agent made
// any tool call. The line is the last of the harness's own error and
// result events, or of the output that is none of its events (a plain-text
// harness, or one that failed before its stream started), trimmed to one
// line; never the agent's text, its tool calls or their output, nor what
// the launcher itself appended. It is "" when there is none.
func LastError(harness string, log []byte) (line string, toolCalls bool) {
	lines := RenderLog(harness, log)
	for _, l := range lines {
		toolCalls = toolCalls || l.Kind == LineTool
	}
	for i := len(lines) - 1; i >= 0 && line == ""; i-- {
		switch l := lines[i]; {
		case l.Kind == LineError && !l.toolError,
			l.Kind == LinePlain && !strings.HasPrefix(l.Text, "bottown: "):
			line = formatAssistantText(l.Text)
		}
	}
	if r := []rune(line); len(r) > maxErrorLine {
		line = string(r[:maxErrorLine-1]) + "…"
	}
	return line, toolCalls
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
		return renderCrushLog(log)
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
// left out. Tool results complete the call they refer to, keeping its place
// in the event order while adding output and a completion state.
func renderClaudeLog(log []byte) []LogLine {
	var lines []LogLine
	add := func(kind LineKind, text string) {
		lines = append(lines, LogLine{Kind: kind, Text: text})
	}
	lastText := ""
	tools := map[string]int{}    // tool_use ID -> line index
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
			if s := strings.TrimSpace(string(line)); s != "" {
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
					ID    string         `json:"id"`
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
					if l, ok := assistantLine(c.Text); ok {
						lines = append(lines, l)
						lastText = strings.TrimSpace(c.Text)
					}
				case "tool_use":
					arg := toolArg(c.Input)
					tools[c.ID] = len(lines)
					lines = append(lines, LogLine{
						Kind:       LineTool,
						Tool:       c.Name,
						ToolClass:  toolClass(c.Name, arg),
						State:      "running",
						Text:       joinToolCall(c.Name, arg),
						Parameters: toolParameters(c.Input),
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
				if c.Type != "tool_result" {
					continue
				}
				out := toolResultText(c.Content)
				if i, ok := tools[c.ToolUseID]; ok {
					lines[i].State = "completed"
					if c.IsError {
						lines[i].State = "failed"
					}
					lines[i].Output = out
				}
				// A denied call's result repeats its denial.
				if !c.IsError || denied[c.ToolUseID] {
					continue
				}
				if t := formatAssistantText(out); t != "" {
					lines = append(lines, LogLine{Kind: LineError, Text: t, toolError: true})
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
				if i, ok := tools[ev.ToolUseID]; ok {
					lines[i].State = "failed"
					lines[i].Output = msg
				}
				lines = append(lines, LogLine{Kind: LineError, Text: fmt.Sprintf("denied: %s: %s", ev.ToolName, formatAssistantText(msg)), toolError: true})
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
				kind := LineStatus
				if rl.Status == "rejected" {
					kind = LineError
				}
				add(kind, s)
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
			// The result is usually the last assistant text again; that
			// of a turn that ended in error, claude's error (an API
			// error, a usage limit).
			full := strings.TrimSpace(ev.Result)
			if l, ok := assistantLine(full); ok && full != lastText {
				if ev.IsError && ev.Subtype == "success" {
					l.Kind = LineError
				}
				lines = append(lines, l)
				lastText = full
			}
		}
	})
	if len(lines) == 0 {
		return kindLines(LinePlain, renderScriptLog(log))
	}
	return lines
}

// toolResultText is a tool_result's content: a string, or its text blocks.
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
	var text []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			text = append(text, b.Text)
		}
	}
	return strings.Join(text, "\n")
}

func assistantLine(text string) (LogLine, bool) {
	full := strings.TrimSpace(text)
	first := formatAssistantText(full)
	if first == "" {
		return LogLine{}, false
	}
	l := LogLine{Kind: LineText, Text: first}
	if full != first {
		l.Detail = full
	}
	return l, true
}

func toolParameters(input any) string {
	if raw, ok := input.(json.RawMessage); ok {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			input = v
		} else {
			return string(raw)
		}
	}
	if s, ok := input.(string); ok {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			input = v
		} else {
			return s
		}
	}
	if input == nil {
		return ""
	}
	if b, err := json.MarshalIndent(input, "", "  "); err == nil {
		return string(b)
	}
	return fmt.Sprint(input)
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
			if s != "" {
				kind := LineStatus
				if strings.HasPrefix(s, "{") {
					kind = LinePlain
				}
				add(kind, s)
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
				if l, ok := assistantLine(text[st.Index]); ok {
					lines = append(lines, l)
				}
			case "tool":
				lines = append(lines, agyToolLine(st.ToolName, st.ToolInfo, st.State))
				if st.ToolInfo != nil && st.State == "DONE" {
					if out := formatAssistantText(st.ToolInfo.Output); agyFailedCommand(out) {
						lines[len(lines)-1].State = "failed"
						lines = append(lines, LogLine{Kind: LineError, Text: out, toolError: true})
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
				lines = append(lines, LogLine{Kind: LineError, Text: msg, toolError: st.Type == "tool"})
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
			if l, ok := assistantLine(r.Response); ok && !hasText {
				lines = append(lines, l)
			}
		}
	})
	if len(lines) == 0 {
		return kindLines(LinePlain, renderScriptLog(log))
	}
	return lines
}

// agyToolLine is a tool step's line. Its argument comes from agy's
// parameter names; an MCP call shows, and is classed by, the server and
// tool it calls.
func agyToolLine(toolName string, info *agyToolInfo, state string) LogLine {
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
	line := LogLine{
		Kind:       LineTool,
		Tool:       name,
		ToolClass:  class,
		State:      "running",
		Text:       joinToolCall(name, arg),
		Parameters: toolParameters(p),
	}
	switch state {
	case "DONE":
		line.State = "completed"
	case "ERROR":
		line.State = "failed"
	}
	if info != nil {
		line.Output = info.Output
		if line.Output == "" {
			line.Output = agyErrorMessage(info.Error)
		}
	}
	return line
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
	tools := map[string]int{} // tool_call ID -> line index

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
			Role       string          `json:"role"`
			Type       string          `json:"type"`
			Version    string          `json:"version"`
			Command    string          `json:"command"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				Type     string `json:"type"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(line, &m) != nil {
			s := strings.TrimSpace(string(line))
			if s == "" || strings.HasPrefix(s, "•") {
				return
			}
			if strings.HasPrefix(s, "{") {
				flushRawOutput()
				flushWaiting()
				lines = append(lines, LogLine{Kind: LinePlain, Text: s})
			} else if kimiWaitingRE.MatchString(s) {
				flushRawOutput()
				waitingLine = s
			} else if strings.HasPrefix(s, "error: failed to run prompt: ") {
				// kimi's own error, such as a provider's 429.
				flushRawOutput()
				flushWaiting()
				lines = append(lines, LogLine{Kind: LineError, Text: s})
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
					tools[tc.ID] = len(lines)
					lines = append(lines, LogLine{
						Kind:       LineTool,
						Tool:       tc.Function.Name,
						ToolClass:  toolClass(tc.Function.Name, arg),
						State:      "running",
						Text:       joinToolCall(tc.Function.Name, arg),
						Parameters: toolParameters(tc.Function.Arguments),
					})
				}
			}
			if t := kimiText(m.Content); t != "" {
				if l, ok := assistantLine(t); ok {
					lines = append(lines, l)
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
			first := formatAssistantText(t)
			failed := strings.HasPrefix(first, "Command failed with exit code") ||
				strings.HasPrefix(first, "Failed to ") || strings.HasPrefix(first, "Error:")
			if i, ok := tools[m.ToolCallID]; ok {
				lines[i].State = "completed"
				if failed {
					lines[i].State = "failed"
				}
				lines[i].Output = t
			}
			if failed && first != "" {
				lines = append(lines, LogLine{Kind: LineError, Text: first, toolError: true})
			}
		}
	})

	flushRawOutput()
	flushWaiting()

	if !events || len(lines) == 0 {
		return kindLines(LinePlain, renderScriptLog(log))
	}
	return lines
}

func renderCodexLog(log []byte) []LogLine {
	var lines []LogLine
	events := false
	items := map[string][]int{} // item ID -> tool line indexes
	eachJSONLine(log, func(line []byte) {
		var ev struct {
			Type  string `json:"type"`
			Usage *struct {
				InputTokens  *int64 `json:"input_tokens"`
				OutputTokens *int64 `json:"output_tokens"`
			} `json:"usage"`
			Item *struct {
				ID               string `json:"id"`
				Type             string `json:"type"`
				Status           string `json:"status"`
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
			if s != "" {
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
		case "item.started", "item.completed":
			if ev.Item == nil {
				return
			}
			item := ev.Item
			state := "running"
			if ev.Type == "item.completed" {
				state = "completed"
				if item.Status == "failed" || item.ExitCode != nil && *item.ExitCode != 0 {
					state = "failed"
				}
			}
			itemIndex := 0
			addTool := func(l LogLine) {
				l.Kind, l.State = LineTool, state
				if itemIndex < len(items[item.ID]) {
					lines[items[item.ID][itemIndex]] = l
				} else {
					items[item.ID] = append(items[item.ID], len(lines))
					lines = append(lines, l)
				}
				itemIndex++
			}
			switch item.Type {
			case "agent_message":
				if ev.Type == "item.completed" {
					if l, ok := assistantLine(item.Text); ok {
						lines = append(lines, l)
					}
				}
			case "command_execution":
				if item.Command != "" {
					cmd := item.Command
					addTool(LogLine{
						Tool:       "Bash",
						ToolClass:  toolClass("Bash", cmd),
						Text:       joinToolCall("Bash", cmd),
						Parameters: toolParameters(map[string]any{"command": cmd}),
						Output:     item.AggregatedOutput,
					})
				}
				if ev.Type == "item.completed" && item.ExitCode != nil && *item.ExitCode != 0 {
					first := formatAssistantText(item.AggregatedOutput)
					text := fmt.Sprintf("exit %d", *item.ExitCode)
					if first != "" {
						text = fmt.Sprintf("exit %d: %s", *ev.Item.ExitCode, first)
					}
					lines = append(lines, LogLine{Kind: LineError, Text: text, toolError: true})
				}
			case "file_change":
				for _, ch := range item.Changes {
					if ch.Path != "" {
						arg := ch.Path
						if ch.Kind != "" {
							arg = fmt.Sprintf("%s (%s)", ch.Path, ch.Kind)
						}
						addTool(LogLine{
							Tool:       "Edit",
							ToolClass:  ToolEdit,
							Text:       joinToolCall("Edit", arg),
							Parameters: toolParameters(ch),
						})
					}
				}
			case "mcp_tool_call":
				if item.Tool != "" {
					arg := toolArg(item.Arguments)
					addTool(LogLine{
						Tool:       item.Tool,
						ToolClass:  toolClass(item.Tool, arg),
						Text:       joinToolCall(item.Tool, arg),
						Parameters: toolParameters(item.Arguments),
					})
				}
			}
		}
	})
	if !events || len(lines) == 0 {
		return kindLines(LinePlain, renderScriptLog(log))
	}
	return lines
}

// crushMeta is the meta of crush's session export (crush session show
// --json, 0.99.0).
type crushMeta struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Created string  `json:"created"`
	Cost    float64 `json:"cost"`
}

// crushMessage is one message of crush's session export: a user, assistant
// or tool message, whose parts are text, reasoning, tool_call, tool_result
// or finish (among others).
type crushMessage struct {
	Role  string `json:"role"`
	Parts []struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		Name       string `json:"name"`
		ToolCallID string `json:"tool_call_id"`
		Input      string `json:"input"`   // tool_call: JSON arguments
		Content    string `json:"content"` // tool_result
		IsError    bool   `json:"is_error"`
		Reason     string `json:"reason"` // finish
	} `json:"parts"`
}

// text is the message's text parts, as crush prints them.
func (m crushMessage) text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// crushLog is a crush run's log: what crush run printed, then, if the
// launcher exported it, the transcript (crushHarness.Transcript).
type crushLog struct {
	output     []byte
	meta       *crushMeta
	messages   []crushMessage
	incomplete []string
}

// parseCrushLog splits a crush log at the transcript's meta line, the last
// line that starts {"meta": (the transcript's JSON lines hold no raw
// newline, so none of them can hide one). Message lines count only after
// it, so a final message that holds JSON stays output. A partial streaming
// record is kept as plain text.
func parseCrushLog(log []byte) crushLog {
	cl := crushLog{output: log}
	prefix := []byte(`{"meta":`)
	start := bytes.LastIndex(log, append([]byte{'\n'}, prefix...)) + 1
	if start == 0 && !bytes.HasPrefix(log, prefix) {
		return cl
	}
	rest := log[start:]
	first, _, _ := bytes.Cut(rest, []byte{'\n'})
	var l struct {
		Meta *crushMeta `json:"meta"`
	}
	if json.Unmarshal(first, &l) != nil || l.Meta == nil {
		return cl
	}
	cl.output, cl.meta = log[:start], l.Meta
	eachJSONLine(rest[len(first):], func(line []byte) {
		var m crushMessage
		if json.Unmarshal(line, &m) == nil && m.Role != "" {
			cl.messages = append(cl.messages, m)
		} else if s := strings.TrimSpace(string(line)); s != "" {
			cl.incomplete = append(cl.incomplete, s)
		}
	})
	return cl
}

var crushExitRE = regexp.MustCompile(`^Exit code (\d+)$`)

// renderCrushLog renders a crush log's transcript: assistant text, tool
// calls, failed tool results, and finishes that are not a normal end. What
// crush printed repeats its last message: only output lines that no
// assistant text holds (an error crush reported, say) follow. A log
// without a transcript (crush failed, or one written before the launcher
// exported it) is its output, as assistant text.
func renderCrushLog(log []byte) []LogLine {
	cl := parseCrushLog(log)
	if cl.meta == nil {
		return kindLines(LineText, renderScriptLog(log))
	}
	var lines []LogLine
	add := func(kind LineKind, text string) {
		lines = append(lines, LogLine{Kind: kind, Text: text})
	}
	s := "session started"
	if t := formatAssistantText(cl.meta.Title); t != "" {
		s += ": " + t
	}
	add(LineStatus, s)
	var said strings.Builder
	tools := map[string]int{} // tool_call ID -> line index
	for _, m := range cl.messages {
		for _, p := range m.Parts {
			switch {
			case m.Role == "assistant" && p.Type == "text":
				said.WriteString(p.Text + "\n")
				if l, ok := assistantLine(p.Text); ok {
					lines = append(lines, l)
				}
			case m.Role == "assistant" && p.Type == "tool_call":
				arg := toolArg(p.Input)
				tools[p.ToolCallID] = len(lines)
				lines = append(lines, LogLine{
					Kind:       LineTool,
					Tool:       p.Name,
					ToolClass:  toolClass(p.Name, arg),
					State:      "running",
					Text:       joinToolCall(p.Name, arg),
					Parameters: toolParameters(p.Input),
				})
			case m.Role == "assistant" && p.Type == "finish":
				switch p.Reason {
				case "error", "canceled", "permission_denied":
					add(LineError, "finish: "+p.Reason)
				}
			case p.Type == "tool_result":
				errText := crushToolError(p.Content, p.IsError)
				if i, ok := tools[p.ToolCallID]; ok {
					lines[i].State = "completed"
					if errText != "" {
						lines[i].State = "failed"
					}
					lines[i].Output = p.Content
				}
				if errText != "" {
					lines = append(lines, LogLine{Kind: LineError, Text: errText, toolError: true})
				}
			}
		}
	}
	for _, l := range renderScriptLog(cl.output) {
		if !strings.Contains(said.String(), l) {
			add(LinePlain, l)
		}
	}
	for _, l := range cl.incomplete {
		add(LinePlain, l)
	}
	return lines
}

// crushToolError is a failed tool result's line: its first line if crush
// marked it an error, or, for a command that exited non-zero (crush's bash
// tool ends its output with "Exit code N"), the exit code and first line.
func crushToolError(content string, isError bool) string {
	first := formatAssistantText(content)
	if isError {
		return first
	}
	for _, l := range strings.Split(content, "\n") {
		m := crushExitRE.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil || m[1] == "0" {
			continue
		}
		if strings.HasPrefix(first, "Exit code ") {
			return "exit " + m[1]
		}
		return "exit " + m[1] + ": " + first
	}
	return ""
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
