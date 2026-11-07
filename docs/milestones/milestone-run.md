+++
title = "Milestone run: children on milestone/<slug>, merged by manager"
kickoff = 316
design = "docs/design/milestones.md"
+++

Step 1 of the milestones design is built: a plan run writes a plan,
`bottown milestone apply` files an epic and its children with `blocked_by`
edges, and the web UI draws the graph. Running the children is still
manual. This epic builds Step 2 (design §3) in the reduced shape that
#316 agreed:

- Children of a plan `<slug>` open their PRs into a protected integration
  branch, `milestone/<slug>`, named after the plan slug in the child's
  trusted `bottown:milestone` marker.
- Only `manager` merges into `milestone/*`, and only at a head that
  `review.Assess` reports approved by a verifier of another family.
- `bottown milestone finish` merges `main` into the branch once, at the
  end, runs the e2e gate on the whole branch, and only then opens the final
  PR into `main` as `manager`. The owner merges that PR; bottown never
  merges into `main`.
- `bottown milestone run SLUG --agents SPEC --parallel N` schedules the
  ready children by spawning `bottown flow N --unattended` processes, as the
  web UI does, and reads the forge to decide what to do next.

Owner decisions from #316: the branch is `milestone/<slug>`; limits are
`--agents` (per-entry counts, `*` for no limit, optional model pins) and
`--parallel` (issues in flight); there is no token budget; an agent that
exits without a tool call is removed from the pool for the rest of the
milestone run; an actor may appear only once in `--agents`; `milestone run`
refuses to start when a family in the pool has no verifier of another
family; `main` merges into the milestone branch only in `finish`.

The issues are cut so that the first four can run in parallel: base
branches, the "agent did nothing" stop, the `--agents` pool, and push
confinement. The queue (`bottown queue`, web UI design §3.6) is not in this
milestone: it can reuse the scheduler later. Out of scope: merging into
`main` by bottown, creating or closing Forgejo milestones, cross-repository
milestones, predicting conflicts from file overlap, enforcing budgets.

## Milestone base branches: run.Base, milestone/* protection, conflicts with base

```issue
key = "milestone-base"
depends = []
size = "L"
```

Let a milestone child's runs and PR use `milestone/<slug>` as their base
instead of the default branch, so that after this issue the owner can
create the branch by hand and drive children with `bottown flow`.

### Base resolution

Add one server function that decides a run's base for an issue, in this
order:

1. the base of the issue's open PR (`issue/<n>`), if there is one;
2. else `milestone/<slug>`, when the issue body ends in a `bottown:milestone`
   child marker (`milestone.ParseMarker`, `internal/milestone/marker.go`;
   a child marker has a `key`, not `role="epic"`), the issue was filed by
   the owner (`milestone.Trusted`), and the branch `milestone/<slug>`
   exists on the forge;
3. else the repository's default branch.

`POST /v1/runs` stores the result on the run record (a new `Base` field on
the run, kept in the run record and returned by the run APIs). Today three
places assume the default branch; make them use the run's base:

- `renderPrompt` (`internal/cli/run.go`) passes the default branch to the
  prompt and to `Workspace.AddImplement(id, branch, base)`
  (`internal/runs/workspace.go`), which already takes a base;
- `opPRCreate` (`internal/server/forge.go`) always opens the PR against
  `repo.DefaultBranch`;
- the implement and verify prompts in `internal/runs/prompts` say "main"
  or the default branch where they mean the PR's base: name the run's base.

The rule applies to `bottown run start` and to `bottown flow` alike. An
untrusted marker (an actor-filed issue) never changes the base.

### Protection

Provisioning (`ensureProtection` in `internal/provision/reconcile.go`)
protects only the default branch. Add a protection rule for the pattern
`milestone/*` with only `manager` on the push and merge whitelists (the
owner is an admin and is not restricted). Actors cannot push to or merge
into `milestone/*`. Reconciling twice is a no-op.

### Stop reason

The flow (`internal/flow/flow.go`) already reads the PR's own base for its
trial merge and `MergeBase`; only the stop's name assumes `main`. Rename
`ConflictsWithMain` / `"conflicts with main"` to `ConflictsWithBase` /
`"conflicts with base"`, and say the base's name in the stop's detail.
Update every reader of the old string (inbox, web UI, docs).

### Web UI

`internal/webui/milestones.go` looks the group's base up as
`milestone/<epic number>`. Look it up by the plan slug from the epic's
trusted marker, `milestone/<slug>`, instead.

### Acceptance criteria

- Unit tests for the base rule: open PR wins; trusted child marker with an
  existing branch gives `milestone/<slug>`; a missing branch, an epic
  marker, an untrusted marker or no marker gives the default branch.
- A run on a milestone child gets a worktree at `milestone/<slug>`, a
  prompt naming it, and a PR into it.
- Provisioning creates the `milestone/*` rule with `manager` whitelisted; a
  test shows an actor's push to `milestone/x` is refused.
- `docs/reference/` describes the base rule, the protection and the
  renamed stop.

## Flow: a distinct stop when the agent did nothing

```issue
key = "agent-did-nothing"
depends = []
size = "M"
```

Give the flow a machine-readable stop for a run whose agent exited without
making a single tool call, the usual sign of an exhausted quota or a
lapsed sign-in, so that a scheduler running flows as separate processes
can tell it apart from other failures.

### Today

`runs.LastError` (`internal/runs/log.go`) reports whether a run's log has
any tool call. The launcher stores that in `FailedRunError.ToolCalls`
(`internal/cli/run.go`, around `failed.LastError, failed.ToolCalls =
runs.LastError(...)`), and the flow's `runFailedDetail`
(`internal/flow/flow.go`) adds "the run ended before the agent did
anything" to a `run failed` stop. Two gaps:

- it is computed only for runs that exited non-zero, not for a run that
  exited 0 having done nothing;
- it reaches other processes only as prose in the stop comment.

### Change

- Check for tool calls at the end of every batch run (implement, verify),
  whatever its exit status.
- Add a stop reason, `AgentDidNothing` (`"agent did nothing"`), used when a
  run ended without a tool call. Name the actor (and model, if pinned) in
  the detail.
- Make the stop machine-readable to another process: `bottown flow` gets a
  distinct exit code for it (document it next to the existing exit codes),
  and the stop comment carries the reason in a form a reader can parse
  without matching prose (for example, the stop reason in a marker line).
- Other `run failed` stops (a check failed, a crash after real work) are
  unchanged.

### Acceptance criteria

- Tests: a run that exits 0 with no tool calls stops the flow as
  `agent did nothing`; one that exits non-zero with no tool calls does the
  same; one with tool calls that fails stays `run failed`.
- `docs/reference/` lists the new stop and exit code.

## The --agents pool: spec parsing and cross-family pairing

```issue
key = "agents-pool"
depends = []
size = "M"
```

Add a small, pure package (no forge access) that the milestone scheduler
will use to describe its pool of agents and to pair implementers with
verifiers.

### The spec

`--agents "claude=*;agy:gemini-3.8-flash-medium=1;kimi=1;crush=*"`. Entries
are separated by `;`. Each entry is `actor[:model]=N`:

- `actor` is a provisioned actor name; `:model` optionally pins the harness
  model for that entry's runs (passed as the flow's `ACTOR[:SPEC]`, see
  `Options` in `internal/flow/flow.go`);
- `N` is a positive integer, the most live runs for that entry, or `*` for
  no per-entry limit.

Refuse, with a message naming the entry: an empty spec, a malformed entry,
`N` of 0 or not a number, an unknown actor, and an actor that appears more
than once (with or without different models).

### Pairing

- An actor's family is the one bottown already records for it (the same
  family the verifier cross-family check uses).
- Given the pool, report at construction time whether every family that
  has an implementer also has at least one verifier of another family. If
  not, the pool is refused: `milestone run` refuses to start rather than
  failing children later. Name the family that cannot be verified.
- Provide a deterministic round-robin that, given the entries in use (live
  run counts per entry) and entries removed from the pool, returns the next
  (implementer, verifier) pair with different families and spare capacity,
  or none.
- Allow removing an entry for the rest of an invocation (the scheduler does
  this after an `agent did nothing` stop); removal can make pairing
  impossible, which the round-robin reports as none.

### Acceptance criteria

- Table tests for parsing and every refusal.
- Tests for pairing: families respected, limits respected, `*` unlimited,
  round-robin order stable, removal honoured, the one-family pool refused.

## Push confinement to issue/<n> in the git proxy

```issue
key = "push-confinement"
depends = []
size = "M"
```

Confine an implement or plan run's pushes to its own branch, `issue/<n>`.
Today `RunScope.DecideGit` (`internal/policy/run.go`) allows an implement
run to push any ref Forgejo accepts; only branch protection keeps actors
off the default branch. With several siblings running side by side on a
milestone, one run must not be able to push another's branch.

### Change

- In the git proxy, for `git-receive-pack` requests from a run, read the
  ref-update commands at the start of the pack request (pkt-line
  `<old> <new> <ref>` lines, before the pack data) without buffering the
  pack itself.
- Allow the push only when every updated ref is `refs/heads/issue/<n>` for
  the run's issue. Refuse anything else (other branches, tags, deletes of
  other refs) with a clear message on the git client's side.
- The owner's key and non-run principals are unchanged.
- Keep the policy decision in `internal/policy`, with the proxy only
  extracting the refs.

### Acceptance criteria

- Unit tests for the pkt-line reader, including multiple refs, capabilities
  on the first line, and a truncated request.
- Policy tests: own branch allowed; another `issue/<m>`, `main`,
  `milestone/x` and a tag refused.
- An e2e test (under `-tags e2e`) that an implement run's push to another
  branch is refused and to its own branch succeeds.
- `docs/reference/` describes the confinement.

## Manager merge into milestone/*: POST /v1/forge/pr/merge and bottown milestone merge

```issue
key = "manager-merge"
depends = ["milestone-base"]
size = "M"
```

Add the one operation through which bottown merges a child PR: as the
`manager` account, only into a `milestone/*` branch, only at a head a
verifier of another family approved. The `manager` account and its token
(`write:repository`, `write:issue`) exist in the secret store, but the
server never uses them yet.

### The operation

`POST /v1/forge/pr/merge`, owner-only, with the PR number and the head SHA
the caller expects. Refuse unless:

- the PR's base matches `milestone/*`;
- the PR's head is exactly the given SHA;
- `review.Assess` (the function the flow and the inbox use) reports the PR
  approved at that head.

Then, as `manager`:

1. merge the PR (a merge commit);
2. close the child issue, because Forgejo applies `Closes #C` only on
   merges into the default branch;
3. comment on the child with what was merged (PR, head SHA, base).

Each refusal says which condition failed. Calling it again after a merge
reports the PR as already merged instead of failing.

### The command

`bottown milestone merge C [--sha SHA]` (owner-only) calls the operation
for issue `C`'s open PR, defaulting `--sha` to the PR's current head, so
the owner can use it by hand before the scheduler exists.

### Acceptance criteria

- Server tests for each refusal (base not `milestone/*`, SHA mismatch, not
  approved, same-family approval only) and for the merge, close and comment
  happening as `manager`.
- Actors (any run role) are refused.
- `docs/reference/` describes the endpoint and the command.

## Milestone branch and finish: bottown milestone branch and finish

```issue
key = "milestone-finish"
depends = ["milestone-base"]
size = "L"
```

Add the two owner-only commands around a milestone's integration branch:
creating it, and finishing it into a final PR into `main`. The owner runs
them by hand; the scheduler calls them later.

### bottown milestone branch SLUG

Create `milestone/<slug>` from the default branch's head, as `manager`, when
the plan `<slug>` has an epic filed by `apply` (a trusted epic marker for
that slug). If the branch exists, report it and succeed.

### bottown milestone finish SLUG

The epic's **active children** are the issues that carry a trusted child
marker for `<slug>` (owner-authored, `milestone.ParseMarker`,
`milestone.Trusted`) and are in the epic's current `blocked_by` set, open or
closed. A key removed from the plan is not active: `apply` leaves its issue
and marker in place and drops only its edge from the epic and its checklist
line (milestones design §2.6, `internal/milestone/apply.go`). Put this
definition in one function in `internal/milestone`, which the scheduler
reuses.

1. Refuse if any active child is still open, listing them (`--force` is not
   offered: the owner merges by hand if they want a partial branch). An
   open issue removed from the plan does not block finish.
2. Merge the default branch into `milestone/<slug>`, as `manager`. This is
   the only time `main` is merged into the milestone branch. On a conflict,
   stop and say so, leaving the branch unchanged; the owner resolves it.
3. Run the repository's e2e gate on the branch's new head in a clean
   worktree, with the same commands, worktree handling and timeout as the
   flow's gate (`flowChecker` in `internal/cli/flowcheck.go`). Reuse that
   code rather than copying it.
4. Only when the gate passes, open the final PR from `milestone/<slug>` into
   the default branch as `manager`, or update its description if it is
   already open. Its description lists the merged active children and their PRs,
   and closes the epic when the owner merges it.
5. When the gate fails, comment on the epic with the summary and the log's
   path, and exit non-zero; open no PR.

The server operations behind both commands are owner-only; actors are
refused.

### Acceptance criteria

- Tests for: branch created once and idempotent; finish refused with open
  active children; an open issue whose key was removed from the plan (apply,
  remove the key, apply again) does not refuse finish; a conflicting merge leaves the branch alone; a failed gate opens
  no PR and comments; a passed gate opens, and on a second call updates,
  one PR authored by `manager`.
- `docs/reference/` describes both commands.

## Flow: merge a clean milestone base without asking

```issue
key = "flow-base-merge"
depends = ["milestone-base"]
size = "M"
```

Let the scheduler's flows keep a child PR up to date with its
`milestone/<slug>` base as siblings land, without the owner's attention.

### Today

When a PR's base moved and the trial merge is clean, an attended
`bottown flow` asks the owner and merges the base into the head as the
owner; an unattended one (`--unattended`, as the web UI starts it in
`internal/webui/actions.go`) leaves it to the inbox. Approval carry-over
(`review.SamePatch`) keeps an approval across a clean base merge when the
patch is unchanged.

### Change

- Add a flow option and a `bottown flow` flag (for example
  `--merge-milestone-base`) that, when the PR's base matches `milestone/*`,
  merges a clean base into the head without asking, attended or not. The
  option does nothing for any other base: the default branch is still the
  owner's to merge.
- Carry-over then keeps the approval when the patch is unchanged, as today;
  when it does not, the flow verifies the new head as usual.
- A conflicting base is not merged: it costs one implement round against
  `--rounds`, as today, and when no rounds remain the flow stops as
  `conflicts with base`.

### Acceptance criteria

- Flow tests: with the option and a `milestone/*` base, a clean moved base
  is merged without a prompt and the approval carries over; with the
  default branch as base, the option changes nothing; a conflicting base
  starts an implement round.
- `docs/reference/` documents the flag.

## bottown milestone run: the scheduler

```issue
key = "milestone-run"
depends = ["agents-pool", "agent-did-nothing", "manager-merge", "milestone-finish", "flow-base-merge"]
size = "XL"
```

Add `bottown milestone run SLUG --agents SPEC --parallel N`, an owner-only
command that drives a milestone's children to completion on
`milestone/<slug>`.

### Start

- Refuse unless the plan `<slug>` has a trusted epic (the `bottown:milestone`
  marker `apply` writes).
- Parse `--agents` with the agents-pool package; it refuses duplicates, bad
  entries and a pool in which some family has no verifier of another
  family. `--parallel N` (required, `N ≥ 1`) caps the issues in flight, and
  so the e2e suites running at once. There are no hidden defaults and no
  token budget.
- Create `milestone/<slug>` if missing (as `bottown milestone branch` does).
- Provisioning creates the `implement/<actor>` and `verify/<actor>` labels
  for every actor (the web UI already reads them; nothing sets them yet).

### Loop

- **Ready children:** open active children of the epic whose `blocked_by`
  dependencies are all closed. Dispatch in ascending issue number; no
  file-overlap prediction.
- **Pairing:** for each child dispatched, take the next cross-family
  (implementer, verifier) pair from the pool and record it on the child as
  `implement/<actor>` and `verify/<actor>` labels, set with the owner's key
  through the proxy. On resume, a child that already has labels keeps its
  pair if both entries are still in the pool.
- **Flows:** start `bottown flow N --unattended` as a separate process per
  child, with the pair, the pinned model if any, and the milestone base
  merge option. Do not run several flow supervisors in one process: the
  flow CLI swaps the cobra command's context per run. Waiting for a busy
  actor is the flow's own retry; the scheduler adds no priority between
  implement and verify.
- **Results:** when a flow ends, read its stop.
  - `ready to merge`: merge with `POST /v1/forge/pr/merge` at the approved
    head; then the next ready children become dispatchable.
  - `agent did nothing`: remove that entry from the pool for the rest of
    this invocation, and name it in the summary. Do not substitute another
    actor for that child: it stays stopped.
  - any other stop: leave the child for the owner, as the flow left it.
- **Finish:** when every active child is closed, run `bottown milestone finish`.
  Otherwise, when nothing more can be dispatched, comment a summary on the
  epic: merged, stopped (with reasons), blocked, and removed pool entries.
- Exit codes follow `bottown flow`'s conventions: success only when the
  final PR is open.

### Running it again

The scheduler keeps no state of its own: every invocation rebuilds what to
do from the forge, so the owner can stop it, add children (edit the plan,
merge it, `bottown milestone apply` again) and run it again.

- **Children** are the epic's active children, as `milestone finish`
  defines them and with the same function: issues with a trusted child
  marker for `<slug>` that are in the epic's current `blocked_by` set, open
  or closed. An issue linked to the epic by hand, without such a marker, is
  not a child: the base rule would not put its PR on `milestone/<slug>`.
  List it in the summary as ignored, and do not dispatch it.
- **Removed keys:** an issue whose key the owner removed from the plan
  keeps its marker but is no longer in the epic's `blocked_by` set. It is
  not dispatched and not counted for finish; list it in the summary as
  flagged (removed from the plan), as `apply` does.
- **Closed children** are done and skipped. **Stopped children** (open, a
  flow stopped on them earlier) are dispatched again once ready; their flow
  resumes on the existing PR. A child with labels keeps its pair while both
  entries are in the pool.
- **Pool removals** last one invocation: an entry removed after
  `agent did nothing` is back in the pool on the next run.
- **An open final PR:** each merge into `milestone/<slug>` moves the final
  PR's head past the commit `finish` gated. After such a merge, comment on
  the final PR that its head has not been gated, so the owner does not
  merge it; when every active child is closed again, `finish` re-gates the head and
  updates the PR, and comments that the head is gated.
- **A merged final PR:** if the previous final PR was merged into the
  default branch, merge the default branch into `milestone/<slug>` (as
  `manager`) before dispatching, so the new round starts from it. On a
  conflict, stop and say so.

Tests: a rerun after `apply` added a child dispatches only the new and the
stopped children; an unmarked issue linked to the epic is ignored and
reported; apply a plan, remove an open child's key, apply again and rerun:
the removed issue is neither dispatched nor required for finish, and the
summary flags it; a removed entry is back on the next invocation; a merge with an
open final PR comments on it, and the next `finish` re-gates and updates
it; a rerun after the final PR merged starts from the default branch.
- If batch runs come to lease permanent actor worktrees (iteration 1, step
  7.5), treat a lease refusal like `actor busy`.

### Acceptance criteria

- Tests with a fake forge and a fake flow launcher: readiness from
  `blocked_by`; `--parallel` honoured; pairs cross-family and kept on
  resume; auto-merge on `ready to merge`; an `agent did nothing` stop
  removes the entry and other children continue; finish called when all
  children close; the summary comment otherwise.
- `docs/reference/` describes the command, its flags and exit codes.

## Web UI: start and stop a milestone run from the Milestones tab

```issue
key = "milestone-run-webui"
depends = ["milestone-run"]
size = "M"
```

Let the owner start and stop `bottown milestone run` for an epic from the
web UI's Milestones tab, as the UI already starts and stops flows
(`internal/webui/actions.go`).

### Change

- On an epic's group in the Milestones tab, a start action that asks for
  the `--agents` spec and `--parallel`, validates them with the same
  agents-pool parser (showing its message), and starts `bottown milestone
  run SLUG` as a background process the UI tracks, like its flows.
- While it runs, the group shows that it is running, its pool, and its
  child flows; a stop action ends the scheduler and its flows the same way
  the UI stops a flow.
- Owner only, like the other actions.
- Add the running state to `web/fixtures` and check it with
  `bottown web shot`.

### Acceptance criteria

- Server tests for the start and stop actions, including a refused spec.
- Frontend behavioural tests for the form and the running state.
- The fixture renders without sideways overflow at the narrow width.
