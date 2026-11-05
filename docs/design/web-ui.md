# Design: the local web UI and the supervisor

Status: **design**, written with the owner in issue #55. Built so far:
run digests (phase 2, #60), per-actor run serialisation and `run stop`
(phase 1a, #58), and the single-issue supervisor (phase 1, #59).

## 1. Goal

Today the owner drives bottown with one CLI command per step: start an
implement run, wait, start a verify run with another family, read the
verdict, start a follow-up run, merge. This design adds two things:

- **The supervisor:** one command that takes an issue through the whole
  loop and stops where the owner has to act. It is the supervisor that the
  [problem statement](../problem-statement.md) describes (§4, §5.3), in its
  first concrete form.
- **A local web UI:** `bottown web ui` becomes a local HTTP server on the
  host that shows what is going on in one repository and its runs, and
  offers the CLI's actions, the supervisor's included, as buttons.

bottown will mostly host repositories other than itself, so nothing here
assumes bottown's own tooling.

This narrows one non-goal of the problem statement (§6, "Dashboards").
The forge stays the place for everything it can show, and the UI links to
it. What the forge cannot show is the work around it: runs, flows, their
cost, the host's git and actor worktrees. Without a view of those, driving
several agents means reading logs and running `git` by hand.

## 2. Architecture

```
browser ──localhost, one-time token──▶ bottown web ui   (host, owner's process)
                                         │  reads: run logs, actor worktrees,
                                         │         upstream origin (owner's creds)
                                         │  calls: supervisor, run commands (Go)
                                         ▼
                                       bottown server   (container)
                                         runs, keys, policy, audit, forge
```

- **`bottown web ui` runs on the host,** in the `bottown` process, started
  by the owner from a repository's `main` checkout like the other owner
  commands. It binds to localhost only and opens the browser with a
  one-time token.
- **One UI, one repository.** The UI is about the repository whose
  checkout it was started in: its inbox, its runs, its upstream. The owner
  starts it in the repository they want to work on, and starts another
  one for another repository. Nothing on the host records where
  checkouts are, and nothing needs to.
- **The UI is a view plus actions.** It reads what only the host has and
  calls the server's API as the owner for the rest. Its actions are the
  CLI's actions, through the same Go code and the same checks.
- **The server stays authoritative:** runs, keys, policy, the audit trail
  and the forge. Section 7 lists the read APIs it needs. The container's own
  page stays as a small health page (`bottown web health`).
- **The UI never duplicates Forgejo.** Whatever Forgejo already does
  (merging, reading and editing issues and PRs, reviews, milestones) is a
  link into Forgejo, where the owner is signed in with `bottown web
  forgejo`. The UI adds only what Forgejo cannot do: runs, flows, the
  host's git, and the views that combine them.
- **The supervisor is a CLI command and Go package first.** The UI calls the
  same package; it stays scriptable and testable without a browser.

## 3. The supervisor

Decided with the owner:

- **One issue per invocation first; a queue later.** `bottown flow 51`
  drives one issue to its stop point and exits. A later phase adds a
  long-running loop that takes issues from a queue (§3.6).
- **The supervisor never merges.** Every successful flow stops at
  "approved, ready for the owner to merge". Merging stays the owner's
  action, in Forgejo. The problem statement's `auto` mode is out of scope
  for this design.
- **Only actors the owner named.** For a single flow the owner names the
  implementer and the verifier. A later milestone flow (§3.7) gets a pool
  of actors from the owner and pairs them itself. Either way the
  supervisor never uses an actor the owner did not name, and never
  substitutes one for another. The cross-family rule is checked for each
  pair before anything starts.
- **Batch runs only.** The supervisor starts unattended batch runs.
  Interactive runs (the owner in a harness, using its slash commands) stay
  manual. The UI shows them, but the supervisor does not drive them.

### 3.1 CLI

```
bottown flow 51 --implement claude --verify kimi
```

- `--rounds N` (default 3): review rounds. The flow stops at the Nth
  request for changes, so at most N implement runs answer reviews, the
  first included. Three rounds means up to two follow-ups after requested
  changes, which matches the problem statement's escalation after two
  rejections.
- `--check-rounds N` (default 2): the implement runs the flow starts to fix
  failed post-implement checks (§3.2). They do not use up `--rounds`: a
  failed check is a mechanical fix, not a reviewer's judgement.
- `--check-timeout D` (default 60m): per post-implement check, apart from
  the runs' `--timeout`. Running out is a failure.
- `--repo R`: as for `run`; defaults to the checkout's repository.
- `--timeout D` (default 60m): per run (§3.5).
- `--serialize-actors`: start each run only when its actor has no other
  live batch run, and wait until then. By default an actor's runs on
  different issues run concurrently (§3.4).
- `--actor-wait D` (default 2h): with `--serialize-actors`, how long each
  wait for a busy actor lasts before the flow stops (actor busy).
- It is an owner command, run from the `main` checkout like `run`. It
  prints one line per step and ends with the stop reason. Exit code 0 means
  "ready to merge"; any other stop is non-zero, so scripts can tell them
  apart. On a terminal, the steps print above a live tail of the current
  run (§4.2, terminal tail).

### 3.2 The loop

```
          ┌───────────────────────────────────────────────┐
          ▼                                               │
  implement run (A) ──failed──▶ stop: run failed          │
          │                                               │
     PR at head H                                         │
          ▼                                               │
  post-implement checks at H ──failed── check rounds left? ┤
          │                             └──no──▶ stop: checks failed
       passed                                             │
          ▼                                               │
  verify run (B) at H ──failed / COMMENT──▶ stop          │
          │                                               │
          ├── APPROVED ──▶ stop: ready to merge           │
          └── REQUEST_CHANGES ── rounds left? ──yes───────┘
                                     └──no──▶ stop: out of rounds
```

**Post-implement checks.** Before a verifier sees a new head, the flow
runs the e2e commands `.bottown.toml` names on `main` (§4.4; default `make
e2e`) itself, with no agent involved: in a clean worktree of the
workspace's mirror detached at H (`.bottown/runs/flow-<issue>-<sha>`,
removed afterwards), one command after another with `sh -c`, stopping at
the first failure, within `--check-timeout`. When the e2e suite takes a
host-wide lock (#158), it does so in the suite, so the flow's checks wait
for other suites like any other. The log goes next to the run records,
`.bottown/runs/flow-<issue>-<sha>.e2e.log`, and the result is the commit
status `bottown/e2e` on H, written by the owner (description ending
"(flow)"), which `pr show` lists next to the agents' own reports.

- **Passed:** the verify run starts, and its prompt says the checks passed
  at H, with the log. The verifier still runs `bottown check` (`make
  check`), but not the e2e commands. The flow asks the server for a
  verify run at H (`POST /v1/runs` with `sha`), and the server pins the
  run at the head it reads only if that is H, else refuses it ("the head
  moved"). So a push during the checks, or while the flow waits for the
  verifier's other batch run to end, never gives a verifier a head the checks did not
  pass at: the flow goes round the loop and checks the new head first.
- **Failed:** no verifier starts. The flow comments on the issue ("checks
  failed", with the description and the log) and starts an implement run
  whose prompt has the failure: the failed command, its first failing
  lines with their line numbers in the log, and the log's path. It counts
  against `--check-rounds`; once those are used up the flow stops as
  "checks failed". There is no automatic retry: a flaky check is fixed,
  not hidden, and the note's log makes one easy to recognise.
- If that implement run pushes no new commit, the flow stops as "run
  failed".
- A repository whose `.bottown.toml` sets `e2e = []` has no
  post-implement checks: the flow goes straight to verify.

Implementers need not run the e2e commands before opening a PR, though
they may run the ones their change touches; the prompts say so.

A follow-up round is a new implement run on the same issue: it resumes
`issue/<n>` and the open PR, as today. Every new head gets a new verify
run.

If the PR's head moves during a verify run, bottown refuses the verdict, as
it does today. The supervisor then starts a new verify run at the new head
and does not count it as a round.

The PR's base can move too. The supervisor tests the merge with `git
merge-tree --write-tree` on the forge's refs, without a worktree. When the
branch is behind and the merge is clean, an attended CLI flow asks `Merge
main into issue/<n> for !N? [Y/n]`; the default is yes. An unattended flow
leaves the choice to an owner-inbox action. On yes, bottown creates a
two-parent commit saying the merge was automatic and pushes it as the owner.
The unchanged-patch approval rule below then carries its approval over, and
the flow continues at the new head. When the merge would conflict, the flow
stops as "conflicts with main" and reports the conflicting files; the owner
starts an implement run to resolve them.

Flows are implement → verify only. Discuss runs stay a separate owner
action (the UI offers it); a flow never starts one.

Every stop says why, on the issue as well as in the command's output, so
the owner sees it in Forgejo and in the UI:

| Stop | Meaning | The owner's next step |
|---|---|---|
| ready to merge | the verifier approved the PR's current head | review and merge |
| out of rounds | changes were still requested after N rounds | read the reviews; re-run with more rounds, steer it interactively, or take over |
| run failed | a run failed, crashed or timed out | read its handover, and the run log and the harness's last error line that the stop names; fix the issue text or the environment, then re-run the flow (with another actor, if the run ended before the agent did anything because its actor is out of quota or signed out) |
| verifier undecided | the verify run ended with `COMMENT` | read why; re-run, maybe with another verifier |
| changes requested | the head is approved, but a request for changes at an older head still blocks the merge (§3.3) | have that reviewer review again, or dismiss the review |
| conflicts with main | merging `main` into the PR would conflict | read the named files; start an implement run to resolve them |
| checks failed | the post-implement checks failed at the head and no check rounds remain | read the log the stop names; fix the cause (a flake gets its own issue), then re-run, maybe with more check rounds |
| actor busy | with `--serialize-actors`, the actor's other batch run, which the stop names, was still live after `--actor-wait` | see why that run has not ended; stop or revoke it, then re-run the flow |

### 3.3 State and restart

Following the problem statement (§4), the supervisor keeps no state of its
own: where an issue stands is read from the forge and the server's run
records each time. A killed flow is resumed by running the same command
again:

1. A run for this issue or its PR is still live on the server → refuse,
   naming the run (the supervisor never runs two steps of one issue at
   once).
2. No open PR from `issue/<n>` → implement.
3. An open PR at head H, by its review assessment (below):
   - the PR is behind and a trial merge is clean → offer to merge the base;
   - the trial merge conflicts → stop with the conflicting files;
   - approved → stop: ready to merge;
   - changes requested at H → a follow-up implement run, if rounds are
     left;
   - changes requested at an older head, while H is approved → stop:
     changes requested;
   - commented → stop: verifier undecided;
   - needs review → by the flow's `bottown/e2e` status at H: none → run
     the post-implement checks; success → verify; failure → an implement
     run to fix them, if check rounds are left, else stop: checks failed.
4. Rounds are counted from the PR's `REQUEST_CHANGES` reviews, and check
   rounds from the PR's commits whose flow `bottown/e2e` status is a
   failure (`GET /v1/forge/pr/gate`), so a resumed flow gets fresh rounds
   of neither. (Commits a force push drops from the PR no longer count.)

**Review assessment.** One function, `review.Assess` (`internal/review`),
decides where a PR's reviews leave it, and the flow, the PR list's review
state and the owner inbox all use it; the server computes it for `pr show`
(`assessment`) and `pr list` (`review_state`, and the whole `assessment`
of each open PR). The web UI never assesses reviews itself: the inbox,
Milestones and History show the assessment from `pr list`. It follows the forge's
branch protection (one approval from the allow-list, dismiss stale
approvals, block on rejected reviews) and adds the cross-family rule; it
never counts a review the forge would not:

- Dismissed reviews never count.
- `APPROVED` and `REQUEST_CHANGES` count only when official. Forgejo keeps
  one official verdict per reviewer, its latest, so a reviewer's new
  verdict replaces its earlier one. `COMMENT` reviews are never official.
- An official `REQUEST_CHANGES` blocks at any commit: the forge keeps
  blocking the merge after a push, until its reviewer reviews again or it
  is dismissed.
- An approval counts only at H and not stale, and only from the owner, an
  actor of another family than the author's, or an actor whose verify run
  at H the owner allowed to be same-family (`--allow-same-family`, read
  from the server's run records, never from review text).
- An approval at an older head **carries over** to H when the patch is
  unchanged (below). It must be official and undismissed, and pass the
  cross-family rule at its own commit, as it did before the move.
- An actor's `COMMENT` at H is an undecided verdict; the owner's is
  discussion.

Precedence, first match: a request for changes at H → changes requested;
a qualifying approval with a request for changes at an older head →
changes requested (held back); a qualifying approval at H → approved; a
carried-over approval → approved, with a reason ("approval at 7d27b2e
carried over: same patch rebased onto e50b14a"); an
actor's comment at H → commented; otherwise needs review, even when an
older head's request still blocks, because the next step is a verdict at
H. The decision names the review behind it: the newest of its kind.

Each surface presents the same decision its own way, on purpose:

| Decision | Flow | Inbox (mergeable, no live run) | PR list |
|---|---|---|---|
| approved | stop: ready to merge | ready to merge | `approved` |
| changes requested at H | follow-up implement run, or out of rounds | stopped flow: changes requested | `changes requested` |
| changes requested, older head | stop: changes requested | stopped flow: changes requested | `changes requested` |
| commented | stop: verifier undecided | stopped flow: verifier undecided | `commented` |
| needs review | verify run | nothing | `needs review` |

The shared fixtures in `internal/review/reviewtest` run through all three.

**Approval carry-over.** A rebase onto a newer main, or a merge of main,
moves the head without changing what was reviewed, and should not cost a
verify run. The patch is unchanged when the diff from each head's merge
base with the base branch to that head has the same `git patch-id
--stable` (`review.SamePatch`). That is the owner's rule, an empty `git
range-diff` between `<old-base>..<old-head>` and `<new-base>..<new-head>`,
compared as one diff, so a squash of the same change still counts. As
with range-diff, a change to the diff's context lines (main editing lines
next to the PR's) is a change, and so is any conflict resolution. The
server runs it in the container, on Forgejo's copy of the repository
(`BOTTOWN_GIT_ROOT`, default `/data/git/repositories`), for each official
approval at an older head of an open PR, and passes the matches to
`review.Assess` as `Policy.Carries`; `GET
/v1/forge/pr/carries?repo=R&number=N` returns them for one PR. The flow's ready-to-merge
comment, `pr show` and `pr list` and the inbox give the reason. `pr show`
also lists every carried commit (`carries`), whichever approval decides:
after a push the flow waits for Forgejo to mark reviews at older heads
stale, except at those commits, which Forgejo may leave unstale. An old
head that Forgejo has garbage-collected no longer matches.

Forgejo's branch protection has its own view. On a push it compares the
heads with a combined diff (`git diff --name-only NEW OLD BASE`); when no
file was changed by both the PR and main it leaves the reviews alone, and
the approval still counts there. Otherwise, under "dismiss stale
approvals", it marks the reviews stale and dismisses the approvals, with
the timeline message "New commits pushed, approval review dismissed
automatically according to repository settings". Forgejo has no setting
for "same patch": turning dismissal off would let an approval survive any
push. Reads never change that state: patch comparisons are cached per
repository, base, old head and new head, and `pr show`, `pr list` and the
carries GET only assess what Forgejo reports. When the flow finds a dismissed
approval whose patch carries over, its explicit reconcile step
(`POST /v1/forge/pr/carries`, owner only) undismisses one Forgejo dismissed
with that message, using the owner's token
(`POST …/reviews/{id}/undismissals`), records `review_carry_over` in the
audit log, and re-reads the PR. Forgejo counts an official, undismissed
approval even when stale, because bottown leaves "ignore stale approvals"
off, so the merge gate then agrees. A dismissal with anyone's own message
stands. The review keeps its old commit and stale flag in Forgejo: what
carried it over is the assessment's reason, not a new review.

Only approvals carry over. A request for changes at an older head keeps
its usual meaning, and a comment at an older head is still no verdict.
With `required_approvals` above 1 the forge asks for more approvals than
"approved" here means.

### 3.4 Concurrency

Batch runs started from `main` use their own run worktrees, so nothing
stops one actor from working on two issues at once; the subscription's rate
window is the real limit. So an actor's batch runs are not limited by
default; what the server serialises is the **target**. `POST /v1/runs`
refuses, with a 409 that names the live run, a second live implement or
plan run on the same issue (git checks its branch out in one worktree
only), and a second live verify run at the same pull request head (two
verdicts would race). Discuss runs change nothing another run needs.

Per-actor serialisation is opt-in: a request with `serialize_actor`
(`run implement`/`run verify --serialize-actors`) is refused while its
actor has another live batch run. `flow --serialize-actors` asks for it on
every run, and when refused waits and tries again, the wait doubling from
a second to a minute, until `--actor-wait` (default 2h) runs out and the
flow stops with "actor busy", naming the run that holds the actor. It
tells the refusal by the server's error code (`actor_busy`), not its
wording, and fetches the mirror only for the first try of each run. Checking in each waiting flow instead would race:
two flows started together (side by side in the UI, or by a milestone)
could both see the actor as free. The server checks and creates the run
in one update of its store, so neither check can be got round. Interactive
runs are not serialised and do not count. The `max_batch_runs` actor
setting of earlier versions is ignored if a desired-state file still sets
it.

Flows on different issues run in parallel, as separate processes. By default
there is no per-actor coordination between them; `--serialize-actors`
serialises each actor's batch runs.

### 3.5 Failures and budgets

A failed run stops the flow, with no retry: when an agent ends with
`--failed` it is usually blocked or unclear, which a retry does not fix.
The owner re-runs the flow after looking.

Each run has a time limit: `--timeout` (default 60 minutes). The
supervisor kills a run that exceeds it and ends it as failed.

Budgets are recorded and shown, not enforced. Where a harness reports
usage in its own events (claude's stream-json reports tokens and cost;
others may report less), the supervisor extracts it per run and puts it in
the stop comment and the UI. Unknown stays unknown, never estimated
(problem statement §5.3). Limits can come later, once the numbers exist.

### 3.6 Later: the queue

A queued issue has to say who works on it. It does so with **labels that
bottown manages**: `implement/<actor>` and `verify/<actor>`, one of each
per issue. The owner sets them through bottown when queuing a single
issue; a milestone flow sets them when it assigns an issue to a pair. bottown creates them for each
actor during provisioning, and only bottown sets or clears them, when the
owner queues an issue from the CLI or the UI (actors already cannot change
labels: the proxy policy keeps labels for bottown). An open issue with both
labels is queued; the long-running loop takes queued issues, runs each as
a flow with §3.4's actor-limit policy, and removes the labels when the flow
stops. The labels make the queue visible and searchable in Forgejo, and
they are state the supervisor can rebuild from the forge.

The label lifecycle in detail (ordering, what a stopped flow leaves on
the issue) is designed with the queue.

### 3.7 Later: milestones

See the [milestones design](milestones.md) for the full design of
`bottown milestone plan`, `check`, `apply`, the Web UI dependency DAG view,
and `bottown milestone run` on a `milestone/<epic>` integration branch.

```
bottown milestone M --agents agy,claude,kimi
```

A milestone flow takes the open issues of a Forgejo milestone and runs a
flow for each, in parallel, with implementer and verifier pairs taken from
the pool: #1 implemented by agy and verified by claude, #2 implemented by
claude and verified by agy, and so on. Nothing above stands in its way:

- each issue's flow is the single-issue flow of §3.2, with its own worktrees;
- with `--serialize-actors`, the server's per-actor check (§3.4) keeps the
  pool's actors from being over-committed, whatever the number of flows;
- its state is the milestone's issues, their labels (§3.6), PRs and
  reviews, so it resumes from the forge like a single flow;
- conflicts between its PRs, as the owner merges them one by one, are the
  "conflicts with main" stop (§3.2).

To be designed when it is built: how pairs are chosen from the pool (always
cross-family), the order of issues and their dependencies (Forgejo issue
dependencies: an issue that builds on another waits until it is merged),
and when a milestone flow is done.

## 4. Views

The first phases build three views: the owner inbox, runs (live and
history), and upstream and repository health, on one tab. A second tab,
Milestones, shows each Implementation Epic's dependency DAG
([milestones design](milestones.md) §2.7). A run detail view with the
transcript comes later.

### 4.1 Owner inbox

What is waiting on the owner in this repository. Items come only from
forge and run state, and an item goes away **only when the owner acts**: merges, comments,
closes, re-runs. There is no hidden "read" state, so the inbox can always
be recomputed.

| Item | Shown when | Goes away when |
|---|---|---|
| Ready to merge | an open PR's review assessment is approved (§3.3: a qualifying approval at the current head, from another family, the owner, or an owner-allowed same-family verify run, and no request for changes), it merges cleanly, and it is not behind | merged or closed, the head moves, a request for changes, or it starts to conflict |
| Merge base | an open PR is behind its base and the trial merge is clean, with no live run for the PR or its issue | the owner clicks **Merge main**, the head or base moves, or a run starts |
| Conflicts with main | an open PR no longer merges cleanly; the description names conflicting files when the trial merge found them | an implement run resolves them, or the PR is closed |
| Stopped flow | a run failed, or the PR's review assessment is changes requested (out of rounds, or held back) or commented (verifier undecided), or the flow's post-implement checks failed at the head (§3.2, §3.3) | the flow is re-run, or the issue is closed |
| Broken run | a run is dead (its harness exited without `run end`), its key expired while it was live, or a failed run left its worktree behind | the run is ended or cleaned up (`run gc` ends a run whose key expired, which can no longer `run end` itself, and keeps a dead run's record when it cannot revoke it; `run stop --revoke ID`, or `run gc --orphans`, ends a live run that no local launcher holds) |
| Discuss answer | a discuss run answered on an issue (the discussant's last comment by the run's end), and the owner has not commented there since | the owner comments, or closes the issue |
| Actor-filed issue | an actor filed an issue the owner has not triaged: not queued, no milestone, not closed | any of those |

Actor-filed issues are hidden by default. The **Show actor-filed issues**
checkbox reveals them; the inbox count and inbox-zero state follow the
current filter.

Each row links to the issue or PR in Forgejo (merging happens there) and
offers the matching bottown action (§5): re-run the flow, end or clean up
the run. Issue-backed rows show the full issue title next to the reference.
Once repositories declare checks (§4.5), "ready to merge" also requires them
to pass.

### 4.2 Runs

**Live.** Active runs: target, issue title, kind, actor and family, batch or
interactive, and elapsed time against the timeout. Runs of one flow are
grouped under it ("#51 · round 2/3 · verifying with kimi"). A batch run
shows a **live tail of its log**, readable rather than raw JSON (assistant
text and tool calls, one line each), refreshed while it runs; the UI is
on the host, so it reads `.bottown/runs/<id>.log` directly. Each line
carries a kind (`text`, `tool`, `output`, `error`, `status`, or `plain`
for harnesses whose parts are not told apart yet) and, for a tool call,
the tool's name, class (`shell`, `read`, `edit`, `search`, `forge`,
`browser`, `other`) and state (`running`, `completed` or `failed`). A call
has a concise summary such as `Read web/test/state.test.ts · completed`;
its full parameters and output expand in place, as does the full text of a
multiline assistant message. Tool results update their call in place, so
completion does not reorder the events. The tail and the history's log boxes
color lines by kind and tool names by class, with a legend in the tail's
header. Both also link to `GET /api/runs/{id}/log?raw=1`, which serves the
complete original log as redacted plain text for diagnosis. Encoded
attachment payloads are elided from both the rendered and raw views.

**Terminal tail.** `bottown flow`, and a batch `bottown run implement`,
`verify`, `discuss` or `plan`, show the same tail in the terminal when
their output is one. A bubbletea program in the normal screen keeps a view
at the bottom: the flow's state ("flow #51 · round 2/3 · verifying with
kimi"), the run's header (id, kind and target, actor and family, elapsed
time against its limit: the flow's `--timeout`, else the run key's
lifetime), and the lines still live, a running call with a spinner. Every
other line goes to the terminal's scrollback, once, as it becomes final:
`runs.LogFollower` re-reads the log and the live transcript about once a
second, re-rendering only when either file changed, and
`runs.LineTracker` hands out a line once it is not a running call and not
the last line, which the next write may still extend, matching lines by
position. When the run ends every line left is printed, so the whole log,
a failed run's included, stays readable in the scrollback, after the
`log:` line that names the file; nothing is only in the view. Assistant
messages and errors are printed whole, in the colours of the legend above
in the terminal's ANSI palette, without escape sequences from the log. The
launcher's and the flow's own lines print above the view too. An attended
flow's merge prompt pauses the program and gives the terminal back for
the answer. Ctrl+C interrupts the command as without the tail. Output that
is not a terminal (piped, redirected, the web UI's background flows),
`TERM=dumb`, interactive runs, and `bottown flow --unattended` keep the
plain output: one line per step and the `log:` path.

Issue references in Live Runs, Run History and Owner Inbox show the issue
title and Markdown description in a popup on hover or keyboard focus. The
browser loads `GET /api/issues/{number}` on first display or interaction,
sharing and caching concurrent and completed reads; a failed read affects
only that popup. The
title stays plain text and rendered description HTML is sanitized.

**History.** A flow-centric table of history threads. Each flow thread groups
`implement` and `verify` runs for the same issue and PR (including manual
runs, retries, stops/resumes, repeated rounds, and pre-PR implementation runs
that stay in the thread once its PR is opened; a different PR on the same
issue forms a separate thread). `discuss` runs appear as separate entries.
The main line shows the issue, PR, and milestone name together (omitting the
milestone when none is assigned), along with latest status, active flow
highlighting, run count, actors, duration, usage, summary, and last activity.
Clicking a flow expands it (at most one flow is expanded at a time) to show a
combined flow **timeline** at the top — covering all runs in the flow with
clear run boundaries, reviews, comments, and merge events — followed by the
constituent runs and their individual logs and policy denials. A run's start
stays compact; its summary appears once, on its completion event, including
for a failed run when one was recorded. Live activity
updates statuses, highlighting, and the selected active flow's timeline
without changing the selected flow.

The table's header stays at the top of its scroll box as the rows scroll.
A summary cell, and each line of a failure reason, shows one line, cut
with an ellipsis; when that hides text, **more** shows all of it in the
cell, wrapped, and **less** cuts it again (#133). Neither expands the flow.

**Status and outcome.** A run's status says whether it executed
(succeeded, failed, revoked); its **outcome** says what work it produced,
as the server recorded it from the run's tools: a verify run's latest
review verdict (Approved, Changes requested, Commented) and an implement
or plan run's opened PR (PR opened). History rows, flow rows (the latest
run's), the timeline's run boundaries and the constituent runs show both,
the outcome as an outlined badge with a glyph and a label, so it reads
without color. A run with no recorded outcome, including every run from
before outcomes were recorded, says "None recorded": the outcome is never
guessed from a summary or log.

**Failure reasons.** A failed or revoked run's summary cell says why
(#132), in two labelled parts: what bottown **observed**, recorded in the
run's digest by whatever ended it, and what the **agent reported**, its
summary. The observed causes are a startup failure before the harness
ran, so with no log (the launcher's error), a non-zero harness exit (its
code), the flow's time limit, an owner's stop, a cancelled or vanished
launcher (`run gc` revokes its run), a key that expired while live, and
an agent ending its run with `run end --failed` (why is its summary). A
run with neither, such as one from before failures were recorded, or an
interactive run that exited cleanly and the owner marked failed, says
"Reason unavailable"; a cause is never guessed from the summary or log.
A flow row shows its latest run's reason unless the flow is going again.
Each reason links to the run: View log expands the flow, if needed, and
the run's log, whose row repeats the reason in full; View details does
the same for a run that wrote no log, and says so. The timeline's run
ended event carries the reason too.

**Flow-level state.** In addition to execution status and run outcome, flow
history rows show the next owner action derived from authoritative current
flow, PR, and review data (#155): Ready to merge, Needs changes, In progress,
Needs intervention, Needs review, or Merged. Stale approvals at an older head
do not imply readiness. Missing or read-failed data remains explicitly neutral
(Unknown), never inferred from summaries. The badge includes a glyph, label,
and screen-reader prefix so it is accessible without color, consistent with
Inbox and flow stop reasons.

**Denials.** A live run's card and a history row (when expanded) list
the operations the server's policy refused the run (denied or rejected),
including those outside its repository: when, the request or operation,
the rule, and why. History marks a run with denials with a count. They
come from `GET /v1/audit` (§7); while the server cannot say,
runs show none.

In-harness runs (started with a harness's slash commands) leave no log:
history shows them from the server's record and their handover comment,
marked interactive, with no usage.

**Stats** come later: success rate per actor and kind, rounds per issue,
and usage per issue.

**Retention.** Records and logs stay on the host until `bottown run gc`
removes them, which also purges the server's records of ended runs older
than `--older-than`. For history to outlive that, each run gets a
**digest** at run end: its record's fields, the stop or handover summary,
and the usage the harness reported. The host posts it to the server, which
keeps digests when `gc` purges full run records. History therefore
survives `gc` and a lost `.bottown/`. In-harness runs get a digest too,
from what the server knows plus the handover.

### 4.3 Upstream and repository health

**Upstream.** The checkout's `origin`, fetched with the owner's own
credentials when the page opens and on a Refresh button (no background
polling), shown with its age ("origin as of 08:40"):

- `main` against `origin/main`: ahead and behind, and the commits merged
  locally but not on `origin`, each with the PR it came from.
- **Push `main` to origin:** a button that lists the exact commits it will
  push and asks for confirmation, then runs `git push origin main` as the
  owner, from the checkout.
- No `origin` configured: "no upstream".

**The checkout against the forge.** The checkout's `main` against the
forge's `main`. When the checkout is behind (the owner merged in Forgejo
and has not pulled), the UI says so and offers **Pull**: `git pull
--ff-only bottown main`. After a pull it shows the steps the repository
declares for "after pulling" (§4.4); bottown itself would declare only
`make install`, which installs the CLI, refreshes the harness commands and
upgrades prod.

**Health checklist:**

- Agent instructions: an `AGENTS.md` (or README) on `main`.
- The forge's `main` is protected.
- Actor worktrees (from `git worktree list` in the checkout): each actor's
  HEAD against `main`, clean or dirty, busy with a live run, and its last
  run. Launched runs' own worktrees under `.bottown/runs` are listed too,
  marked with the run's kind and target; a permanent worktree shows as
  live only for the run it leases.
- The installed slash commands of each harness are current (what `bottown
  harness setup` would change), and the CLI and server versions match.

### 4.4 Repository settings

A repository declares what bottown cannot guess in a file in the
repository, `.bottown.toml`, versioned and reviewed like code. bottown
reads it from the forge's `main`, never from a PR branch, so a change to
it applies only after the owner merges it: an implementer cannot change
what applies to its own PR. A repository without the file gets the
defaults, and the health checklist says it has none.

```toml
# What the owner runs in the main checkout after pulling (§4.3).
after_pull = ["make install"]

# What `bottown check` runs (§4.5), in order, with sh -c; e2e only with
# --e2e. These are the defaults.
check = ["make check"]
e2e = ["make e2e"]
```

The file names commands; bottown runs them only when the owner asks
(after-pull steps are shown, or run on the owner's click with the command
in full view).

bottown resolves `main`'s head commit (`GET /v1/forge/branch`) and reads
the file at that commit (`GET /v1/forge/file`), as the owner; the
checkout's own copy is never read.
Unknown keys are an error, shown in the health checklist, so a misspelt
key is not silently ignored. After a Pull, the UI shows the steps in a
confirmation; on confirm it sends back the steps it showed, and the host
reads the file again and runs nothing unless they are still exactly
those. The steps run one at a time, with `sh -c`, in the main checkout,
and stop at the first that fails.

### 4.5 Declared checks

Implementers and verifiers run the checks `check` and `e2e` in
`.bottown.toml` name with `bottown check [--e2e]` (docs/index.md), which
records each run's result as the commit statuses `bottown/check` and
`bottown/e2e` on the commit it ran on, and `pr show` lists them by actor.
A status is what the run's actor reports it ran, not a verification.

`bottown flow` runs the `e2e` commands itself, as the post-implement
checks (§3.2): on the host, in a fresh worktree at the PR's head, after
the implement run and before the verify run, with the result as the
owner's `bottown/e2e` status on that head. A passing result is in the
verify prompt; a failing one sends the PR back to the implementer, and
shows in the inbox when the flow stops on it. Outside a flow (runs started
by hand) nothing runs the checks but the agents.

## 5. Actions

Each action is a CLI command, and the UI calls the same Go code with the
same checks. Actions marked "confirm" show what they will do, in full, and
ask first.

| Action | CLI equivalent | Confirm |
|---|---|---|
| Start a flow: implementer, verifier, rounds, timeout | `bottown flow N --implement A --verify B` | no |
| Start one run: implement, verify, or discuss with a question | `bottown run …` | no |
| Stop a live run or flow: kill the harness, end the run as failed | `bottown run stop ID` (new) | confirm |
| Clean up broken runs | `bottown run gc` | confirm |
| Pull the checkout | `git pull --ff-only bottown main` | no |
| Run the after-pull steps | the commands in `.bottown.toml` | confirm |
| Push `main` to origin | `git push origin main` | confirm |
| Merge the base into a clean, behind PR | answer `y` to the attended flow's prompt | confirm |
| Queue or unqueue an issue (later, §3.6) | sets or clears the bottown-managed labels | no |

Starting a flow or a run picks actors from lists of the instance's actors,
each with an optional model field: a model given starts the actor as
`ACTOR:SPEC`, and an empty field leaves the host's `[actor_models]` entry,
which the field shows as its placeholder, to apply. The UI refuses a same-family verifier before anything starts, as the CLI
does.

Merging pull requests, editing issues and PRs, reviewing, and milestones are
not actions here: the UI links to Forgejo for them. The clean base-merge
action only updates the PR's own branch.

## 6. Data sources

| Data | Where | How the UI gets it |
|---|---|---|
| Runs: live and ended, with digests and each run's state | server | `GET /v1/runs` (§7) |
| Policy decisions (allow/deny per forge write) | server | `GET /v1/audit` (new, §7) |
| PRs, with where their reviews leave each open one | server | `GET /v1/forge/prs?state=open\|all[&checks=1]` (`pr list`): each item carries the PR's branches, mergeability, milestone, dates, its `assessment` and rounds spent, and with `checks=1` the statuses at its head |
| One PR's reviews (bodies and times, for a timeline) | server | `GET /v1/forge/pr` (`pr show`) |
| Issues, with labels, milestone and, for markers, bodies | server | `GET /v1/forge/issues?state=…[&body=1]` (`issue list`) |
| One issue, with or without its comments | server | `GET /v1/forge/issue[?comments=0]` (`issue show`) |
| Issue dependencies, milestones, branches | server | `GET /v1/forge/issue/dependencies`, `/v1/forge/milestones`, `/v1/forge/branch` (§7) |
| Actors and their families | server | `GET /v1/actors` |
| Run records and logs of this checkout's runs | host, `.bottown/runs/` | read directly |
| Actor worktrees | host | `git worktree list` in the checkout, and run records in `.bottown/runs` |
| Upstream | host, the checkout's `origin` | `git fetch origin` with the owner's credentials |
| Harness slash commands | host, each harness's config | compared with what `harness setup` would write |
| Repository settings | `.bottown.toml` on the forge's `main` | `GET /v1/forge/branch` for `main`'s head, then `GET /v1/forge/file` at it (§4.4) |

**What logs yield** differs by harness, so each binding extracts what its
log has, at run end, into the digest:

| Harness | Final message | Usage |
|---|---|---|
| claude | the `result` event | tokens (summed over every model the run used), cost, turns and duration from the `result` event |
| agy | the `result` event | tokens (thinking counted as output), turns and duration from the `result` event; no cost |
| kimi | the last assistant message without tool calls | none known |
| codex | the last completed `agent_message` item | input (excluding cached), cached input as cache reads, output and turns, summed over `turn.completed` events; no cost or duration |
| crush | the assistant text from stdout, else the transcript's last assistant text | cost, from the transcript's session; its token counts are the last step's, not the run's, and are left out |

The live tail renders the same events, one line per assistant message or
tool call, each with a kind and, for a tool call, a tool class, state and
expandable parameters and output (§4.2). Multiline assistant text keeps its
full contents for expansion. Tool results complete their original calls in
place. Incomplete streaming records remain visible as plain text, and a
stream with no recognized events falls back to readable plain text rather
than disappearing. One table in `internal/runs` classifies tools by the names
the harnesses use; a shell command that runs `bottown` is `forge`. claude's stream-json
names every part, so its lines are typed: assistant text, tool calls,
denials, failed tool results, a `rejected` rate limit and a failed
result (with claude's error, such as an API error) as errors, and the
session start, background tasks, other rate limits than `allowed` and the
result (turns, cost, duration) as status. agy's stream-json is typed by its
`step_update` events, each step shown once, when it ends or while it is
the latest step: an `agent_response`'s streamed text, a `tool` step's
call (its argument from agy's parameter names, an MCP call as
`server.tool`), failed steps and commands that exit nonzero as errors,
and the session start, system messages, agy's non-JSON lines and the
result (status, turns, tokens, duration) as status. codex's stream is
typed: agent_message text, command_execution and mcp_tool_call calls
with their class, file_change edits, commands with non-zero exit code as
errors, and thread.started, turn completion with usage, and the preamble
as status. kimi's messages are typed: assistant text, tool_calls with
their class, tool results that start with failure messages and kimi's
own `error: failed to run prompt:` line as errors,
non-JSON tool output capped to a few lines, and the latest of
consecutive background task waits and meta messages (system.version,
session.resume_hint) as status. `crush run` prints only the final
message, so when a crush batch run ends the launcher appends the
session's transcript to its log (`crush session last --json`, if crush
created that session during the run), as JSON lines: the session's meta,
then each message. While the run goes on, the launcher exports the
transcript every 3 seconds into `<id>.transcript` beside the log,
replacing it whole; the tail reads it after the log, so Live Runs follows
the session as it grows, and the launcher removes it before appending the
final transcript. Its lines are typed: assistant text, tool calls with
their class, tool results crush marks as errors, commands whose output
ends `Exit code N` with N non-zero, and finishes other than a normal end
as errors, and the session start as status; output lines the assistant
text does not hold (an error crush printed) follow as `plain`. Before
crush starts its session, and in logs without a transcript, crush's lines
are its output, as `text`. The script fallback is still `plain`.
Unknown usage stays unknown.

## 7. Server APIs

The UI uses the server's existing API where it can. What changes:

- **PR base merges.** Owner-only `GET /v1/forge/pr/merge-base` reports
  whether the PR is behind, whether `merge-tree` was clean, and any
  conflicting files. `POST` takes the offered head SHA, repeats the check,
  creates the automatic merge commit and pushes it without force. A moved
  head, a conflict, or a live run on the PR, its issue or its branch is
  refused.
- **Run digests.** Ending a run (`DELETE /v1/runs/{id}`, from the launcher
  or from an agent's `run end`) can carry a digest: `summary` (cut to 4000
  bytes) and `usage`, whatever the host extracted, each field absent when
  unknown, and, for a run that did not succeed, `failure`: the observed
  `cause`, a `detail` (cut to 1000 bytes) and the harness's `exit_code`
  (§4.2, Failure reasons). The server stores it with the run, once: a
  second end changes nothing.
- **Run outcomes.** While a run is live, the server records its `outcome`
  on its record when its tools produce one: `review_submit` stores the
  verdict (`approved`, `changes_requested`, `commented`; a later review
  replaces it) and `pr_create` stores `pr_opened`. Ending the run keeps
  it; it is absent when none was recorded.
- **Purging keeps digests.** `POST /v1/runs/purge` (used by `run gc`)
  drops full run records, keys included, but keeps each run, with its
  digest, in a history, marked `purged`. `GET /v1/runs/{id}` finds it
  there too, and `bottown run show` prints it.
- **`GET /v1/runs`** (owner only) takes filters: `repo`, `state`,
  `since` (RFC 3339, created at or after) and `history=1`, which adds
  purged runs, so history works after `gc`. Every run carries its digest
  and its `state`, decided by the server (`secrets.Run.State`): `active`
  (not ended, key usable), `expired` (not ended, key expired: `run gc`
  ends it) or `ended`. The `state` filter takes one of those, or `live`
  for every run not ended. The UI shows these states and has no rule of
  its own; only whether a run's process is alive is the host's to know,
  from its own run records.
- **`GET /v1/audit`** (owner only): the policy decisions the server
  makes, stored rather than only logged: every refusal (`deny`, or
  `reject` for an unclean path) by the API and git proxies and the forge
  operations, and every write they let through (API writes, pushes,
  issues, PRs, comments, reviews). Allowed reads are only logged. Each
  entry has its time, actor, run, repository (`org/name`), operation
  (`api`, `git` or e.g. `pr_create`), method and path, decision, rule,
  detail and status. Filters: `repo` (`org/name`, or a name in the
  instance's org), `actor`, `run`, `decision` (`allow`, `deny` or
  `reject`, or a comma-separated list such as `deny,reject`), `since`
  (RFC 3339) and `limit` (the newest N); entries come oldest first. The
  server keeps them in `audit.jsonl` in its data directory for 14 days,
  and at most the newest 20000.
- **PR lists with their review state.** `GET /v1/forge/prs` (`pr list`)
  returns, for each PR, its branches (`head_branch`, `head_sha`,
  `base_branch`), `merged` and `merged_by`, `mergeable`, `milestone` (the
  title), `created`, `updated`, `merged_at` and `closed_at`, its
  `review_state`, and, for an open PR whose reviews could be read, the
  whole `assessment` (decision, deciding review, carry-over). The UI
  lists PRs only through it, so the review assessment is made in one
  place, the server. `changes_requested` counts the requests for changes,
  dismissed and stale ones included: the rounds the flow has spent, as
  the flow counts them (`server.ChangesRequested`). `checks=1` adds the
  statuses at each open PR's head, as `pr show` has them.
- **The UI's other forge reads.** `GET /v1/forge/issues` with `body=1`
  adds descriptions (agents' lists stay short); `GET /v1/forge/issue`
  with `comments=0` reads the issue alone; `GET
  /v1/forge/issue/dependencies?repo=&number=` lists what blocks an issue;
  `GET /v1/forge/milestones?repo=&state=` the milestones; `GET
  /v1/forge/branch?repo=&name=` a branch's head and whether a protection
  rule covers it; `GET /v1/forge/file?repo=&ref=&path=` a file's bytes at
  a ref the caller pinned. Each pages in full and stays within the key's
  repository.
- **Run concurrency** (§3.4): `POST /v1/runs` refuses a second live run on
  the same issue or pull request head, and, with `serialize_actor`, a batch
  run whose actor has another live one.
- **Later, for the queue:** provisioning creates `implement/<actor>` and
  `verify/<actor>` labels, and an owner-only endpoint sets or clears them.

The UI makes no Forgejo reads of its own: everything it shows from the
forge comes from these endpoints, so a rule (what counts as approved, as
live, as a round) is decided in one place.

## 8. Security

- **Localhost only.** `bottown web ui` binds `127.0.0.1` on a random port
  (or `--port`), checks the `Host` header against it (against DNS
  rebinding), and refuses state-changing requests whose `Origin` is not
  its own (against CSRF).
- **One-time token.** The URL it opens carries a token that is exchanged,
  once, for an `HttpOnly`, `SameSite=Strict` session cookie. The session
  lives as long as the process: when `bottown web ui` exits, it is gone.
- **The owner key never reaches the browser.** The Go process holds it and
  makes every server call itself.
- **Everything the agents wrote is untrusted:** issue and PR text, review
  bodies, handovers, logs. The UI escapes text, sanitizes the HTML produced
  from issue-description Markdown, and sets a strict Content-Security-Policy
  with no inline scripts, so text an agent controls cannot run in the page
  that can start runs or push.
- **Actions are the CLI's.** Each goes through the same Go code and checks
  as its CLI command, and the server enforces policy regardless of who
  calls. Repository-declared commands (after-pull steps) run only when the
  owner clicks, after seeing them in full, and only from `main`'s
  `.bottown.toml`.
- **Logs stay on the host.** Transcripts can contain secrets. The UI shows
  them locally, and only the digest goes to the server.
- **Flows outlive the UI.** A flow started from the UI is its own
  background `bottown flow` process, with its output in `.bottown/`.
  Closing the UI does not stop it; a restarted UI finds it through the
  server's run records.

### 8.1 Previewing the UI in a run

`bottown web ui` needs the owner's key and a `main` checkout, so a run that
changes the UI cannot use it. `bottown web preview` is for that run, and
for its verifier:

```
bottown web preview [--fixture NAME] [--port N] [--watch] [--detach] [--stop]
```

- **It runs anywhere,** a run or actor worktree included, and needs no
  instance: it never contacts prod or dev, and holds no key.
- **It serves the checkout's own UI.** It builds `web/` with vite into a
  temporary directory (running `npm ci` first when `web/node_modules` is
  missing or stale), prints the URL and serves until interrupted.
  `--watch` rebuilds when `web/src` changes; reload the page to see it.
- **`--detach` does not block the shell that starts it.** It builds and
  serves in a child of its own session, prints the URL and returns, so a
  script or an agent keeps its prompt. The child records itself in a temp
  file named for the checkout; `--stop` reads that file and ends the child.
  A bad `--fixture` fails before the build, and one detach at a time is
  allowed per checkout.
- **A state file is trusted only while it is still the preview.** Both
  `--stop` and the "already running" check confirm that the pid the file
  names still answers `GET /api/preview` with that pid, so a pid left over
  from a preview that crashed, was killed or outlived a reboot, and now
  belongs to an unrelated process, is left alone. A detached preview ends
  on its own after `--ttl` (default one hour), and a run stops one recorded
  in its scratch directory when the run ends, so an orphan cannot serve
  forever.
- **The API answers from a fixture,** `web/fixtures/NAME.json` (default
  `default`): the status, inbox, live runs with their logs, active flows,
  history, milestones, issue details and timelines, in the API's own JSON.
  A fixture is decoded strictly into the Go types the API uses, and a test
  loads every one, so a renamed field fails the tests instead of showing
  as a blank. `default` exercises every view; `empty` is a repository with
  nothing going on; `errors` has every error state. Actions do nothing and
  succeed, unless the fixture's `actions` gives their answer. A change
  that shows something new adds it to the fixtures.
- **Tests and people can change the state** while the page is open:
  `POST /api/preview/logs/RUN` appends lines to a run's log and live tail,
  `PUT /api/preview/fixture/SECTION` replaces a section, `POST
  /api/preview/load` reloads the fixture or switches to another, and `GET
  /api/preview` names the one in use and the pid serving it.
- **Same checks, no sign-in.** It binds `127.0.0.1`, checks `Host` and,
  on state-changing requests, `Origin`, and sets the same CSP as `bottown
  web ui`. There is no token: it serves only checked-in fixture data, and
  its actions do nothing.

Runs never get a signed-in UI: a second kind of login, and a read-only
mode on every endpoint, would cost security surface that preview, shot and
captured fixtures make unnecessary. When a bug depends on what the real
data looks like, the owner captures it:

```
bottown web preview --capture NAME [--issue N]... [--log RUN]... [--force]
```

- **It runs where `bottown web ui` does:** in the owner's main checkout,
  with the owner's key. In a run or actor worktree it refuses, saying it
  needs the owner's key.
- **It writes what the UI shows now** as `web/fixtures/NAME.json`,
  computed by the same code as `bottown web ui`'s API: the status, inbox,
  live runs, active flows, history and milestones. The file loads back
  strictly, as every fixture does, before it is written. It does not
  serve.
- **`--issue N` adds an issue's details and timeline, `--log RUN` a run's
  rendered log.** An issue or log named this way that cannot be read fails
  the capture. With neither, it takes the issues, issue-less PR threads
  and run logs the views link to, live work first, up to a cap (30 issues,
  20 logs), so popups and log views work in the preview; what it cannot
  read it skips, saying so. `actions` stays empty: a preview action does
  nothing.
- **It refuses to replace a fixture** without `--force`. It prints the
  path, and on stderr the count per section.
- **A capture holds real data:** issue titles and bodies, timelines, log
  lines from this forge. What looks like a secret is dropped: bottown API
  keys (`btk_…`) and the owner key, the one-time logins of `web ui`
  (`?token=`) and `web forgejo` (`/v1/web/login/…`), credentials in URLs,
  and encoded payloads, as in logs. Read it before committing it. The
  owner commits the fixture, or attaches it to an issue, for a run to
  serve with `--fixture NAME`.

The implement and verify prompts mention it when the repository's default
branch has `web/fixtures`.

### 8.2 Capturing the UI in a run

The UI is a Svelte SPA: what the preview serves at `/` is an empty shell,
so a run that can fetch the page still cannot see what it renders.
`bottown web shot` renders it in a headless browser and records what came
out, as numbers to assert on first and pictures second:

```
bottown web shot [--fixture NAME] [--path /#milestones] [--width 1440,390]
                 [--height 900] [--scheme light,dark] [--selector CSS]…
                 [--eval JS] [--wait CSS] [--out DIR] [--browser PATH]
```

- **One shot, then it exits.** It builds `web/` into `DIR/build`, serves
  it with the fixture as the preview does, on a random port, loads the
  page for each width and color scheme, writes the artifacts and stops
  the server and the browser. It never blocks the shell. `DIR` defaults
  to a new temporary directory.
- **Its output is paths,** one per line: `metrics.json` first, then each
  case's `WIDTH-SCHEME.png` (a full-page screenshot) and
  `WIDTH-SCHEME.dom.html` (the DOM after the scripts ran), then the built
  CSS and JS.
- **`metrics.json` is for runs and tests.** Per case: the `section`
  panels in document order (`order`, their headings) with their boxes;
  every element that scrolls or has a `max-height`, with `scrollHeight`,
  `clientHeight`, the computed `max-height` and whether it scrolls; the
  outermost elements that reach past the viewport's right edge
  (`overflowX`, what makes a narrow page scroll sideways); what each
  `--selector` matches; the `--eval` expression's value; and the page's
  console errors and uncaught exceptions, which are also printed. Svelte's
  `svelte-*` scoping classes are left out of element labels, as they
  change with every build.
- **A page counts as rendered** after its load event and half a second
  without network requests, so the fetches the UI makes on load have
  answered; `--wait` also waits for a selector.
- **It drives Chrome, Chromium or Edge** over the DevTools protocol, with
  a fresh profile it removes afterwards: `--browser`, else
  `$BOTTOWN_BROWSER`, else an installed one. Without a browser, or where a
  run's sandbox does not let it start one, it fails with that reason and
  what the browser said; `web preview --detach` still serves the page for
  the owner to open.

## 9. Phases

Each phase is one issue, small enough for one implement run:

1. **Supervisor:** `bottown flow` for one issue (§3.1–§3.5): explicit
   actors, rounds, timeout, restart from forge state, conflicts with main,
   stop comments. Adds `bottown run stop`, and the per-actor run limit on
   the server.
2. **Run digests:** per-harness extraction at run end, stored by the
   server and kept by `gc`; `GET /v1/runs` filters and digests.
3. **The host UI and repository health:** `bottown web ui` as a local
   server (§8), with the upstream and health view (§4.3) and its pull and
   push actions.
4. **Inbox** (§4.1).
5. **Runs:** live with the tail, history and timelines (§4.2), and the
   actions that start, stop and clean up flows and runs (§5).
6. **`.bottown.toml`** with after-pull steps (§4.4).
7. **Audit:** `GET /v1/audit`, and policy denials shown in the run views.

Later, each with its own design: the run detail view with the full
transcript, stats, the queue and its labels (§3.6), milestones (§3.7),
and declared checks (§4.5).

## 10. Out of scope

- Merging by bottown, and the problem statement's `auto` mode.
- The supervisor driving interactive runs.
- A UI spanning several repositories, remote access, notifications.
- Anything Forgejo already does: the UI links to it.
- Synchronising issues with GitHub. Upstream is git only.
