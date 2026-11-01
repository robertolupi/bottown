# Design: the local web UI and the supervisor

Status: **design**, written with the owner in issue #55. Built so far:
run digests (phase 2, #60), per-actor run limits and `run stop`
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

- `--rounds N` (default 3): the most implement runs one flow starts, the
  first included. Three rounds means up to two follow-ups after requested
  changes, which matches the problem statement's escalation after two
  rejections.
- `--repo R`: as for `run`; defaults to the checkout's repository.
- `--timeout D` (default 60m): per run (§3.5).
- It is an owner command, run from the `main` checkout like `run`. It
  prints one line per step and ends with the stop reason. Exit code 0 means
  "ready to merge"; any other stop is non-zero, so scripts can tell them
  apart.

### 3.2 The loop

```
          ┌───────────────────────────────────────────────┐
          ▼                                               │
  implement run (A) ──failed──▶ stop: run failed          │
          │                                               │
     PR at head H                                         │
          ▼                                               │
  verify run (B) at H ──failed / COMMENT──▶ stop          │
          │                                               │
          ├── APPROVED ──▶ stop: ready to merge           │
          └── REQUEST_CHANGES ── rounds left? ──yes───────┘
                                     └──no──▶ stop: out of rounds
```

A follow-up round is a new implement run on the same issue: it resumes
`issue/<n>` and the open PR, as today. Every new head gets a new verify
run.

If the PR's head moves during a verify run, bottown refuses the verdict, as
it does today. The supervisor then starts a new verify run at the new head
and does not count it as a round.

The PR's base can move too: the owner merges another PR into `main`, and
this one stops merging cleanly. An approval is bound to the head, so it
still stands, but the PR cannot be merged. The supervisor checks Forgejo's
mergeable flag at its stops. A conflicting PR stops as "conflicts with
main", and re-running the flow starts an implement run that brings `main`
into `issue/<n>` and resolves the conflict. That is a new head, so a new
verify run follows. This happens as soon as two flows run in parallel, and
always in a milestone.

Flows are implement → verify only. Discuss runs stay a separate owner
action (the UI offers it); a flow never starts one.

Every stop says why, on the issue as well as in the command's output, so
the owner sees it in Forgejo and in the UI:

| Stop | Meaning | The owner's next step |
|---|---|---|
| ready to merge | the verifier approved the PR's current head | review and merge |
| out of rounds | changes were still requested after N rounds | read the reviews; re-run with more rounds, steer it interactively, or take over |
| run failed | a run failed, crashed or timed out | read its handover; fix the issue text or the environment, then re-run the flow |
| verifier undecided | the verify run ended with `COMMENT` | read why; re-run, maybe with another verifier |
| conflicts with main | the PR no longer merges cleanly, because `main` moved | re-run the flow: its implement run brings `main` in and resolves the conflict |

### 3.3 State and restart

Following the problem statement (§4), the supervisor keeps no state of its
own: where an issue stands is read from the forge and the server's run
records each time. A killed flow is resumed by running the same command
again:

1. A run for this issue or its PR is still live on the server → refuse,
   naming the run (the supervisor never runs two steps of one issue at
   once).
2. No open PR from `issue/<n>` → implement.
3. An open PR at head H:
   - the PR conflicts with `main` → an implement run to resolve it, if
     rounds are left (a conflict fix counts as a round);
   - the verifier's official review at H is `APPROVED` → stop: ready to
     merge;
   - `REQUEST_CHANGES` at H → a follow-up implement run, if rounds are
     left;
   - no review at H → verify.
4. Rounds are counted from the PR's `REQUEST_CHANGES` reviews, so a resumed
   flow does not get fresh rounds.

### 3.4 Concurrency

Batch runs started from `main` use their own run worktrees, so nothing
stops one actor from working on two issues at once; the subscription's rate
window is the real limit. Each actor has a **limit on live batch runs**,
a per-actor `max_batch_runs` setting with a default of 1 (0 means no limit).

The server enforces it, atomically: `POST /v1/runs` refuses a batch run
for an actor already at its limit, with an error that says so. A flow that
is refused waits and tries again. Checking in each flow instead would race:
two flows started together (side by side in the UI, or by a milestone)
could both see the actor as free. Because the server counts, the limit
covers every batch run, whoever starts it. Interactive runs do not count.

Flows on different issues run in parallel, as separate processes; the
limit is the only coordination between them.

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
a flow under §3.4's per-actor limit, and removes the labels when the flow
stops. The labels make the queue visible and searchable in Forgejo, and
they are state the supervisor can rebuild from the forge.

The label lifecycle in detail (ordering, what a stopped flow leaves on
the issue) is designed with the queue.

### 3.7 Later: milestones

```
bottown milestone M --agents agy,claude,kimi
```

A milestone flow takes the open issues of a Forgejo milestone and runs a
flow for each, in parallel, with implementer and verifier pairs taken from
the pool: #1 implemented by agy and verified by claude, #2 implemented by
claude and verified by agy, and so on. Nothing above stands in its way:

- each issue's flow is the single-issue flow of §3.2, with its own worktrees;
- the per-actor limit on the server (§3.4) is what keeps the pool's actors
  from being over-committed, whatever the number of flows;
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
history), and upstream and repository health. A run detail view with the
transcript comes later.

### 4.1 Owner inbox

What is waiting on the owner in this repository. Items come only from
forge and run state, and an item goes away **only when the owner acts**: merges, comments,
closes, re-runs. There is no hidden "read" state, so the inbox can always
be recomputed.

| Item | Shown when | Goes away when |
|---|---|---|
| Ready to merge | an open PR's current head has an official `APPROVED` review from a family other than the author's, and it merges cleanly | merged or closed, the head moves, or it starts to conflict |
| Conflicts with main | an open PR no longer merges cleanly | the flow is re-run and resolves it, or the PR is closed |
| Stopped flow | a flow stopped as out of rounds, run failed or verifier undecided (§3.2) | the flow is re-run, or the issue is closed |
| Broken run | a run is dead (its harness exited without `run end`), its key expired while it was live, or a failed run left its worktree behind | the run is ended or cleaned up |
| Discuss answer | a discuss run answered on an issue, and the owner has not commented there since | the owner comments, or closes the issue |
| Actor-filed issue | an actor filed an issue the owner has not triaged: not queued, no milestone, not closed | any of those |

Each row links to the issue or PR in Forgejo (merging happens there) and
offers the matching bottown action (§5): re-run the flow, end or clean up
the run. Once repositories declare checks (§4.5), "ready to merge" also
requires them to pass.

### 4.2 Runs

**Live.** Active runs: target, kind, actor and family, batch or
interactive, and elapsed time against the timeout. Runs of one flow are
grouped under it ("#51 · round 2/3 · verifying with kimi"). A batch run
shows a **live tail of its log**, readable rather than raw JSON (assistant
text and tool calls, one line each), refreshed while it runs; the UI is
on the host, so it reads `.bottown/runs/<id>.log` directly.

**History.** A table of ended runs: when, target, kind, actor, harness,
mode, status, duration, usage where known, and the first line of the
handover. It can be filtered by actor, kind, status and date. An issue's **timeline** lists every run on it in order, with the
reviews and the merge between them.

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

# Later (§4.5): the checks bottown runs itself on a PR's head.
# checks = ["make check"]
```

The file names commands; bottown runs them only when the owner asks
(after-pull steps are shown, or run on the owner's click with the command
in full view).

bottown resolves `main`'s head commit and reads the file at that commit,
through the API proxy as the owner; the checkout's own copy is never read.
Unknown keys are an error, shown in the health checklist, so a misspelt
key is not silently ignored. After a Pull, the UI shows the steps in a
confirmation; on confirm it sends back the steps it showed, and the host
reads the file again and runs nothing unless they are still exactly
those. The steps run one at a time, with `sh -c`, in the main checkout,
and stop at the first that fails.

### 4.5 Later: declared checks

Today the verifier runs "the repository's checks" as the repository's
AGENTS.md (or README) names them, and the PR description says what the
implementer ran. Neither is checked by bottown. With `checks` in
`.bottown.toml`, bottown can run them itself:

- on the host, in a fresh worktree at the PR's head, after the implement
  run and before the verify run;
- with the result posted as a commit status on that head, shown in the
  verify prompt and in the inbox ("ready to merge" would then also need
  passing checks).

This needs its own design (isolation, time limits, what a failure does to
the flow) and gets its own issue.

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
| Queue or unqueue an issue (later, §3.6) | sets or clears the bottown-managed labels | no |

Starting a flow or a run picks actors from lists of the instance's actors.
The UI refuses a same-family verifier before anything starts, as the CLI
does.

Merging, editing issues and PRs, reviewing, and milestones are not
actions here: the UI links to Forgejo for them.

## 6. Data sources

| Data | Where | How the UI gets it |
|---|---|---|
| Runs: live and ended, with digests | server | `GET /v1/runs` (§7) |
| Policy decisions (allow/deny per forge write) | server | `GET /v1/audit` (new, §7) |
| Issues, PRs, reviews, comments, labels, milestones | Forgejo | the server's API proxy, as the owner |
| Actors and their families | server | `GET /v1/actors` |
| Run records and logs of this checkout's runs | host, `.bottown/runs/` | read directly |
| Actor worktrees | host | `git worktree list` in the checkout, and run records in `.bottown/runs` |
| Upstream | host, the checkout's `origin` | `git fetch origin` with the owner's credentials |
| Harness slash commands | host, each harness's config | compared with what `harness setup` would write |
| Repository settings | `.bottown.toml` on the forge's `main` | read through the API proxy |

**What logs yield** differs by harness, so each binding extracts what its
log has, at run end, into the digest:

| Harness | Final message | Usage |
|---|---|---|
| claude | the `result` event | tokens (summed over every model the run used), cost, turns and duration from the `result` event |
| agy | the `result` event | tokens (thinking counted as output), turns and duration from the `result` event; no cost |
| kimi | the last assistant message without tool calls | none known |

The live tail renders the same events, one line per assistant message or
tool call. Unknown usage stays unknown.

## 7. Server APIs

The UI uses the server's existing API where it can. What changes:

- **Run digests.** Ending a run (`DELETE /v1/runs/{id}`, from the launcher
  or from an agent's `run end`) can carry a digest: `summary` (cut to 4000
  bytes) and `usage`, whatever the host extracted, each field absent when
  unknown. The server stores it with the run, once: a second end changes
  nothing.
- **Purging keeps digests.** `POST /v1/runs/purge` (used by `run gc`)
  drops full run records, keys included, but keeps each run, with its
  digest, in a history, marked `purged`. `GET /v1/runs/{id}` finds it
  there too, and `bottown run show` prints it.
- **`GET /v1/runs`** (owner only) takes filters: `repo`, `state=live|ended`,
  `since` (RFC 3339, created at or after) and `history=1`, which adds
  purged runs, so history works after `gc`. Every run carries its digest.
- **`GET /v1/audit`** (owner only, new): the policy decisions the server
  makes, stored rather than only logged, filterable by repository, actor,
  run and decision. The server keeps them for a bounded time.
- **Per-actor run limits** (§3.4): `POST /v1/runs` refuses a batch run for
  an actor at its limit; the limit is part of the actor's configuration.
- **Later, for the queue:** provisioning creates `implement/<actor>` and
  `verify/<actor>` labels, and an owner-only endpoint sets or clears them.

Forge data (reviews at a head, who commented since, issues filed by
actors) needs no new endpoints: the UI reads it as the owner through the
API proxy.

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
  bodies, handovers, logs. The UI escapes all of it and sets a strict
  Content-Security-Policy with no inline scripts, so text an agent
  controls cannot run in the page that can start runs or push.
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
