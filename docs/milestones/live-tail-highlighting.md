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

### What the logs contain (from the runs of 2026-11-02)

| Harness | Format | Text | Tool calls | Errors | Status |
|---|---|---|---|---|---|
| claude | stream-json | `assistant` `text` blocks | `assistant` `tool_use` | `user` `tool_result` with `is_error`, `system/permission_denied` (28 in 4 runs) | `system/init`, `task_started`, `task_notification`, `rate_limit_event`, `result` |
| agy | `step_update` events | `agent_response` steps streamed as `text_delta` | `tool` steps: `tool_name`, `tool_info.parameters` | `state: "ERROR"` with `tool_info.error`; `Command exited with code N` | `init`, `system_message` steps, `result`, non-JSON `root agent idle; waiting…` |
| codex | `--json` items | `agent_message` | `command_execution`, `file_change`, `mcp_tool_call` | `exit_code` ≠ 0 | `thread.started`, `turn.started`, `turn.completed` (usage) |
| kimi | JSON messages, plus raw tool stdout as plain lines | `assistant` `content` | `assistant` `tool_calls` | tool results starting `Command failed with exit code` / `Failed to …` | `meta` lines, `Waiting Ns / … background task still running` |
| crush | plain text: the final message only | everything | none | none | none |

Only crush needs heuristics for parts of speech. Two of the current
renderers are also wrong, not just uncolored. **agy** reads `tool_call` and
`tool_calls`, which real agy 1.4.12 logs never contain (they use
`tool_name` and `tool_info`), so a live agy tail shows almost nothing until
the run ends. **kimi** passes its raw tool stdout (diffs, `git log`, source
files) into the tail as if it were the agent's prose.

The kinds: `text` (the agent's prose), `tool` (a tool call), `output` (tool
output, where a harness's tail already shows it), `error` (a failed or
denied tool call), `status` (session lifecycle and progress) and `plain`
(unclassified, the default). Tool lines also carry the tool's name and a
*class* (shell, read, edit, search, forge, browser, other), so the
`Bash:` / `Edit:` prefix can be colored the same way across harnesses.

The agy issue matters most: its logs are the hardest to read today.

## Typed log lines: carry a kind from the renderer to the web UI

```issue
key = "tail-line-kinds"
depends = []
```

Make the live tail and the history log view able to show different kinds
of line, without changing what they look like yet.

### What to do

- In `internal/runs/log.go`, render to typed lines:

  ```go
  type LogLine struct {
      Kind      LineKind `json:"kind"`                 // plain, text, tool, output, error, status
      Tool      string   `json:"tool,omitempty"`       // tool name, for kind tool
      ToolClass string   `json:"tool_class,omitempty"` // empty until tail-palette
      Text      string   `json:"text"`
  }
  ```

  `RenderLog` returns `[]LogLine`. Every harness renderer still produces
  exactly the lines it does today, all `plain`, so this change is
  idempotent. Redaction (`RedactEncoded`) applies to `Text`.
- `GET /api/status` `live_runs[].log_tail` and `GET /api/runs/{id}/log`
  `lines` become arrays of these objects. The UI is embedded in the same
  binary, so there are no other consumers. Update the Go tests that
  compare rendered lines, and `web/src/types.ts`.
- Web UI: one small component renders a line as
  `<div class="log-line log-{kind}" data-tool-class={toolClass}>`, with the
  tool name in its own `<span class="log-tool">` when present. Use it in
  `LiveLogTail.svelte` and both history log boxes in `App.svelte`. Add one
  CSS rule per kind, all the same color for now. Follow/trim detection in
  `state.ts` keeps comparing line text.
- docs/design/web-ui.md: the live tail paragraph (§4.2) and the "What logs
  yield" note say lines carry a kind.

### Acceptance criteria

- `make check` passes, and the tail looks exactly as before.
- Unit tests: each renderer's existing cases still give the same text, all
  `plain`. The API returns objects. A web test renders a line of each kind
  with its class.

## Palette and tool-class highlighting

```issue
key = "tail-palette"
depends = ["tail-line-kinds"]
```

Give each kind and each tool class its color, and classify tools the same
way for every harness.

### What to do

- A shared classifier in `internal/runs`, `toolClass(name string) string`,
  fills `ToolClass` for `tool` lines, keyed on the names the harnesses use:
  - **shell**: `Bash` (claude, kimi, codex `command_execution` as rendered
    today), `run_command` (agy);
  - **read**: `Read`, `read_file`, `view_file`, `list_dir`;
  - **edit**: `Edit`, `Write`, `NotebookEdit`, `edit_file`, `write_file`,
    `replace_file_content`, `multi_replace_file_content`, codex
    `file_change`;
  - **search**: `Grep`, `Glob`, `grep_search`, `find_by_name`;
  - **forge**: `mcp__bottown__*` and bottown's MCP tool names, plus shell
    commands whose first word is `bottown` or ends in `/bottown`;
  - **browser**: `browser_*`, chrome-devtools MCP tools;
  - everything else: **other**.

  The shell and forge rules look at the command text, which is the
  heuristic part. Keep the table in one place with a test per row.
- The palette, on the tail's dark background:
  - `text`: the brightest color;
  - `tool`: the tool name colored by its class, with the argument in a muted
    tone;
  - `output`: dimmed;
  - `error`: red;
  - `status`: italic grey;
  - `plain`: today's color.

  Each color needs readable contrast (WCAG AA on the tail background).
  Colors are CSS custom properties, so a later theme can change them.
- A one-line legend (or `title` tooltips) in the tail header explains the
  colors.

### Acceptance criteria

- With the scaffolding's all-`plain` lines nothing changes visually. A web
  test with one line of each kind and each class checks the classes and
  custom properties are applied.
- Classifier unit tests cover every row above.

## agy: render the real step_update format, with kinds

```issue
key = "tail-agy"
depends = ["tail-palette"]
```

agy's tail is the hardest to read, and the renderer does not match agy
1.4.12's real stream. `renderAgyLog` looks for `step_update.tool_call` and
`tool_calls`, which never occur. Real tool steps carry `tool_name` and
`tool_info: {name, parameters, output, error}`, and assistant text streams
as `text_delta` pieces on `ACTIVE` `agent_response` steps. The fixture
`internal/runs/testdata/agy-1.4.12.stream.jsonl` has no tool step, and
`TestRenderLog_Agy` tests the invented format.

### Parts of speech and how to find them

- **tool**: a `step_type: "tool"` step. Emit once per `step_index` (on
  `DONE` or `ERROR`; on `ACTIVE` too when it is the latest step, so a
  running command shows). The name is `tool_info.name`, and the argument
  comes from `parameters`: `CommandLine`; `AbsolutePath`/`Path`/
  `TargetFile` (with `StartLine`–`EndLine` for `read_file`); `Query`/
  `SearchPath`; for `call_mcp_tool`, `ServerName.ToolName`.
- **text**: concatenate `text_delta` by `step_index` for `agent_response`
  steps, and emit the first non-blank line when the step is `DONE` (or the
  text so far while it is the latest `ACTIVE` step).
- **error**: `state: "ERROR"` (the line shows `tool_info.error.message`'s
  first line), and `run_command` output starting `Command exited with code
  N` with N ≠ 0.
- **status**: `init` (`session started: <model>`), `system_message` steps,
  the `result` event (`status`, turns, tokens), and non-JSON lines such as
  `root agent idle; waiting up to 30m0s for 1 background task(s)`.

### Acceptance criteria

- A new fixture, trimmed from a real 1.4.12 run (a couple of each step
  type, an `ERROR` tool step, a long `text_delta` sequence and the
  `result`), replaces the invented test. Its golden output lists kinds and
  text.
- The digest (final message and usage from `result`) is unchanged.

## claude: kinds from stream-json, plus errors and status

```issue
key = "tail-claude"
depends = ["tail-palette"]
```

claude's stream-json already names every part, so no heuristics are needed.

### Parts of speech and how to find them

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

- The fixture `claude-2.1.316.stream.jsonl` (extended with a denial, an
  error result and a background task, trimmed from a real run) has a
  golden output with kinds.

## codex: kinds from --json items, plus exit codes and file changes

```issue
key = "tail-codex"
depends = ["tail-palette"]
```

### Parts of speech and how to find them

- **text**: `item.completed` `agent_message` (as today).
- **tool**: `command_execution` (`Bash: <command>`, today) and
  `mcp_tool_call` (today). Also `file_change`, which is dropped today:
  `Edit: <path>` per change, with `kind` (add/update/delete) in the text.
- **error**: a `command_execution` with `exit_code` ≠ 0 gets an extra
  `error` line, `exit <N>: <first line of aggregated_output>`.
- **status**: `thread.started`, and `turn.completed` with its usage
  (`turn done: <input> in, <output> out`), plus the non-JSON preamble
  `Reading additional input from stdin...`.

### Acceptance criteria

- The fixture `codex-0.166.0.stream.jsonl` gains a `file_change` and a
  failing command, trimmed from a real run, and has a golden output with
  kinds.

## kimi: separate raw tool output from the agent's text

```issue
key = "tail-kimi"
depends = ["tail-palette"]
```

kimi writes each tool's raw stdout to the log as plain, non-JSON lines,
between its JSON messages (2,947 such lines in r-e93330ba: diffs, `git log`,
source files). `renderKimiLog` passes every non-JSON line through, so they
read as the agent's prose. This is the one harness where the renderer has
to guess.

### Parts of speech and how to find them

- **text**: `assistant` `content` (as today).
- **tool**: `assistant` `tool_calls` (as today), with `Tool` set to the
  function's name.
- **output**: non-JSON lines are raw tool output. Mark them `output`, so
  they are dimmed, and cap a run of them to its first few lines plus `…
  (N more lines)`, so a long file read doesn't push everything else out of
  the 25-line tail.
- **status**: non-JSON lines matching `^Waiting \d+s / .* background
  tasks? still running$`, keeping only the latest of a consecutive run.
  `meta` messages (`system.version`, `session.resume_hint`).
- **error**: `tool` messages whose content starts with `Command failed with
  exit code`, `Failed to `, or `Error:` (first line). These are heuristics,
  so keep them few and exact.

### Acceptance criteria

- The fixture `kimi-2.4.1.stream.jsonl`, trimmed from a real run with
  interleaved raw output, a waiting sequence and a failed command, has a
  golden output with kinds.

## crush: classify the final message once the transcript is captured

```issue
key = "tail-crush"
depends = ["tail-palette", "#166"]
```

crush logs today hold only the final message (`crush run --quiet`), so
every line is `text`. There is nothing else to classify until #166 makes
the binding keep the transcript.

### Parts of speech and how to find them

- Today: all lines `text`. Set that, instead of `plain`, so the final
  message gets the text color.
- After #166: map whatever structured events crush's transcript has to the
  same kinds (text, tool with its name, tool errors, status), as for the
  other harnesses. Look at a real log from the new binding first. If the
  transcript is plain text, use only prefixes crush prints itself, and
  write down the heuristics in the renderer.

### Acceptance criteria

- A fixture from a crush run after #166, with a golden output with kinds.
