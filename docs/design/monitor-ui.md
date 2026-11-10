# Design: a monitoring-first UI, in the browser and the terminal

Status: **design**, settled with the owner in issue #469 and the
conversation it was filed from. Nothing here is built. The plan that
builds it is [`docs/milestones/monitor-ui.md`](../milestones/monitor-ui.md).
It changes [the local web UI design](web-ui.md): its views (§4), its
actions (§5), part of its server APIs (§7) and the command that starts it.
Each issue of the plan edits `web-ui.md` to match what it builds, so that
document stays the description of the UI; this one is the record of why.

## 1. The problem

The owner uses the UI to **watch** concurrent runs and **acts in the
terminal**, with `bottown` commands. The UI was designed to do both
(`web-ui.md` §1: "offers the CLI's actions … as buttons"), and the acting
half gets in the way of the watching half.

Measured with `bottown web preview` and the default fixture at 1440×900,
at commit `a738fe2` (the layout has not changed since):

- **The overview is one scroll of about 4,000px.** Live Runs takes 1,586px
  for 8 active items, because every run is a full-width card with its log
  tail. The owner inbox starts 1,744px down, Run History at 2,316px,
  Upstream and Health at 3,477px. The tab is labelled "Inbox · Runs ·
  Health": three jobs in one view.
- **A flow shows in up to five places** (a Live Runs card, a Run History
  row, an inbox item, a Milestones node, a Timeline bar) **and has three
  detail surfaces**: the inline expansion in Run History, `TimelineModal`
  and `TimelineRunModal`.
- **Run History has 10 columns** and scrolls sideways at 1440px: Last
  Activity is clipped and Actions is off screen.
- **Views have no address.** The tabs call `history.replaceState` and
  nothing listens for `hashchange`, so Back does nothing, and an expanded
  flow or an open run cannot be linked or reloaded.
- **Seven dialogs and ten `POST /api/actions/*` routes** start flows and
  runs, stop them, clean up, pull, push, run after-pull steps and land
  PRs. The owner does all of these from the terminal.
- **A flow started in the terminal is half visible.** The UI knows an
  "active flow" only from `.bottown/flow-<N>.json`, which the UI itself
  writes when it starts one (`getActiveFlow`, `internal/webui/runs.go`).
  A flow started with `bottown flow` shows while one of its runs or checks
  is live, and disappears between them.
- **Nothing says whether a run is stuck.** A tail that has not moved for
  ten minutes looks like one that printed its last line a second ago.

Separately, the owner wants the same view in a terminal over SSH, where a
browser is a port forward away.

## 2. Goal and non-goals

**Goal.** One command, `bottown ui`, that shows what is running now, in
the terminal it was started in and in the browser, from the same data:

- at a glance, every live run with the end of its log and the time since
  its last output, so a stuck run stands out;
- one click (or Enter) from any run to its whole log;
- what waits on the owner, as a count that expands, with the command to
  run next to each item;
- nothing that changes a run, a flow, the forge or the checkout.

**Non-goals.**

- Acting from the UI. Every former button becomes the command it ran,
  shown to copy (§6). Stopping a run is Ctrl-C in the terminal that
  started it, or `bottown run stop ID`; the UI shows neither.
- Milestones, the Timeline, History and Stats in the terminal. They stay
  in the browser; over SSH, `ssh -L` reaches them.
- A redesign of Milestones, the Timeline or Stats. They keep their
  content, lose their buttons, and gain addresses.

## 3. Now

The first view, in place of "Inbox · Runs · Health". Top to bottom:

```
bottown  bottown/demo · prod        ✓ healthy   main · 1 ahead   live · 2s ago
Now   History   Milestones   Timeline   Stats   Repository
──────────────────────────────────────────────────────────────────────────────
Needs you   1 ready to land   2 failed   1 conflict                   expand ▾
──────────────────────────────────────────────────────────────────────────────
Live · 4 running · 1 quiet · 2 waiting
┌ #42 Retry webhook deliveries ────────┐ ┌ !57 Rate-limit the public API ─────┐
│ implement 1/3 · claude · 12m/60m  4s │ │ verify 2/3 · kimi · 18m/60m    6m ◀ │
│ Edit internal/webhook/deliver.go     │ │ Read internal/api/ratelimit.go      │
│ Bash go test ./internal/webhook/ ok  │ │ Bucket now refills per second.      │
│ Bash make check · running            │ │ denied: git push (verify runs …)    │
└──────────────────────────────────────┘ └─────────────────────────────────────┘
┌ #56 Refresh export caches ───────────┐ ┌ #44 Should exports stream CSV? ─────┐
│ checks e2e · 7c1d2e3 · 3m        11s │ │ discuss · codex · 23m/60m       38s │
│ --- PASS: TestE2EFlowResume (41s)    │ │ Read internal/export/writer.go      │
│ === RUN   TestE2EMilestoneRun        │ │ The writer already flushes per row. │
└──────────────────────────────────────┘ └─────────────────────────────────────┘
#53 Usage dashboard endpoint · flow 1/3 · waiting for agy (busy with #47) · 6m
#61 Rename the export flag   · implement · claude · interactive · 9m
```

- **Tiles.** One per live batch run and per live check group, in a grid
  that fills the width (`repeat(auto-fill, minmax(420px, 1fr))`: three
  across at 1440px). Each has a two-line header (target and title; then
  kind, the flow's round, actor, elapsed against the limit) and the last
  lines of the log, rendered as the live tail renders them today, without
  its filter bar. Tiles keep a fixed height and a stable order (by start
  time), so nothing jumps while the owner watches.
- **Last output.** Each tile shows the time since the run last wrote to
  its log. At five minutes it turns to a warning colour with "no output
  for 6m", and the tile gets a warning border: this is **quiet** (§7.2).
  Quiet tiles are not reordered.
- **Denials** stay on the tile as a count ("1 denied"); the list moves to
  the run's page.
- **One-line rows** under the grid, for what is live but has no tail:
  flows waiting for an actor or between runs, with what they wait for, and
  interactive runs (the owner is in their terminal).
- **Needs you.** The owner inbox, collapsed to one line of counts by kind.
  It expands in place to today's items, each with its link and its command
  (§6). The UI remembers whether it was expanded. "Show actor-filed
  issues" moves inside.
- **Header chips.** Health is "healthy" or "N problems"; upstream is the
  default branch against origin ("main · 1 ahead"). Both link to the
  Repository view (§5), which holds today's Upstream & Forge and Health
  Checklist panels.
- **Freshness.** "live · 2s ago" counts from the last successful poll. A
  failed poll is no longer silent: after 30 seconds without one, the chip
  turns to "stale · 40s". **Refresh** stays, on the upstream chip, because
  it also fetches origin (§10).
- **Just ended.** One line under the rows names the last few threads that
  ended in the past hour, each a link to its page, so a run that finishes
  does not vanish from the screen the owner was watching.

## 4. The run page and the flow page

One page replaces the Run History expansion, `TimelineModal` and
`TimelineRunModal`. A tile, a one-line row, a History row, a Milestones
node, a Timeline bar and an inbox item all open it.

```
← Now / #42 Retry webhook deliveries with backoff
implement · round 1/3 · claude (anthropic) · opus · batch · 12m / 60m   output 4s ago
[ implement ]──[ checks ]──[ verify ]──[ land ]
bottown run show r-1a2b3c4d ⧉      Forgejo #42 ↗   !— 
Log   Timeline   Runs                                   filter…   ● following
001 Run r-1a2b3c4d started
002 Bash bottown issue show 42 · completed
…
```

- **`#/run/<id>`** is one run or check: its header, its denials, and its
  whole log in the page's own scroll (no inner scroll box), following
  while the run is live. The kind filters and the raw-log link of today's
  tail are here.
- **`#/flow/<issue>`** (with `?pr=N` for a thread of that PR, as
  `GET /api/issues/N/timeline` takes today) is a flow: the same page with
  the flow's phase bar and three tabs. **Log** is the live run's log, or
  the last run's. **Timeline** is today's combined flow timeline.
  **Runs** lists the flow's runs, each linking to `#/run/<id>`.
- A run that belongs to a flow shows the flow's phase bar and a link to
  the flow page; opening a tile goes to the run, opening a History flow
  row goes to the flow.
- The page is a route, so Back returns to where the owner was, at the
  scroll position they left, and a reload stays on it.

**Loading the log.** The page loads the rendered log once
(`GET /api/runs/ID/log`). While the run is live it then asks only for
what is new: the endpoint gains `after=N`, which returns the lines after
the N-th final line and the lines still live (`runs.LineTracker` already
separates the two for the terminal tail). Until that lands (§12, step 2)
the page reloads the whole log at the live poll's interval while it is
open.

## 5. The other views, addresses and keys

**Views.** Now, History, Milestones, Timeline, Stats, Repository.

- **History** is today's Run History as its own view, with five columns:
  target (references, title, and "flow · 4 runs" as a muted suffix),
  actors, state (one badge: the thread's status, with the review outcome
  folded into it), duration and cost, last activity (relative). The
  summary is a second, clipped line under the target. A row opens its
  page. The filters stay. It does not scroll sideways at 1280px.
- **Repository** holds the Upstream & Forge and Health Checklist panels,
  unchanged but for their buttons (§6) and short SHAs with a copy
  control.
- **Milestones, Timeline, Stats** keep their content. A Milestones node
  opens the flow page; it no longer opens the Start Flow dialog.

**Addresses.** Fragment routes, pushed (not replaced) on navigation:
`#/now` (the default), `#/history`, `#/milestones`, `#/timeline`,
`#/stats`, `#/repo`, `#/run/<id>`, `#/flow/<issue>`. The UI listens for
`hashchange`. Today's `#milestones`, `#timeline` and `#stats` keep
working.

**Keys**, ignored while a field has focus:

| Key | Does |
|---|---|
| `1`–`6` | Switch view |
| `j` / `k`, arrows | Move the selection among tiles and rows |
| `Enter` | Open the selected item's page |
| `Esc` | Back |
| `f` | Follow or stop following the log |
| `g` / `G` | Top or bottom of the log |
| `/` | Focus History's search |
| `?` | Show these keys |

The terminal view (§8) uses the same keys.

## 6. Read-only: commands in place of buttons

The UI makes no change to a run, a flow, the forge or the checkout. Where
a button was, it shows the command, in a monospace chip with a copy
control. The server writes the command (it knows the pair, the slug, the
PR), the frontend only shows it.

| Where | Today's button | Command shown |
|---|---|---|
| Inbox: ready to land | Land | `bottown merge-all N` |
| Inbox: stopped flow, conflicts with base | Re-run flow | `bottown flow N --implement A --verify B`, with the flow's recorded pair |
| Inbox: broken or expired run | Stop run, Clean up | `bottown run gc` or `bottown run gc --failed` |
| Inbox: discuss answer, actor-filed issue | (link only) | none |
| Live Runs header | Start Flow, Start Run, Clean up | none: removed |
| Live run, flow | Stop Run, Stop Flow | none: Ctrl-C in its terminal |
| Milestones: a ready node | Flow, Run | `bottown flow N --implement A --verify B` from the node's labels |
| Milestones: a plan not running | Run milestone | `bottown milestone run SLUG` |
| Milestones: a milestone run | Stop | none |
| Repository: behind the forge | Pull | `git pull --ff-only bottown <default>` |
| Repository: after-pull steps | Run after-pull steps… | each declared step, to copy |
| Repository: ahead of origin | Push to Origin… | `git push origin <default>` |
| Run page | (none) | `bottown run show ID` |

The inbox's Land item carries `bottown land !N` today, which is not a
command; `bottown merge-all N` is. So that it cannot happen again, a test
builds every command the server can suggest and checks that the CLI's
command tree resolves it and parses its flags.

When a pair or a slug is unknown, the command is shown with an upper-case
placeholder (`--implement ACTOR`), never guessed.

## 7. The live view

Today the browser assembles the live picture: `web/src/state.ts` groups
runs into flows, separates standalone runs and checks, and decides the
poll's pace, from three lists (`live_runs`, `live_checks`,
`active_flows`). The terminal view needs the same picture, so the server
makes it once.

### 7.1 `Live`

`internal/webui` gains `(*Server).Live(ctx) (Live, error)`, served as
`GET /api/live` and called in-process by the terminal view (§8):

```
Live
  items    []Item     tiles: batch runs and check groups, oldest first
  rows     []Row      one-liners: waiting flows, interactive runs
  ended    []Ended    threads ended in the past hour, newest first, at most 5
  summary             running, quiet, waiting; inbox counts by kind
Item
  id, kind (run kind, or check/e2e), target refs and title, urls
  flow     issue, round, max rounds, step        when it belongs to a flow
  actor, family, spec, started, limit
  last_output, quiet
  tail     []LogLine  the last rendered lines: 12, or the caller's count
                      (`?tail=N`; a tall terminal pane asks for more)
  denials  count
Row
  target refs and title, what (the flow's step, or "interactive"),
  detail ("waiting for agy (busy with r-2b3c4d5e)"), since
```

`GET /api/runs?state=live` stays for the History view's reconciliation
until that moves too; the Now view and the terminal use only `Live`.

### 7.2 Last output and quiet

`last_output` is the modification time of the run's log
(`.bottown/runs/<id>.log`; a check group's own log). `quiet` is
`now − last_output ≥ 5 minutes`, one constant (`quietAfter`) in
`internal/webui`. An item with no log yet counts from its start. Rows are
never quiet: a waiting flow says what it waits for, and an interactive
run's silence is the owner's.

Five minutes is a first value. The longest silent step in a healthy run
is a tool call that prints nothing until it ends; the repository's own
`make check` streams. If it flags healthy runs, the constant changes in
one place, and a later change can make it a `.bottown.toml` setting.

### 7.3 Flows the terminal started

A flow holds `.bottown/runs/flow-<repo>-<issue>.lock` for its whole life
(`runs.HoldFlow`), whoever started it. The live view finds flows by the
locks that are held, not by the UI's `flow-<N>.json`, which goes away
with the UI's Start Flow.

The lock says only the pid. For the row's text, the flow writes a
**status file** next to it, `flow-<repo>-<issue>.json`, at each step
change: its pid, implementer, verifier, rounds, the step (`implementing`,
`checks`, `verifying`, `waiting for actor`, `between runs`), a detail
(the actor and run it waits for) and the time. It is written atomically
and never read back by the flow: `web-ui.md` §3.3 still holds, the flow
keeps no state of its own, and a missing or stale file costs the row its
detail and nothing else. The live view ignores a status file whose lock
is not held.

A flow with a live run or check is that item's `flow`; a held flow with
neither is a row.

## 8. `bottown ui`

One command starts the local server and shows the live view in the
terminal:

```
bottown ui [--port N] [--print] [--once] [--repo R]
```

```
bottown · bottown/demo · prod      web http://127.0.0.1:31338/?token=…
4 running · 1 quiet · 2 waiting    needs you: 1 ready to land, 2 failed, 1 conflict
▸ #42 Retry webhook deliveries · implement 1/3 · claude · 12m/60m ───── 4s ──
  Edit internal/webhook/deliver.go
  Bash go test ./internal/webhook/ · completed
  Bash make check · running
  !57 Rate-limit the public API · verify 2/3 · kimi · 18m/60m ── no output 6m ──
  Read internal/api/ratelimit.go
  Bucket now refills per second.
  denied: git push (verify runs cannot push)
  #56 Refresh export caches · checks e2e · 7c1d2e3 · 3m ──────────────── 11s ──
  --- PASS: TestE2EFlowResume (41s)
  === RUN   TestE2EMilestoneRun
  …
  #44 Should exports stream CSV? · discuss · codex · 23m/60m ─────────── 38s ──
  Bash bottown issue show 44 · completed
  Read internal/export/writer.go
  The writer already flushes per row.
  #53 Usage dashboard endpoint · flow 1/3 · waiting for agy (busy with #47) · 6m
  #61 Rename the export flag · implement · claude · interactive · 9m
j/k move · enter log · o open browser · ? keys · q quit
```

- **In a terminal** it takes the alternate screen, redrawn every two
  seconds: a header with the web address, the summary line, then one
  **pane** per item of `Live` and one line per row.
- **Panes split the height evenly.** The lines left after the header, the
  summary, the one-line rows and the key hint are divided equally among
  the items; each pane is a rule with the item's header and its
  last-output age, then that many lines of its log's tail, rendered and
  coloured as the terminal tail renders them, each cut to the width. A
  quiet pane's rule is in the warning colour and says "no output 6m".
  A pane never has fewer than three log lines: when the items do not fit
  at that height, the panes scroll with the selection and the summary
  line says how many are off screen. The split is recomputed when the
  terminal is resized or an item starts or ends.
- It does not open a browser: over SSH there is none to open. `o` opens
  it on the machine the command runs on.
- **The link at the top** carries the login token and works for as long
  as the command runs (the owner's decision). Today the token is
  one-time: the server trades it for a session cookie once and refuses it
  afterwards, so a link kept on screen would work only the first time.
  The server instead accepts the token on every request that carries it,
  sets the cookie and redirects to the address without it, as it does
  now. What this gives up: a login URL left in a browser's history or a
  terminal's scrollback stays usable until the command exits, where today
  it is dead after first use. The server still listens on 127.0.0.1 only,
  checks the Host header, makes a new token at each start, and after §9
  can change nothing. With `--port N`, `ssh -L N:127.0.0.1:N host` makes
  the link work from the owner's own machine.
- **Enter** shows the selected run's log full screen, following it, with
  the terminal tail's rendering and colours (`internal/cli/tail.go`,
  `runs.RenderLog`); `Esc` goes back. `q` quits and stops the server.
- **`--print`**, or an output that is not a terminal: print the login URL
  and serve until interrupted, as `bottown web ui --print` does today.
- **`--once`**: print one frame as plain text and exit, without serving:
  the summary, then each item's header with its last three log lines, then
  the rows. For scripts, and for a look over a slow link.
- **`bottown web ui`** becomes an alias of `bottown ui`. `web preview`
  and `web shot` are unchanged.

The terminal view and the browser read the same `Live` from the same
process, so they cannot disagree.

## 9. What goes away

| Area | Removed |
|---|---|
| Frontend dialogs | `FlowModal`, `RunModal`, `MilestoneRunModal`, `StopModal`, `GCModal`, `PushModal`, `AfterPullModal`, `TimelineModal`, `TimelineRunModal` |
| Frontend | `actions.ts`; the History inline expansion and its nested tables; the live grouping in `state.ts` (once `Live` is served) |
| Routes | `POST /api/actions/{pull,push,after-pull,flow,milestone-run,land,run,stop,gc}`; `refresh` moves to `POST /api/refresh` |
| Server | the action executors and the background landing worker (`internal/webui/actions.go`, `land.go`); the UI's `flow-<N>.json` and landing records; the preview server's action answers |
| CLI | the browser opening by default |

Landing from the inbox was built two days before this design (the
`web-ui-land` issue of the merge-queue plan). It is removed with the
rest: `bottown merge-all N` lands the same PR through the same
`land.Land`, in the terminal, and its checks show on Now as check items.

With no action routes, the server's write surface is one route,
`POST /api/refresh`, which fetches origin. The Origin check of
`web-ui.md` §8 stays on it.

## 10. Decisions on the issue's open questions

- **What makes a run quiet?** Five minutes without a write to its log,
  one constant (§7.2).
- **Does Refresh stay?** Yes, as the one control, on the upstream chip.
  It fetches origin, which the periodic reload does not do on demand. It
  changes nothing but the checkout's remote-tracking refs.
- **The clean base merge.** The flow stopped merging bases with the merge
  queue, and the inbox item became Land. Its command is
  `bottown merge-all N` (§6).
- **How does the terminal read the live view?** In-process: `bottown ui`
  runs the server, and the terminal view calls `Live` on it (§8). There
  is no separate `bottown top`.

## 11. Out of scope

- Server-sent events or websockets: polling at today's pace is enough for
  a view read by one person.
- Notifications (a run went quiet, a flow stopped).
- The terminal view for several repositories at once.
- Making the quiet threshold a setting, until five minutes proves wrong.
- Milestones, Timeline, History and Stats in the terminal.

## 12. Steps

The plan builds this in three steps, each usable when it lands:

1. **The browser, on today's server.** Routes, the run and flow page, Now,
   History, read-only, keys. The server gains one field (`last_output`)
   and one corrected command; no route is removed, so the step is easy to
   revert.
2. **The server.** The flow's status file, `Live`, the removal of the
   action routes and executors, log increments.
3. **The terminal.** `bottown ui` with the live view, then the log
   follower.
