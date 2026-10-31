---
authors:
  - claude
  - rlupi
kind: proposal
status: Draft
created: 2026-10-29
proposal-id: botfam-v2-problem-statement-v1
executor: rlupi
quorum: any
deadline: none
---

# bottown — problem statement: what we are solving, and how

> [!IMPORTANT]
> **Provenance.** This was drafted inside the v1 repository on 2026-10-29. Bare
> file paths such as `internal/famconfig/resolve.go` refer to **botfam v1**
> (`~/src/fams/botfam-new`, forge `gitea:3000/botfam/botfam`, final commit
> `6e3b162` 2026-06-24). Page names such as `theory-value-thesis` and
> `review-2026-06-16-claude_1` refer to v1's wiki (`botfam.wiki.git`). Issue
> numbers `#NNN` are v1 forge issues. Nothing here is a path in this repository
> unless it starts with `docs/`.
>
> **Rename.** The project was called botfam2 when this was written and was renamed
> bottown on 2026-10-31; the text has been updated mechanically. botfam v1 is
> still called botfam.

> [!NOTE]
> **Status**: Draft (2026-10-29; forge decision ratified same day, §8.1).
> Founding document of this repository. It replaces the epic-and-twelve-children
> framing kept as [history/v1-rewrite-epic.md](history/v1-rewrite-epic.md) for its
> evidence tables and the red-team `Challenges`. This document
> states the problem, the constraints, what v1 settled, the shape of a
> solution, and the identity/enforcement model on the local forge that the
> red-team said was missing. It is written to be argued with, not executed.

## 1. The problem

A solo developer with several AI coding subscriptions (Claude Code, Codex,
Antigravity, plus local models) on one Mac wants backlog issues to become
landed pull requests **without supervising the agents**. Today every agent
session is interactive: the human opens it, steers it, reviews it, merges, and
repeats. OpenAI's Symphony post names the ceiling precisely: three to five
sessions before context switching dominates, and "the agents were fast, but we
had a system bottleneck: human attention."

botfam v1 tried to remove that bottleneck by making the agents **coordinate
with each other** (IRC, mailboxes, spools, event delivery) and never shipped a
downstream milestone unattended. Symphony removes it by making the **issue
tracker the control plane**: every open ticket gets an agent, the orchestrator
is a scheduler that reads the tracker and restarts crashed runs, and humans
"manage the work instead of supervising coding agents."

The problem v2 solves is therefore narrower than v1's and the same as
Symphony's, with three differences that matter:

1. **One human, several vendors, one machine.** Symphony assumes a team, a
   devbox fleet and one agent vendor. We have one person whose only irreducible
   reason to run more than one model is **cross-model critique** (wiki
   `theory-value-thesis`): the implementer and the reviewer should be different
   model families, because a model reviewing its own family's work is the
   weakest reviewer we can buy.
1. **Subscription budgets, not an API bill.** The agents are paid for by
   per-vendor subscriptions with rate windows. v1 stalled repeatedly when "all
   peers were out of tokens" (`review-2026-06-16-claude_1`), and the operator
   could not reliably run all harnesses at once. The scheduler must treat
   *actor availability* as a first-class, time-varying resource, not assume
   `max_concurrent_agents: 10`.
1. **Self-contained, private by default.** Symphony assumes a hosted tracker
   (Linear) and a trusted environment. bottown ships its **own forge**: a
   Forgejo instance in a container that one command deploys, with the bot
   accounts, labels and branch protection created by the same command. The
   control plane is local and private; GitHub is where releases are pushed as
   plain git, not where work is tracked. That keeps Symphony's trusted
   environment posture honest and makes identities free.

## 2. Constraints that shape the solution

- **Operator attention is the scarcest input.** botfam has been dormant since
  2026-06-24. Any design that needs the operator to review six design documents
  before the first run will not get its first run. The first deliverable must
  be an *unattended run*, not a spec.
- **Budgets are per vendor and per window.** The scheduler must survive "the
  implementer's harness is rate-limited until 14:00" without stalling the
  verifier, and without silently running the task under a different identity
  (`antipattern-misattributed-work`).
- **Enforcement must live in the forge or in supervisor code.** v1's one rule
  that held was Gitea branch protection; prose rules "failed three times at the
  same boundary under time pressure" (`review-2026-06-10-claude_1`). Anything
  the forge cannot enforce and the supervisor cannot check is *advisory* and is
  labelled as such.
- **Forge content is untrusted input.** Issue bodies, comments and reviews are
  prompt-injection vectors into an agent holding a write token. On the private
  local forge the authors are known, which is why Symphony's
  trusted-environment posture applies; the author filter on reads is still
  built, because it is the day-one requirement of any later public-forge
  adapter (Symphony §15.5 names the risk and leaves it to the implementation).
- **No canonical state outside the forge.** Symphony's orchestrator state is
  in-memory and restart means re-poll. v1 built two session stores before one
  run. Same rule here.
- **One host.** No leases with fencing, no RPC, no multi-host. Single process,
  `flock` if anything.
- **Self-contained and single-command.** `bottown up` must bring up the forge
  (Forgejo, SQLite backend, one volume), create the bot accounts and tokens
  through the admin API, apply labels and branch protection, and write the
  config, with no browser clicks. v1 borrowed a Gitea already running on a NAS;
  bottown owns its forge so that a fresh machine is one command from a working
  fam. Forgejo over Gitea for its open-source posture (community governed under
  Codeberg e.V., copyleft licence for new contributions); the API surface
  bottown needs is common to both.
- **One canonical plane per fact.** No issue synchronisation between the local
  forge and GitHub, epics-only or otherwise: a second copy of tracker state is
  the substrate churn v1 died of. Outside issues are triaged in by hand; the
  public roadmap, if any, is a file in the repo.

## 3. What v1 settled (keep) and what it disproved (drop)

Kept, with the v1 artifact that proves it worked:

| Lesson                                                                                        | Evidence                                                                                                                                                       |
| --------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Identity = worktree basename; per-worktree git identity; refuse when no config stanza matches | `internal/famconfig/resolve.go`, `internal/provision/worktree.go`, #338                                                                                        |
| Approvals die on new commits; reviews pinned to a SHA; never retype a SHA                     | `PROTOCOL.md` §3, `tools/forge-gate.sh`, the 06-10 fabricated-SHA incident                                                                                     |
| Verify at the exact SHA in a detached worktree before approving                               | `internal/cli/review/verify.go`, forge-autonomy skill §2                                                                                                       |
| Handover snapshot on exit; let it crash; supervise                                            | `PROTOCOL.md` §4, `pattern-handover-snapshot`                                                                                                                  |
| Refuse-and-halt rather than misattribute                                                      | wiki `CattleDispatchPolicy`                                                                                                                                    |
| Host-side credentialed forge writes, agent never holds the raw token                          | v1's in-process `forge_*` tools did exactly this; Symphony §11.5 makes it a design principle. The red-team (C4/C5) argues it back in after the epic dropped it |
| Process-risk vocabulary, meta-review in a separate context, blameless retrospective           | `proposal-process-risk-labels`, `skills/meta-review`, `skills/botfam-session-retrospective`                                                                    |

Disproved or never validated, and why:

| Item                                                                       | Why it goes                                                                                                                                              |
| -------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Agents coordinating with each other (IRC, mailbox, spool, EventDeliveryV2) | Six substrates in eight days, none retrospected after 06-16. Symphony's model has no agent-to-agent channel at all: the tracker is the only shared state |
| Long-lived agent sessions ("pets")                                         | Every wake/liveness incident in the retros is a pet problem. A run starts with a goal and exits                                                          |
| Gitea SDK types as the domain model                                        | No seam; bottown owns its domain types and treats Forgejo as one adapter                                                                                 |
| Wiki as runtime storage                                                    | No PR gate, `antipattern-advisory-as-canonical`                                                                                                          |
| Designing session stores, leases, transports before a run                  | `FalseStarts`, R1's "a real run must exist first"                                                                                                        |

## 4. The shape of the solution

Adopt Symphony's architecture and vocabulary where they fit, and say where they
do not.

**Adopted from Symphony (SPEC.md):**

- The six layers: policy (in-repo `WORKFLOW.md`-style file: prompt body plus
  typed front matter), configuration, coordination (poll → reconcile →
  dispatch), execution (per-issue workspace plus agent subprocess), integration
  (tracker adapter: a *read kernel* of two operations, `fetch_issues_by_states`
  and `fetch_issues_by_ids`, plus provider-native agent tools executed
  host-side), observability (structured logs, token accounting from the agent's
  own events).
- "Symphony is a scheduler/runner and tracker reader." Ticket writes are done
  by the agent through host-executed tools holding the credential. The
  orchestrator has no first-class tracker write API.
- Tracker states as the state machine; a successful run ends at a **handoff
  state** (their `Human Review`), not necessarily `Done`.
- "Give agents objectives instead of strict transitions." The prompt states the
  goal and the handoff rules; the agent decides how many PRs, whether to file
  follow-up issues, how to address review.
- Reconciliation: a run whose issue left an active state is stopped. Restart
  recovery is tracker-driven; no durable orchestrator database.
- Stall timeout, exponential backoff, bounded concurrency, continuation turns
  on the same thread while the issue stays active.
- Harness hardening is part of the core safety model, not an afterthought.

**Where we diverge, and why:**

- **Two roles with two identities, not one agent per ticket.** Symphony runs
  one agent per ticket and relies on CI plus human review. Our value claim is
  the cross-model verifier, so a PR is a second unit of work, dispatched to a
  different actor. The scheduler's candidate set is issues *and* PRs.
- **Actor availability is scheduled.** Concurrency limits are per actor and
  time-varying (rate-window aware, from the harness's own rate-limit events,
  which Symphony already tracks in `codex_rate_limits`). An unavailable actor
  parks its work; it never reassigns.
- **Reads are filtered by author.** The adapter's read kernel drops comments,
  reviews and label changes whose actor is not in the configured actor set (the
  owner plus the registered bots) before anything reaches the scheduler or a
  prompt. Symphony leaves this to the implementation.
- **Modes and escalation are explicit.** Symphony has one posture per
  deployment ("document your trust posture"). We need per-issue `auto`/`ask`
  because the operator wants to delegate routine work fully and keep judgement
  on the rest, in the same repo.
- **Multi-harness runner.** Symphony targets the Codex app-server protocol. Our
  execution layer launches whichever harness the actor is bound to (`claude -p`
  stream-json first; v1's `internal/cli/ops/run.go` already does Claude, Codex
  and Antigravity).

## 5. Identity and enforcement on the local forge

This is the section the red-team said was missing. With a forge bottown
administers, the answer is short: identities are ordinary accounts created by
the admin API, and the rules are Forgejo branch protection plus a few
supervisor checks over forge data.

### 5.1 Actors

| Actor         | Identity                 | Token scope                                     | Who holds the token                                                                                                            |
| ------------- | ------------------------ | ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Product owner | your Forgejo account     | admin                                           | the human                                                                                                                      |
| Implementer   | user `implementer` (bot) | repo write, issues write, PRs write             | the supervisor; a child run gets it in `GITEA_TOKEN`/`tea` login for the run only                                              |
| Verifier      | user `verifier` (bot)    | repo **read**, PRs write (review), status write | the supervisor; the child run gets a read-only token, the review and status are posted by the supervisor from the verdict file |
| Manager       | user `manager` (bot)     | repo write (merge), issues write, labels        | the supervisor process only; never a child                                                                                     |

Forgejo tokens are scoped per user with read/write per category
(`read:repository`, `write:issue`, …), so the implementer's token cannot post a
commit status and the verifier's token cannot push. `bottown up` creates all
four with `POST /admin/users` and `POST /users/{name}/tokens`; there is no
browser step and no terms-of-service limit on bot accounts.

### 5.2 What the forge enforces by itself

| Rule                           | Forgejo mechanism                                                                                                                                | Status                                                   |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------- |
| Author cannot approve own PR   | built in                                                                                                                                         | enforced                                                 |
| Approvals die on new commits   | branch protection: dismiss stale approvals                                                                                                       | enforced (v1 `tools/forge-gate.sh` already sets it)      |
| A rejected review blocks       | "block merge on rejected reviews"                                                                                                                | enforced                                                 |
| Review bound to a SHA          | reviews carry `commit_id`; the supervisor posts the verifier's verdict with the verified SHA                                                     | enforced if the supervisor posts it                      |
| Only the manager merges        | branch protection: whitelist merge users = `manager` (+ owner)                                                                                   | enforced                                                 |
| `verify` cannot be forged      | required status context `botfam/verify`; only `verifier` and `manager` tokens carry `write:repository` status rights, the implementer's does not | enforced by token scope                                  |
| Protected paths need the owner | branch protection "protected file patterns", or CODEOWNERS with owner review required                                                            | enforced; this *is* the `ask` escalation for those paths |

### 5.3 What the supervisor enforces (code, from forge data)

| Rule                                                 | How                                                                                                                  |
| ---------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `mode/auto` is honoured only if the **owner** set it | issue timeline: latest `label` event for `mode/*` must have the owner as actor, else treat as `ask`                  |
| `needs/human` cleared only by the owner              | same check; a bot clearing it halts the sprint                                                                       |
| Scope growth                                         | PR diff against the issue's declared paths; new issues filed by a run are allowed but are `ask` by construction      |
| Budget                                               | from the harness's own usage events; `null` when unknown, never estimated                                            |
| Misattribution                                       | commit author on the PR branch equals `implementer`; any review by `implementer`, or any commit by `verifier`, halts |
| Unavailable actor                                    | park the item; never dispatch to another actor; report in `needs/human` after a configured window                    |

### 5.4 Residual risks

- All tokens live under one Unix user on the host. An implementer run with Bash
  could read the verifier's token from disk. Cheapest mitigation: the runner
  injects the run's own token into the child environment and the harness
  sandbox profile denies `~/.botfam`; detection is the misattribution rule
  above. Separate macOS users or containers remain available if that proves
  insufficient.
- The forge is private, so forge content is trusted input from known actors.
  The author filter on reads is still cheap and still applied, because it is
  the day-one requirement for any later public-forge adapter.
- The forge container is infrastructure you keep alive: one volume, one backup
  rule. v1's compose stacks had incidents (leaked processes, "reproducible"
  claims that regressed). Keep it to Forgejo plus, if `verify` is to be a
  required status, one Forgejo Actions runner. Agents and the supervisor stay
  on the host; they need the subscription CLIs and keychains.
- Known Gitea-family gotchas from v1 that must be re-checked on Forgejo: reused
  notification ids (#352), the wiki slug bug (#164), `Closes #N` not
  auto-closing on API merge, the transient 405 during mergeability recompute.

### 5.5 Permission prompts

In `claude -p`, a tool call outside the allowlist is **denied**, not prompted,
so the failure mode is an agent working around a denial rather than a stall.
Design response: the agent's forge access is read-mostly by token scope, so the
allowlist can be generous on reads; every policy-carrying write (review,
status, label, merge) is done by the supervisor from a verdict file. The
implementer's writes are `git push` to its own branch and opening the PR, both
covered by its token scope and by branch protection.

### 5.6 GitHub, later

A GitHub adapter is a second integration behind Symphony's thin boundary (two
read operations, host-side writes). Its identity model is one GitHub App
registered through the manifest flow (two browser clicks, no key copying), with
`verify` as an App-posted check run and the owner approving as a normal
reviewer. Nothing in §5.1–§5.5 depends on GitHub, and nothing in the GitHub
design is needed for the pilot.

## 6. Non-goals

- Multi-host, leases, RPC, notification service, webhooks.
- A GitHub adapter at launch (§5.6 sketches it for later).
- Any synchronisation of issues, epics or labels between the local forge and
  GitHub. Releases reach GitHub as git pushes only.
- Supporting a forge bottown did not deploy (the existing NAS Gitea is a
  migration source, not a target).
- A planner role, more than two LLM roles, more than one repo per sprint.
- Dashboards, OpenTelemetry, simulation.
- Backward compatibility with v1.

## 7. The smallest thing that tests the problem

Before any supervisor code, in one sitting, by hand:

1. `docker compose up` a Forgejo with SQLite; create the owner, `implementer`,
   `verifier` and `manager` users and their scoped tokens through the admin API
   (a shell script is fine; this becomes `bottown up`). Push deep-cuts into it.
   Apply the branch protection in §5.2 with a port of `tools/forge-gate.sh`.
1. Run one issue end to end: `claude -p` as the implementer with its token in
   the environment, a second harness as the verifier with the read-only token,
   the review posted by hand with `tea` or `curl` at the verified SHA with the
   verifier token, the merge done with the manager token.
1. Record: which steps needed the owner, which forge calls the agents needed,
   which of §5.2's rules actually blocked something, and whether any v1 gotcha
   in §5.4 reappeared on Forgejo.

If step 2 cannot be completed, the problem statement is wrong somewhere above
and that is the finding. If it can, the supervisor is "whatever automates step
2", `bottown up` is "whatever automates step 1", and the policy file is
"whatever we typed into the prompts".

## 8. Open questions for the product owner

1. ~~**Forge for the pilot**~~ — **decided 2026-10-29 (rlupi): bottown ships
   its own Forgejo in a container; the control plane is local and private;
   GitHub receives releases by git push; no issue sync.** An earlier same-day
   decision for GitHub with GitHub Apps was reversed on the grounds of
   self-containment (one command, no browser steps, free identities).
1. **Where the verifier runs:** which vendor, and is a local model acceptable
   for the first verifier (zero marginal cost, weaker critique)?
1. **Sandbox vs separate macOS users** for the token-isolation residual in
   §5.4.
1. **Scope of `ask` by default:** every issue, or only issues touching
   CODEOWNERS paths, with everything else `auto` once the pilot has one green
   week?
1. **What counts as "shipped":** PRs merged, or issues closed by the owner
   after use? (The red-team's C3 says a trivial milestone passes for the wrong
   reason.)
