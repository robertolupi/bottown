+++
title = "Monitor UI: a read-only live view in the browser and the terminal"
kickoff = 469
design = "docs/design/monitor-ui.md"
+++

The owner watches runs in the UI and acts in the terminal.
[The monitor UI design](../design/monitor-ui.md) rebuilds the UI around
that: a **Now** view of live log tails with the time since each run's last
output, one page per run and flow in place of three detail surfaces, a
read-only UI that shows `bottown` commands to copy where it had buttons,
and one command, `bottown ui`, that serves the web UI and shows the same
live view in the terminal.

Owner decisions, from #469 and its conversation: the UI is read-only;
stopping a run is Ctrl-C, so the UI has no stop; Now keeps a tail per run,
to see at a glance whether one is stuck; a click opens the whole log as a
page, not a side panel; waiting flows are one line each; the terminal
view is part of `bottown ui`, not a separate `top` command; its panes
split the screen height evenly and show several log lines each; the login
token stays valid in the URL shown at the top; Milestones, Timeline,
History and Stats stay in the browser.

Three steps, each usable when it lands. **The browser** (`web-*`) runs on
today's server: `web-routes` first, then the run page, then Now and
History in parallel, then read-only and keys. **The server**
(`flow-status`, `live-view`, `actions-removed`, `log-increments`) moves
the live picture into Go and removes the action routes. **The terminal**
(`term-ui`, `term-follow`) builds on `live-view` and waits for
`actions-removed`: it makes the login token reusable, which is acceptable
only for a server with no action routes. `flow-status` and
`web-routes` are independent and can start together.

Every issue that changes what the UI shows updates
`docs/design/web-ui.md` (the description of the UI) and the fixtures in
`web/fixtures`, and checks its layout claims with `bottown web shot`.
Out of scope (design §11): server-sent events, notifications, several
repositories in one terminal view, a setting for the quiet threshold.

## Web: addressable views, and a Repository view

```issue
key = "web-routes"
depends = []
size = "M"
```

Give every view an address (design §5). In `web/src`:

- A small fragment router: `#/now` (the default, and what an empty
  fragment means), `#/history`, `#/milestones`, `#/timeline`, `#/stats`,
  `#/repo`. Navigation pushes history entries (`history.pushState` or
  assigning `location.hash`), and the app listens for `hashchange`, so
  Back and Forward work and editing the address switches the view. The
  old fragments `#milestones`, `#timeline` and `#stats` resolve to the
  new routes. An unknown route shows Now.
- Split today's overview: `#/now` keeps Live Runs and the Owner Inbox as
  they are (the `web-now` issue rebuilds them); `#/history` gets the Run
  History panel as it is; `#/repo` gets Upstream & Forge and the Health
  Checklist as they are. `ViewTabs` lists Now, History, Milestones,
  Timeline, Stats, Repository.
- `RunHistoryState` and the live poll stay in `App.svelte`, so History
  keeps following live runs whichever view is shown.

No behaviour of a panel changes here; this issue only moves panels to
routes, so the next issues can replace one view at a time.

### Acceptance criteria

- Frontend tests: each route renders its view; a click on a tab adds a
  history entry and `history.back()` returns to the previous view; a
  `hashchange` to `#/stats` shows Stats; `#milestones` shows Milestones;
  an unknown fragment shows Now.
- `bottown web shot` metrics: Now no longer contains the Run History,
  Upstream & Forge or Health Checklist panels.
- `docs/design/web-ui.md` §4 lists the views and their routes.

## Web: one page for a run and for a flow

```issue
key = "web-run-page"
depends = ["web-routes"]
size = "L"
```

Add the page of design §4 and remove the three surfaces it replaces.

- Routes `#/run/<id>` and `#/flow/<issue>` (optionally `?pr=N`), one
  component. The header shows the target and title, kind, the flow's
  round, actor and model, elapsed against the limit, and links to the
  issue and PR in Forgejo. A flow, and a run that belongs to one, show
  the phase bar (implement, checks, verify, land) from the flow's step.
- **Run:** the whole log from `GET /api/runs/ID/log`, in the page's own
  scroll (no inner scroll box, no max-height), with the kind filters, the
  raw-log link and the follow toggle of `LiveLogTail`, and the run's
  denials. While the run is live, reload the log at the live poll's
  interval and keep the scroll at the end when following
  (`log-increments` later makes this incremental).
- **Flow:** tabs Log (the live run's log, else the last run's), Timeline
  (today's `FlowTimeline` from `GET /api/issues/N/timeline`) and Runs
  (the flow's runs, each a link to `#/run/<id>`).
- Open it from: a Live Runs card's title, a Run History row (a flow row
  opens the flow, a discuss or single-run row its run), a Timeline bar
  and lane, and a Milestones node's issue reference.
- Remove `TimelineModal`, `TimelineRunModal`, the inline expansion in
  `FlowThreadRow` with `ConstituentRuns`, the log toggle in
  `DiscussThreadRow` and `RunLogRow`, and the expanded-thread, timeline
  and log state in `history.svelte.ts` and the poll in `App.svelte` that
  only they use.
- Back returns to the view the owner came from, at its scroll position.

### Acceptance criteria

- Frontend tests: `#/run/<id>` renders the fixture run's log and
  denials; a live run's page shows new lines after a poll and stays at
  the end while following, and stays put when following is off;
  `#/flow/<issue>` shows the three tabs with the fixture's timeline and
  runs; a History flow row and a Timeline bar navigate to the right
  route; an unknown run id shows an empty state, not an error banner.
- `bottown web shot` metrics for `#/run/<id>`: no element with a
  max-height contains the log, and nothing overflows sideways at the
  narrow width.
- `svelte-check` and `make lint` find nothing left of the removed
  components.
- `docs/design/web-ui.md` §4.2 and §4.6 describe the page in place of
  the expansion and the modals.

## Web: Now, a wall of live tails

```issue
key = "web-now"
depends = ["web-run-page"]
size = "L"
```

Rebuild `#/now` as design §3.

- **Server, one field:** `LiveRunInfo` and `LiveCheckInfo`
  (`internal/webui/runs.go`) gain `last_output`: the newest modification
  time among the files the rendered tail is read from, which for a run
  are its log and its live transcript (`runs.ReadLog` appends the
  transcript, and `runs.FollowTranscript` rewrites it only when it
  changes), and for a check group its own log. It is omitted when none
  exists yet. Fixtures and the preview server carry it.
- **Tiles:** a grid (`repeat(auto-fill, minmax(420px, 1fr))`) with one
  tile per live batch run and live check group, ordered by start time. A
  tile has a two-line header (target and title; kind, round, actor,
  elapsed against the limit, the denials count) and a fixed-height tail of
  the last lines of `log_tail`, rendered with `LogLineView`, without the
  filter bar. A click anywhere on the tile opens `#/run/<id>`.
- **Last output and quiet:** each tile shows the time since
  `last_output` (since the start when absent), updated every second. At
  five minutes it reads "no output for Nm" in the warning colour and the
  tile gets a warning border. Tiles are not reordered. Keep the threshold
  in one exported constant; `live-view` moves it to the server.
- **Rows:** under the grid, one line per active flow that has no live
  run or check (its label and how long it has waited) and per interactive
  run. Each opens its page.
- **Needs you:** the owner inbox as one line of counts by kind, with an
  expand control; expanded, it shows today's items. The expanded state is
  kept in `localStorage`, read and written inside try/catch. "Show
  actor-filed issues" moves into the expanded list. Inbox zero is one
  muted line.
- **Header:** a health chip ("healthy", or "N problems" from the health
  checklist's failing items) and an upstream chip (the default branch
  against origin: "up to date", "N ahead", "N behind"), both linking to
  `#/repo`; **Refresh** moves next to the upstream chip. A freshness chip
  reads "live · Ns ago" from the last successful live poll and "stale ·
  Ns" after 30 seconds without one (the poll's errors are no longer
  ignored silently).
- **Just ended:** one line naming up to five threads that ended in the
  past hour, from the history the app already loads, each a link.
- Durations use hours past 60 minutes (`3h 12m`, not `192m`).
- Remove `FlowGroupCard`, `LiveRunCard` and `LiveCheckCard` when nothing
  uses them. The Start Flow, Start Run and Clean up buttons stay in the
  view's header until `web-read-only`.

### Acceptance criteria

- Frontend tests: one tile per batch run and check group, in start
  order; the last-output text for a fresh, a quiet and a log-less item,
  and the quiet class at the threshold; a waiting flow and an interactive
  run are rows, not tiles; the inbox counts by kind, expands, and
  remembers it; the freshness chip turns stale when polls fail; the
  health and upstream chips for a healthy and an unhealthy fixture.
- `bottown web shot` with the default fixture at the wide width: the
  Needs-you strip and every tile's top are within the first viewport
  height, and the page does not overflow sideways at either width.
- Go tests: `last_output` from a log's mtime; a stale log with a fresh
  live transcript gives the transcript's time, so the item is not quiet;
  absent without either file.
- `docs/design/web-ui.md` §4.1 and §4.2 describe Now.

## Web: History as its own view, without the sideways scroll

```issue
key = "web-history"
depends = ["web-run-page"]
size = "M"
```

Slim `#/history` (design §5). The table has five columns: **Target**
(issue and PR references, the title, and the thread's kind and run count
as a muted suffix, with the summary as a second, clipped line),
**Actors**, **State** (one badge: the thread's status, which already
folds in the review outcome for merged, ready, needs-changes and
needs-intervention threads; show the outcome badge only when the status
does not imply it), **Duration · cost**, and **Last activity** as a
relative time with the exact time as its title. Remove the Kind, Outcome,
Usage, Summary and Actions columns; token counts move to the run page. A
row opens its page (`web-run-page` wired the click). Failure reasons stay
visible on the row, under the summary. The filters and the sticky head
stay. The table fills the view's height instead of a fixed panel height.

### Acceptance criteria

- Frontend tests: the five columns; a failed thread shows its observed
  reason on the row; the state badge for a merged, a ready, a
  needs-changes, an in-progress and a failed thread; filters still
  narrow the rows.
- `bottown web shot` with the default fixture at 1280px wide: the
  History table's `scrollWidth` equals its `clientWidth`.
- `docs/design/web-ui.md` §4.2 describes the columns.

## Web: read-only, with commands to copy

```issue
key = "web-read-only"
depends = ["web-now", "web-history"]
size = "M"
```

Remove every action from the frontend and show commands instead (design
§6).

- A `CommandCopy` component: the command in a monospace chip with a copy
  button that uses `navigator.clipboard` and falls back to selecting the
  text, with "copied" feedback.
- Inbox items show `item.action.command` with `CommandCopy` in place of
  the action button. In `internal/webui/inbox.go`, the Land item's
  command becomes `bottown merge-all N` (today `bottown land !N`, which
  is not a command), and the stopped-flow and conflicts items carry
  `bottown flow N --implement A --verify B` with the pair of the flow's
  last runs, or the upper-case placeholders `ACTOR` when it is unknown.
- Milestones: a ready node shows its flow command (from the node's
  implement and verify labels, placeholders otherwise) instead of the
  Flow and Run buttons; a plan that can start shows
  `bottown milestone run SLUG --agents POOL --parallel N` instead of Run
  milestone (both flags are required and the UI does not know the pool,
  so `POOL` and `N` are placeholders); the milestone
  run's Stop button goes. A click on a node opens `#/flow/<issue>`.
- Repository: `git pull --ff-only bottown <default>` when behind the
  forge, `git push origin <default>` when ahead of origin, and each
  declared after-pull step, all as `CommandCopy`; SHAs are shown short
  with a copy control.
- The run page shows `bottown run show ID`.
- Remove the Start Flow, Start Run and Clean up buttons, every Stop
  button, `FlowModal`, `RunModal`, `MilestoneRunModal`, `StopModal`,
  `GCModal`, `PushModal`, `AfterPullModal`, `actions.ts`, the action
  payload builders and same-family check in `state.ts`, and the action
  banner. Refresh stays. Errors that remain (a failed load) show in a
  fixed-position notice that is visible wherever the page is scrolled.

The server's action routes stay registered; `actions-removed` deletes
them.

### Acceptance criteria

- Frontend tests: no element posts to `/api/actions/` except Refresh;
  each inbox kind with an action shows its command; a ready Milestones
  node shows its flow command and a click opens the flow page; the
  Repository view's three command cases; `CommandCopy` writes the
  command to the clipboard and falls back when the API is missing.
- Inbox tests in Go: the Land item's command is `bottown merge-all N`;
  the flow commands with a known and an unknown pair.
- `svelte-check` and `make lint` find nothing left of the removed
  dialogs.
- `docs/design/web-ui.md` §5 says the UI is read-only and lists the
  commands it shows.

## Web: keyboard navigation

```issue
key = "web-keys"
depends = ["web-now", "web-history"]
size = "S"
```

The keys of design §5, handled in one place and ignored while an input,
select or textarea has focus or a modifier is held: `1`–`6` switch
views; `j`/`k` and the arrow keys move a visible selection among Now's
tiles and rows and History's rows; `Enter` opens the selected item's
page; `Esc` goes back (`history.back()`, or to Now when there is nothing
to go back to); on a run or flow page `f` toggles following and `g`/`G`
go to the top and the end of the log; `/` focuses History's search; `?`
shows the keys in a small panel that `Esc` closes. The selection is
visible, scrolls into view, and is reachable by Tab as well (tiles and
rows are focusable and announce themselves as links).

### Acceptance criteria

- Frontend tests: each key on the view where it applies; keys typed in
  History's search field do nothing else; `Esc` on a page opened by a
  direct link goes to Now.
- `docs/design/web-ui.md` §4 lists the keys.

## Flow: a status file next to the lock

```issue
key = "flow-status"
depends = []
size = "M"
```

Let any reader tell what a flow is doing, whoever started it (design
§7.3). While it holds its lock, a flow writes
`.bottown/runs/flow-<repo>-<issue>.json` next to the lock file, at every
step change: `pid`, `issue`, `implement`, `verify` (with their model
specs), `rounds`, `round`, `step` (`implementing`, `checks`, `verifying`,
`waiting for actor`, `between runs`), `detail` (for a wait: the actor
and the run it waits for) and `since`. Write it atomically (temporary
file and rename) and remove it when the flow ends normally. The flow
never reads it: its state still comes from the forge and the run records
(`docs/design/web-ui.md` §3.3).

Add `runs.FlowStatus(dir, repo, issue)` and `runs.HeldFlows(dir, repo)`:
the status of every flow whose lock is held (`runs.FlowHolder`), with a
zero status when the file is missing or unreadable, and nothing for a
file whose lock is not held. Wire the writer through `flow.Options` (a
callback the CLI sets), so `internal/flow` keeps no file knowledge, and
cover the flows `milestone run` and `merge-all --actor` start.

### Acceptance criteria

- Flow tests with a recording callback: the steps reported through an
  implement, checks, verify sequence, a wait for a busy actor with its
  detail, and the wait between rounds.
- `internal/runs` tests: atomic write; `HeldFlows` returns a held flow
  with and without a status file, skips a stale file whose lock is free,
  and survives a corrupt file.
- `docs/reference/runs/` describes the file as a view of the flow, not
  its state.

## Server: the live view, made once

```issue
key = "live-view"
depends = ["flow-status", "web-now"]
size = "L"
```

Move the live picture from the browser to the server (design §7.1,
§7.2). In `internal/webui`, `(*Server).Live(ctx, tail int) (Live, error)`
returns:

- `items`: live batch runs and check groups, oldest first, each with its
  id, kind, target references, title and URLs, its flow (issue, round,
  max rounds, step) when it has one, actor, family, model spec, start,
  limit, `last_output`, `quiet`, the last `tail` rendered log lines (12
  by default) and its denials count;
- `rows`: held flows (`runs.HeldFlows`) with no live run or check, with
  their step, detail and since, and interactive runs;
- `ended`: up to five threads that ended in the past hour, newest first;
- `summary`: running, quiet and waiting counts, and the inbox's counts
  by kind.

`quiet` is `now − last_output ≥ quietAfter` (five minutes, one constant;
from the start when there is no log); rows are never quiet. Serve it as
`GET /api/live?tail=N`; the preview server answers it from the fixture's
`live` section. The flow of an item comes from its run's record or its
check's issue, as `ComputeLiveRuns` and `ComputeActiveFlows` do now;
active flows come from held locks, not from the UI's `flow-<N>.json`.

Switch Now to `GET /api/live` and delete what it replaces in
`web/src/state.ts` (`groupLiveFlows`, `standaloneLiveRuns`,
`standaloneLiveChecks`, the quiet threshold) with their tests. History
keeps `GET /api/runs?state=live` for its reconciliation.

### Acceptance criteria

- Go tests with a fake server and a temporary runs directory: a flow's
  run, its checks, a standalone run and a landing's checks as items; a
  terminal-started flow between runs as a row with its status detail; an
  interactive run as a row; `quiet` on either side of the threshold and
  for a log-less item; the summary counts; `tail=N`.
- Frontend tests pass against the `live` fixture section, with no
  grouping logic left in TypeScript.
- `docs/design/web-ui.md` §6 and §7 describe `Live` and where each field
  comes from.

## Server: remove the actions

```issue
key = "actions-removed"
depends = ["web-read-only", "live-view"]
size = "M"
```

Delete what the read-only UI no longer calls (design §9):

- the routes `POST /api/actions/{pull,push,after-pull,flow,milestone-run,land,run,stop,gc}`
  and their handlers; `refresh` becomes `POST /api/refresh` (keep the
  Origin check on it), and the frontend follows;
- `internal/webui/actions.go` (the executors, `startBackground`,
  `FlowRecord`), `land.go` (the background landing worker, its records
  and `liveLandings`), the `land` kind's special cases in the log
  renderer and the frontend, and `getActiveFlow` with the
  `.bottown/flow-<N>.json` files;
- the preview server's action answers and the fixtures' `actions`
  section;
- the tests of all of these.

Add the guard of design §6: export the commands the server can suggest
(inbox, Milestones, Repository, the run page), each built with concrete
sample values in place of its placeholders, and a test in `internal/cli`
that resolves every one against the CLI's command tree, parses its flags
and validates the required ones (cobra's `ValidateRequiredFlags`), so a
missing required flag fails as an unknown one does (`git …` commands are
checked for their fixed shape only).

A landing started with `bottown merge-all` shows on Now through its
check items; say so where `docs/design/web-ui.md` described the inbox's
Land.

### Acceptance criteria

- A request to any removed route gets 404; `POST /api/refresh` without a
  matching Origin is refused as before.
- The suggested-commands test fails when a command names a subcommand or
  flag the CLI does not have, and when it leaves out a required flag
  (prove both with deliberately wrong samples in the test's own table:
  an unknown flag, and `bottown milestone run SLUG` without `--agents`
  and `--parallel`).
- `make lint` reports no dead code left behind.
- `docs/design/web-ui.md` §5, §7 and §8 describe a UI with one write
  route; `docs/design/merge-queue.md` §8 points at `bottown merge-all`
  for landing from the inbox.

## Server: log increments for the run page

```issue
key = "log-increments"
depends = ["web-run-page"]
size = "S"
```

`GET /api/runs/ID/log` gains `after=N` and every response gains
`stable` (design §4). `stable` is the length of the rendered log's
**stable prefix**: the lines before the first one that can still change
(a tool call still running, or the last line of a live log); for an
ended run it is the whole log. With `after=N` the response's `lines` are
the rendered lines from position N on. The run page loads the log once,
then polls with `after` set to the last `stable` it received, keeps its
first N lines and replaces everything after them.

Do not use a count of final lines as the cursor. `runs.LineTracker`
hands out a line as final while an earlier tool call is still running
(`TestLineTrackerHandsOutFinalLinesOnceInOrder` in
`internal/runs/follow_test.go`: "A running call
does not hold back the lines after it"), so when that call completes it
becomes final at a position before lines the page already has, and an
append-only cursor would skip it or duplicate what follows.

### Acceptance criteria

- Go tests, each asserting that prefix-plus-response equals the full
  rendered log after every request: a log that grows; a tool call
  running in the first response and completed in the second; **an
  unfinished call followed by final text, then the call's completion**
  (`stable` stays at the call until it completes); `after` at the end
  returns no lines; an ended run's `stable` is its length; a check's
  plain log; an `after` larger than the log is answered as a full load.
- Frontend test: for the same sequences, the page's lines after every
  poll equal a full load's.

## `bottown ui`: the server and the live view in one terminal

```issue
key = "term-ui"
depends = ["live-view", "actions-removed"]
size = "L"
```

Add `bottown ui [--port N] [--print] [--once] [--repo R]` (design §8),
and make `bottown web ui` an alias of it.

- It starts the local server as `web ui` does and, when its output is a
  terminal, shows the live view in the alternate screen (bubbletea, as
  `internal/cli/tail.go`): a header with the repository, instance and
  the web address; a summary line (running, quiet, waiting; the inbox's
  counts); one pane per item of `Live`; one line per row; a key hint.
- **Panes split the height evenly:** the lines left after the fixed
  lines are divided equally among the items. A pane is a rule carrying
  the item's header (target, title, kind and round, actor, elapsed
  against the limit) and its last-output age, then its share of log
  lines from the item's tail, rendered and coloured as the terminal tail
  renders them and cut to the width. A quiet pane's rule is in the
  warning colour and reads "no output Nm". A pane has at least three log
  lines; when the items do not fit, the panes scroll with the selection
  and the summary says how many are off screen. Recompute on resize and
  when items start or end. It calls `Live` in-process every two seconds,
  asking for as many tail lines as the tallest pane shows.
- **Keys:** `j`/`k` and arrows move the selection, `o` opens the web UI
  in a browser on this machine, `?` lists the keys, `q` and Ctrl-C quit
  and stop the server. `Enter` is `term-follow`'s.
- **The link:** the header shows the login URL, and it works for as long
  as the command runs. In `internal/webui`, accept the token on every
  request that carries it (today it is refused after its first use), set
  the session cookie and redirect to the address without the token.
  A new token is made at each start; the Host check and the 127.0.0.1
  listener stay. This is why the issue depends on `actions-removed`: a
  login URL recovered from history or scrollback must reach a server
  with no action routes.
- It does not open a browser by default. `--print`, or an output that is
  not a terminal, prints the login URL and serves until interrupted.
  `--once` prints one frame as plain text (the summary, each item's
  header with its last three log lines, the rows) and exits without
  serving, with exit status 0 whatever is running.

### Acceptance criteria

- Model tests in the style of `tail_test.go`, with a fake `Live`: the
  even split for one, three and seven items at a given height, with the
  remainder lines going to the first panes; the three-line minimum and
  the scroll with its off-screen count; a quiet pane; rows; a resize; an
  item ending while selected.
- `--once` output for a fixture `Live`, with no ANSI sequences when the
  output is not a terminal.
- `internal/webui` tests: the token works twice and sets the cookie each
  time; a wrong token is refused; the redirect drops the token.
- `bottown web ui --print` behaves as before.
- `docs/design/web-ui.md` §2 and §8 describe the command and the token;
  the fam guide and `docs/reference` name `bottown ui` where they named
  `bottown web ui`.

## `bottown ui`: follow a run's log

```issue
key = "term-follow"
depends = ["term-ui"]
size = "M"
```

`Enter` on a selected item shows its log full screen in `bottown ui`
(design §8): a header line with the item's target, kind, actor, elapsed
and last-output age, then the log read from the run's or check's log
file and rendered with `runs.RenderLog` and the terminal tail's styles.
It follows the end while the run is live; `f` toggles following, `j`/`k`,
the arrows, PgUp/PgDn and `g`/`G` scroll, `Esc` returns to the panes
with the same selection, `q` quits. When the run ends, the view stays on
its log and says how it ended. A row (a waiting flow, an interactive
run) has no log: `Enter` on it does nothing and the hint says so.

### Acceptance criteria

- Model tests: a growing log is followed; scrolling up stops following
  and `f` resumes it; a running tool call is replaced by its completed
  line; `Esc` restores the selection; a run that ends while followed; a
  very long line is cut to the width, not wrapped into the next row.
- `docs/design/web-ui.md` §8 lists the keys.
