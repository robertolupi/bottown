+++
title = "Live tail: color highlights by part of speech"
kickoff = 124
design = "docs/design/web-ui.md"
+++

The live tail (and the history log view, which uses the same renderer)
shows every harness's log as uniform grey lines. `runs.RenderLog` reduces
each event to a `string`, so the structure every harness log already has
(this is a tool call, this is the agent talking, this failed) is thrown
away before the UI sees it. This epic keeps that structure as a line
*kind*, colors kinds and tool classes, and teaches each harness's renderer
to fill them in.

Three issues: the scaffolding, proven on claude; agy, whose renderer is
broken and whose tail is the hardest to read; then codex and kimi. Each
issue repeats the part of the log survey it needs.

The kinds: `text` (the agent's prose), `tool` (a tool call), `output` (tool
output, where a harness's tail shows it), `error` (a failed or denied tool
call), `status` (session lifecycle and progress) and `plain` (unclassified,
the default). Tool lines also carry the tool's name and a *class* (shell,
read, edit, search, forge, browser, other), so the `Bash:` / `Edit:` prefix
is colored the same way across harnesses.

crush gets no issue: its logs hold only the final message, so all of it is
`text`, and there is nothing more to classify until #166 makes the binding
keep a transcript. Whoever implements #166 should map the transcript to
these kinds.

## Live tail: typed log lines, palette and tool classes, with claude first

```issue
key = "tail-kinds-claude"
depends = []
```

Make the live tail and the history log view show lines by kind and tool
class, and fill in the kinds for claude, the harness whose log is the
cleanest to prove it on. Other harnesses keep rendering the same text, as
`plain`, until their own issues.

### Typed lines, from the renderer to the UI

- In `internal/runs/log.go`, render to typed lines:

  ```go
  type LogLine struct {
      Kind      LineKind `json:"kind"`                 // plain, text, tool, output, error, status
      Tool      string   `json:"tool,omitempty"`       // tool name, for kind tool
      ToolClass string   `json:"tool_class,omitempty"` // shell, read, edit, search, forge, browser, other
      Text      string   `json:"text"`
  }
  ```

  `RenderLog` returns `[]LogLine`. Redaction (`RedactEncoded`) applies to
  `Text`. agy, codex, kimi, crush and the script fallback produce the same
  text as today, as `plain`. Set crush's lines to `text` instead: its log
  is only the final message.
- `GET /api/status` `live_runs[].log_tail` and `GET /api/runs/{id}/log`
  `lines` become arrays of these objects. The UI is embedded in the same
  binary, so there are no other consumers. Update `web/src/types.ts` and the
  Go tests that compare rendered lines.
- Web UI: one small component renders a line as
  `<div class="log-line log-{kind}" data-tool-class={toolClass}>`, with the
  tool name in its own `<span class="log-tool">`. Use it in
  `LiveLogTail.svelte` and both history log boxes in `App.svelte`.
  Follow/trim detection in `state.ts` keeps comparing line text.

### Tool classes

A shared classifier in `internal/runs`, `toolClass(name, text string)
string`, keyed on the names the harnesses use:

- **shell**: `Bash` (claude, kimi; codex commands are rendered as `Bash`),
  `run_command` (agy);
- **read**: `Read`, `read_file`, `view_file`, `list_dir`;
- **edit**: `Edit`, `Write`, `NotebookEdit`, `edit_file`, `write_file`,
  `replace_file_content`, `multi_replace_file_content`;
- **search**: `Grep`, `Glob`, `grep_search`, `find_by_name`;
- **forge**: `mcp__bottown__*` and bottown's MCP tool names, plus shell
  commands whose first word is `bottown` or ends in `/bottown` (the
  heuristic part);
- **browser**: `browser_*`, chrome-devtools MCP tools;
- everything else: **other**.

Keep the table in one place, with a test per row. The harness issues only
need to set `Tool`.

### Palette

On the tail's dark background:

- `text`: the brightest color;
- `tool`: the name colored by class, the argument in a muted tone;
- `output`: dimmed;
- `error`: red;
- `status`: italic grey;
- `plain`: today's color.

Each color needs readable contrast (WCAG AA on the tail background), and
colors are CSS custom properties. A one-line legend, or `title` tooltips,
in the tail header explains the colors.

### claude: parts of speech and how to find them

claude's stream-json names every part, so no heuristics are needed.

- **text**: `assistant` `text` blocks (as today).
- **tool**: `assistant` `tool_use` blocks (as today), with `Tool` set to the
  block's `name`.
- **error**: `system` `permission_denied` (`denied: <tool>: <message>`),
  and `user` `tool_result` blocks with `is_error: true` (first line of the
  content). These are common: 28 denials across four verify runs on
  2026-11-02.
- **status**: `system` `init` (`session started: <model>, <permissionMode>`),
  `task_started` / `task_notification` (`background: <description> —
  <status>`), `rate_limit_event` when `status` is not `allowed`, and
  `result` (`done: <num_turns> turns, $<cost>, <duration>`), followed by
  the result text as `text` as today.
- Ignored: `thinking` blocks (empty in batch logs), `thinking_tokens`,
  `session_title_changed`.

### Acceptance criteria

- The tail shows claude runs in color, with tool prefixes colored by
  class. Other harnesses look as before.
- Unit tests:
  - the existing renderer cases still give the same text;
  - `claude-2.1.316.stream.jsonl`, extended with a denial, an error result
    and a background task trimmed from a real run, has a golden output
    with kinds;
  - the classifier table, one test per row;
  - the APIs return objects.
- A web test renders a line of each kind and class with its classes.
- docs/design/web-ui.md: the live tail paragraph (§4.2) and "What logs
  yield" say lines carry a kind and a tool class.

## Live tail: render agy's real step_update format, with kinds

```issue
key = "tail-agy"
depends = ["tail-kinds-claude"]
```

agy's tail is the hardest to read because the renderer does not match agy
1.4.12's real stream. `renderAgyLog` looks for `step_update.tool_call` and
`tool_calls`, which never occur. So a live agy tail shows almost nothing
until the `result` event, plus stray non-JSON lines. The fixture
`internal/runs/testdata/agy-1.4.12.stream.jsonl` has no tool step, and
`TestRenderLog_Agy` tests the invented format.

The real stream, from the 2026-11-02 runs (r-3a38b5eb, r-ed439ad7):

- `{"event":"init","init":{"model":…,"tools":[…]}}`
- `step_update` with `step_type`, `state` (`ACTIVE`, `DONE`, `ERROR`) and
  `step_index`:
  - `agent_response`: text streamed as `text_delta` pieces on `ACTIVE`
    steps; `DONE` carries `usage`.
  - `tool`: `tool_name` plus `tool_info: {name, parameters, output,
    error}`. `run_command` output starts `Command exited with code N.`
  - `system_message`, `user_input`.
- `{"event":"result","result":{"status":…,"response":…,"usage":…}}`
- non-JSON lines such as `root agent idle; waiting up to 30m0s for 1
  background task(s)`.

### Parts of speech and how to find them

- **tool**: a `tool` step. Emit once per `step_index`: on `DONE` or
  `ERROR`, or on `ACTIVE` while it is the latest step, so a running command
  shows. `Tool` is `tool_info.name`. The argument comes from `parameters`:
  `CommandLine`; `AbsolutePath`/`Path`/`TargetFile` (with
  `StartLine`–`EndLine` for `read_file`); `Query`/`SearchPath`; for
  `call_mcp_tool`, `ServerName.ToolName`.
- **text**: concatenate `text_delta` by `step_index` for `agent_response`
  steps, and emit the first non-blank line when the step is `DONE` (or the
  text so far while it is the latest `ACTIVE` step).
- **error**: `state: "ERROR"` (first line of `tool_info.error.message`), and
  `run_command` output starting `Command exited with code N` with N ≠ 0.
- **status**: `init` (`session started: <model>`), `system_message` steps,
  `result` (`status`, turns, tokens), and the non-JSON lines.

### Acceptance criteria

- A new fixture, trimmed from a real 1.4.12 run, replaces the invented
  test. It covers a couple of each step type, an `ERROR` tool step, a long
  `text_delta` sequence and the `result`, and has a golden output with
  kinds.
- The digest (final message and usage from `result`) is unchanged.

## Live tail: kinds for codex and kimi

```issue
key = "tail-codex-kimi"
depends = ["tail-kinds-claude"]
```

Fill in the kinds for codex and kimi, the same way the claude issue does:
take kinds from each log's structure, and add error and status lines. Both
tails are readable today. What changes is color, a few missing lines, and
keeping kimi's raw output from reading as prose.

### codex (`--json` items)

- **text**: `item.completed` `agent_message` (as today).
- **tool**: `command_execution` (`Bash: <command>`, today) and
  `mcp_tool_call` (today). Also `file_change`, which is dropped today:
  `Edit: <path>` per entry in `changes`, with its `kind`
  (add/update/delete).
- **error**: a `command_execution` with `exit_code` ≠ 0 gets an extra
  `error` line, `exit <N>: <first line of aggregated_output>`.
- **status**: `thread.started`, `turn.completed` with its usage
  (`turn done: <input> in, <output> out`), and the non-JSON preamble
  `Reading additional input from stdin...`.

### kimi (JSON messages, plus raw tool stdout as plain lines)

kimi writes each tool's raw stdout to the log as plain, non-JSON lines,
between its JSON messages (2,947 such lines in r-e93330ba: diffs, `git log`,
source files). `renderKimiLog` passes every non-JSON line through, so they
read as the agent's prose. This is the one harness where the renderer has
to guess.

- **text**: `assistant` `content` (as today).
- **tool**: `assistant` `tool_calls` (as today), with `Tool` set to the
  function's name.
- **output**: non-JSON lines are raw tool output. Mark them `output`, so
  they are dimmed, and cap a run of them to its first few lines plus `…
  (N more lines)`, so a long file read doesn't push everything else out of
  the 25-line tail.
- **status**: non-JSON lines matching `^Waiting \d+s / .* background
  tasks? still running$`, keeping only the latest of a consecutive run;
  `meta` messages (`system.version`, `session.resume_hint`).
- **error**: `tool` messages whose content starts with `Command failed with
  exit code`, `Failed to `, or `Error:` (first line). These are heuristics,
  so keep them few and exact.

### Acceptance criteria

- `codex-0.166.0.stream.jsonl` gains a `file_change` and a failing command,
  and `kimi-2.4.1.stream.jsonl` gains interleaved raw output, a waiting
  sequence and a failed command, all trimmed from real runs. Each has a
  golden output with kinds.
