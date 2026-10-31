---
authors:
  - claude
kind: proposal
status: Superseded
superseded-by: docs/problem-statement.md
created: 2026-10-29
proposal-id: botfam-v2-rewrite-v1
executor: TBD
quorum: any
deadline: none
---

# Proposal: botfam v2 — a tracker-driven supervisor that ships one milestone unattended

> [!NOTE]
> **Status**: Draft (2026-10-29). Epic for a clean-room rewrite of botfam.
> Twelve children under `botfam-v2-children/`, each one PR, dependency ordered;
> SPEC.md is child #1. Not filed on any forge yet.

## Status

**Draft** (2026-10-29, by claude for rlupi). Written from the full git history
(896 commits, 2026-06-09 → 2026-06-22), the wiki's Lineage, Sessions and
Reviews indexes, all 39 retrospectives, and the process-risk glossary. The
`Challenges` section at the end is an unresolved red-team pass; the product
owner resolves it.

| Field       | Value                                           |
| ----------- | ----------------------------------------------- |
| Proposal id | botfam-v2-rewrite-v1                            |
| Executor    | TBD — see "Decisions already made"              |
| Quorum      | `any` — product owner decides, reviewers advise |
| Deadline    | none                                            |

**Decisions already made:** \[fill in: fresh repo vs `v2` branch; who writes
SPEC.md\].

## Problem — why now

botfam's whole history is 14 days, in which the coordination substrate changed
six times and the wake mechanism eight (`tail -f` → `watch.sh` → `irc-wait` →
`forge-wait` → spool `botfam wait` → MCP nudge → `wait N` → EventDeliveryV2),
the last retrospective is dated 2026-06-16, and nothing has moved since
2026-06-24, so the fourth substrate shipped (#484 M0c, #494 M1) and went
dormant without ever being reviewed. All 39 retrospectives have botfam's own
machinery as their goal: no downstream milestone was ever shipped unattended,
`botfam sprint` had not completed one real run when R1 was planned
([R1SprintAndCiSequencing](http://gitea:3000/botfam/botfam/wiki/R1SprintAndCiSequencing),
#538), and deep-cuts' last "botfam-shipped" PR (deep-cuts#19) carried three
human commits to one agent commit. The core has no forge seam — ten Gitea SDK
type aliases in `internal/forge/client.go:32-39` consumed by ten packages, and
53 in-process gitea-mcp tools in `internal/mcp/forge.go` — so moving the pilot
to GitHub, where deep-cuts actually lives, means rewriting that core either
way; the only question is whether 20k lines of retired substrates come along.

What the history corrected in the starting position:

- The order was not "spool → forge-first → EDv2". Forge-first landed 2026-06-13
  (#34 deleted ~10k lines of CCREP/UDS/scribe); the Maildir spool was layered
  *on top of it* as the wake path on 06-15/16 (#348–#359), after the same idea
  had been rejected on 06-11 as "relitigating the IRC pivot in reverse"
  (`review-2026-06-11-meta`). EventDeliveryV2 then replaced the spool on
  06-19/20. The oscillation is the finding, not any one substrate.
- The one enforcement that held was neither the binary nor the prose: it was
  Gitea branch protection configured by `tools/forge-gate.sh`
  (`review-2026-06-13-claude_1`: "when the platform already provides a
  primitive, configure it"). Prose rules failed "three times at the same
  boundary under time pressure" (`review-2026-06-10-claude_1`); the binary's
  trust machinery had quorum, self-approval and spoofing holes
  (`review-2026-06-11-claude_1`, `review-2026-06-12-claude_1`).
- `botfam sprint run` is more than a skeleton: it leases, pulls SQLite work
  items and spawns `botfam run` workers (`internal/cli/ops/sprint.go:362-650`).
  It does not read the forge, its worklist is frozen at `start`, and its
  default worker TTL is 30 s. The EventDeliveryV2 observer package has no
  production importer.
- No token or cost figure exists anywhere (`token_usage: UNAVAILABLE` in both
  06-16 retros). Any "budget exceeded" rule is unimplementable until runtime
  telemetry exists.
- `verify` is Go-only. deep-cuts is Rust plus Svelte, so v1's verifier could
  never have gated the pilot.
- "Paste, never retype, SHAs" survives only in `archived-bootstrap` (wiki),
  from the 06-10 fabricated-SHA incident; nothing current carries it.
- The retrospectives do argue for keeping two things on the drop list: the
  mailbox/spool ("the patterns done right",
  `review-2026-06-15-pattern-code-conformance`) and the in-process `forge_*`
  tools ("avoiding terminal prompts", `review-2026-06-14-agy_3`). Both are
  addressed in the dropped table and in Challenges.

## Goals

1. Ship one deep-cuts milestone on GitHub through v2 with two agent identities
   and the product owner acting only in `ask` reviews.
1. A spec (SPEC.md) from which the implementation can be regenerated; the Go
   code is one rendering of it.
1. Policy enforced at the forge or in supervisor code, never in prompts alone.
1. Every operational number read from the runtime or the forge, never from the
   agent.
1. Zero canonical state outside the forge; a supervisor restart from nothing
   reaches the same decisions.

## Non-goals

- A Gitea adapter at launch (the interface is reviewed against Gitea's API so
  it is not GitHub-shaped; the adapter itself is later).
- Multi-host, leases with fencing, a notification service, webhooks, a session
  store, a worker RPC channel.
- A planner agent, more than two LLM roles, more than one repo per sprint.
- OpenTelemetry export, dashboards, a simulator, a policy optimiser.
- Backward compatibility with v1 config, tokens, skills or wiki pages.
- Running v1 and v2 side by side on the same repo.

## What carries over

| Piece                                              | Current path                                              | Note                                                                                                                                      |
| -------------------------------------------------- | --------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| Worktree-basename identity, fail-loud config       | `internal/famconfig/resolve.go`, `config.go`              | Keep the prefix rules and the "no stanza → refuse" invariant; tokens become per **actor** (v1 drifted to per-harness, `famconfig.go:170`) |
| Per-worktree git identity                          | `internal/provision/worktree.go`                          | Keep; add a `doctor` check for author ≠ actor                                                                                             |
| Approvals die on new commits; review pinned to SHA | `doc/collab/PROTOCOL.md` §3, `tools/forge-gate.sh`        | Forge-native. Port to GitHub branch protection (dismiss stale reviews, required check)                                                    |
| Paste, never retype, SHAs                          | wiki `archived-bootstrap` line 266                        | Re-derive into the verifier skill from the 06-10 incident                                                                                 |
| `verify <sha>` worktree discipline                 | `internal/cli/review/verify.go`                           | Keep the detached-worktree-at-exact-SHA shape; make commands project-declared and publish as a check                                      |
| Harness launch and capture                         | `internal/cli/ops/run.go`                                 | Keep `claude -p` stream-json launch and capture dir; drop SQLite, Connect socket, fencing env                                             |
| Supervisor tick shape                              | `internal/cli/ops/sprint.go:362-650`                      | Keep "pull items, spawn run, record exit, restart"; replace the SQLite worklist with a forge query                                        |
| Handover snapshot protocol                         | `doc/collab/PROTOCOL.md` §4                               | Verbatim: goal, decisions, branch/PR, blocker, next step; posted by the runner on non-`done` exits                                        |
| Refuse-and-halt dispatch                           | wiki `CattleDispatchPolicy`                               | Supervisor invariant: never run a task under an identity other than the declared one                                                      |
| Process-risk glossary + meta-review                | wiki `proposal-process-risk-labels`, `skills/meta-review` | Move into the repo as `doc/process-risks.md` and a skill; wiki stops being storage                                                        |
| Retrospective skill                                | `skills/botfam-session-retrospective`                     | Retarget from interactive sessions to runs (ledger + capture + forge thread)                                                              |
| `red-team`, `submitting-a-pr` skills               | `skills/red-team`, `skills/submitting-a-pr`               | Unchanged                                                                                                                                 |
| Mangle exporter predicates                         | `internal/mangle/mangle.go` `Export()`                    | Become the forge-neutral fact schema, extended with labels, reviews, comments, checks; the linter itself is optional                      |
| Pattern/anti-pattern corpus                        | wiki `Architecture` MOC                                   | Read-only input to SPEC.md invariants and detections; not ported, not storage                                                             |

## What is dropped

| Item                                                              | Reason                                                                                                                                                                                                             |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| EventDeliveryV2 (`internal/eventdelivery/*`, ~2.7k LOC)           | Solves wake delivery for a topology v2 does not have (many long-lived consumers, one spool); `observe` has no production importer; never retrospected. Polling the tracker is sufficient at two actors on one host |
| Mailbox, ingest, `botfam wait`, `forge-wait`                      | Well built (single-writer `flock`), but they exist to wake long-lived pets. v2 has no pets: a run starts with a goal and exits                                                                                     |
| In-process gitea-mcp mount (`forge_*`, 53 tools)                  | Ties the core to Gitea's SDK and tool registry. Agents drive `gh` themselves (Symphony's model). Cost acknowledged: terminal permission prompts, handled by per-role allowlists (child 07)                         |
| `botfam serve` MCP server at launch                               | Its three jobs — docs corpus, forge tools, identity bootstrap — move to repo docs, the `gh` CLI and the runner's environment. Revisit only if a role needs a tool the CLI cannot give                              |
| IRC remnants                                                      | Removed in #520; strings linger in mailbox/ingest/server. The decision-on-ephemeral-channel scar (#141 duplicate work) stands                                                                                      |
| Wiki as storage                                                   | No PR gate; `antipattern-advisory-as-canonical`; slug bug #164. The wiki stays as history; nothing v2 runs on reads it                                                                                             |
| Session store, scope generations, leases with fencing, multi-host | Designed ahead of a single real run (`FalseStarts`, R1). Re-earn them against evidence from the pilot                                                                                                              |
| Memory, external-review, agent-docs commands                      | Not on the pilot's critical path; re-add from need, not from inventory                                                                                                                                             |

Everything not in the carry-over table is dropped by default.

## Roles at launch

- **Implementer** (LLM, own forge identity): issue → branch → PR → address
  comments. Never approves, never merges.
- **Verifier** (LLM, own forge identity, a different model family from the
  implementer where config allows — the one place the retrospectives show
  diversity paying): reviews at the actual head SHA, runs `verify`, approves or
  requests changes pinned to that SHA. Never merges.
- **Manager** = the supervisor loop, code not LLM: reads the forge, derives the
  worklist, dispatches runs, restarts crashes, escalates, merges in `auto`.
- **Product owner** = rlupi: defines milestones, answers `needs/human`, reviews
  `ask` items.
- **Planner**: deferred. v1's planning output was its least validated artifact
  (five overlapping simulator proposals in one day, `FalseStarts`).

## Operating modes

Per issue, as a label; default `ask` when absent.

- `mode/ask`: runs are dispatched; the supervisor stops before merge and on
  escalation, adds `needs/human`, posts one comment saying what it wants
  decided.
- `mode/auto`: verifier approval at head plus `botfam/verify` green at head →
  the supervisor merges.
- Mechanical escalation `auto → ask`, each detected from forge or runtime data:
  protected path touched; two `changes_requested` on one PR; scope growth
  (files outside declared paths, or new issues/PRs opened by the run); budget
  exceeded (runtime tokens or wall-clock); run exits `stuck` or crashes twice.
  Never flips back without a human removing `needs/human`.

## Architecture sketch

```
            policy ──► forge gate (branch protection, labels, CODEOWNERS)
               │
   config ──► supervisor ──poll──► forge adapter (Reader/Writer, gh) ──► GitHub
   (identity,     │  ▲
    stanzas)      │  └── exit reason / handover
                  ▼
            execution runner ──► harness CLI in actor worktree ──gh──► GitHub
                  │
                  ▼
            observability (run ledger, runtime-reported cost, status)
```

- **Policy**: SPEC.md, the two role skills, `doc/process-risks.md`, and the
  forge gate that makes branch protection and labels idempotent. A rule that
  cannot be enforced by the forge or the supervisor is documented as advisory,
  not pretended to be a rule.
- **Config**: `~/.botfam/config.toml` with `[actor.<name>]` and `[repo.<key>]`
  stanzas; identity from the worktree basename; no env overrides; no match →
  refuse.
- **Supervisor**: a loop. Poll → derive worklist by query (open issues in the
  milestone without an active run; PRs awaiting the verifier; PRs with changes
  requested) → claim (assignee + `run/active`) → dispatch → on exit: restart
  once with handover, or escalate. Bounded WIP per role. Local state is a
  deletable ledger.
- **Execution**: `run --role R --issue N`: one harness session, one worktree,
  goal + skill in the prompt, capture everything, exit taxonomy
  `done | stuck | budget | crashed | policy`, handover comment on non-`done`.
- **Forge adapter**: botfam's own
  `Issue/PullRequest/Review/Comment/Label/ Commit/Actor/CheckRun` and a fact
  schema; `Reader` and `Writer` interfaces; GitHub via `gh`; a build-time fence
  so no SDK type leaks past the adapter.
- **Observability**: JSONL run ledger, per-run capture, cost fields from the
  harness runtime or `null`, `status` command that also counts product-owner
  touches so the done-when is measurable.

## Children (dependency ordered, one PR each)

- [ ] 01 `ask` \[SPEC.md\](v1 worktree: doc/proposals/botfam-v2-children/01-spec.md) — blocks all
- [ ] 02 `auto`
  \[Repo bootstrap, config, identity\](v1 worktree: doc/proposals/botfam-v2-children/02-config-identity.md)
  — after 01
- [ ] 03 `ask`
  \[Domain model and GitHub adapter\](v1 worktree: doc/proposals/botfam-v2-children/03-domain-model-forge-adapter.md)
  — after 01, 02
- [ ] 04 `auto` \[Forge policy gate\](v1 worktree: doc/proposals/botfam-v2-children/04-forge-policy-gate.md)
  — after 03
- [ ] 05 `auto` \[Execution runner\](v1 worktree: doc/proposals/botfam-v2-children/05-execution-runner.md) —
  after 01, 02
- [ ] 06 `ask` \[Supervisor loop\](v1 worktree: doc/proposals/botfam-v2-children/06-supervisor-loop.md) —
  after 03, 04, 05
- [ ] 07 `ask`
  \[Roles: implementer and verifier\](v1 worktree: doc/proposals/botfam-v2-children/07-roles.md) — after 05,
  06
- [ ] 08 `ask`
  \[Modes and escalation\](v1 worktree: doc/proposals/botfam-v2-children/08-modes-and-escalation.md) — after
  04, 06 (budget trigger after 10)
- [ ] 09 `auto` \[verify v2\](v1 worktree: doc/proposals/botfam-v2-children/09-verify-v2.md) — after 02, 03
- [ ] 10 `auto` \[Observability\](v1 worktree: doc/proposals/botfam-v2-children/10-observability.md) — after
  03, 05
- [ ] 11 `auto`
  \[Process hygiene port\](v1 worktree: doc/proposals/botfam-v2-children/11-process-hygiene-port.md) — after
  03, 07, 10
- [ ] 12 `ask` \[deep-cuts pilot\](v1 worktree: doc/proposals/botfam-v2-children/12-deep-cuts-pilot.md) —
  after all

Children 04, 05 and 09, 10 are pairwise independent and may run in parallel
(different files, different owners). Nothing else may.

### Rollout

Phase 0 is child 01 alone: the spec, red-teamed, with no code. Phase 1 is
children 02–07: the shortest path to **one** deep-cuts issue going from open to
PR-approved unattended, in `ask` mode, before 08–11 exist — the thing v1 never
did. Phase 2 is 08–11. Phase 3 is the pilot.

## Costs and Risks

- **This epic is itself the pattern it warns about.** Twelve children filed
  before a single v2 run exists is `ungated-speculation`; R1's own rule was "a
  real run must exist before it is worth viewing or CI-guarding". Mitigation:
  Phase 1 is deliberately thin and ends in a real PR; children 08–11 are not
  started before it ships.
- **Third attempt at the same component.** Cattle Not Pets (2026-06-16) and
  EventDeliveryV2 M1 (2026-06-20) were both this supervisor. Each grew a store,
  a transport and a lease before a run. Child 06's non-goals list is the guard.
- **Two parallel systems.** The 06-11 meta review's shared principle was "don't
  run two coordination systems"; `#231` (resolver ×3) is what coexistence
  produces. v1 is frozen read-only the day child 02 merges.
- **`gh` in the harness means permission prompts.** v1's in-process tools
  avoided them. Allowlists per role are the answer; if they leak, unattended
  runs stall silently — the leading cause of v1 "quiet stalls".
- **Two GitHub identities cost real accounts or app installations**, and
  GitHub's rules on bot reviews approving PRs for branch protection must be
  checked before child 04 is built on them.
- **The product owner is the bottleneck in `ask`.** With everything defaulting
  to `ask`, throughput equals review latency. That is the intended first state;
  the pilot measures it.
- **Done-when is unmeasurable until child 10.** v1 had no cost data; without
  the ledger the pilot can only be judged by anecdote.
- **Rewrite tax.** `theory-cost-reliability-inversion`: restart is expensive
  and lossy. The carry-over table is the only hedge; everything else is
  re-derived, and some of it will be re-derived wrong.

## First Expected Payoff

The first deep-cuts issue that goes from `open` to a verifier-approved PR with
no human action, produced by children 02–07 in `ask` mode. That event, with its
run ledger entry, is the first evidence v1 never produced. If Phase 1 cannot
produce it within the agreed budget, the epic is re-planned, not extended.

## Done when

One deep-cuts milestone, defined on GitHub, closes through `bottown sprint run`
with two agent identities; every product-owner action on it is a review on a
`mode/ask` item (verified from the forge and the ledger, child 10); a
retrospective is written with the ported skill and records which Challenges
below materialised. Failure to ship also closes the epic, with the
retrospective naming the broken assumption.

## Challenges

> Red-team pass by claude (2026-10-29) using the `red-team` skill, run in a
> separate context that had not seen the draft's reasoning. None of these are
> resolved here; the product owner resolves them. **Obs** marks something
> checked on this host or in the repo. **Spec** marks speculation.

**Core claim, steelmanned:** v1 never shipped downstream work because it kept
re-solving wake delivery for long-lived agents. A small supervisor that reads
the tracker, runs short task-scoped agents with separate identities, and leaves
enforcement to branch protection would be the first botfam to ship a milestone
unattended, and GitHub is where the pilot would run.

### C1. The case for a rewrite rests on choosing GitHub, and that choice is optional

- **Mechanism.** The epic says moving to GitHub "means rewriting that core
  either way".

- **Obs:** `robertolupi/deep-cuts` on GitHub is a public mirror. It has one
  closed issue, zero PRs and no `.github/workflows`, and `github/main` is 10
  commits behind `origin/main` on Gitea. The pilot's seed issues (#10, #11,
  #12) live on Gitea. deep-cuts lives on Gitea, not on GitHub.

- **Obs:** R1 (ratified 2026-06-22) put "#534/#184 → #538, a real run first"
  ahead of everything else. #538 still has not happened. The epic puts a spec
  and a forge seam ahead of that run without saying it is reordering a ratified
  plan.

- **Steelman, then break.** The best version: the seam is needed eventually,
  and doing GitHub now avoids porting twice. The counter: the epic bundles
  three changes (first real run, GitHub, rewrite). If the pilot fails, the
  retrospective cannot tell which one broke it, so the done-when's "name the
  broken assumption" cannot be met.

- **Risk:** phase-inversion; partially superseded (R1 ordering).

- **Cheaper alternative:** subtract from v1 and get #538 on Gitea deep-cuts.
  That means:

  - fix the 30 s `--worker-ttl` default (`sprint.go:646`);
  - make `verify` read its commands from config;
  - replace the frozen SQLite worklist with a forge query;
  - delete eventdelivery/mailbox only after the run works.

  Migrate to GitHub as a separate, attributable step.

- **Cost of the alternative:** the Gitea SDK debt stays longer, and the forge
  seam gets designed later against one adapter.

### C2. The pilot does not test botfam's only value claim

- **Mechanism.** `theory-value-thesis` says parallelism and continuity do not
  count, because one agent with subagents already provides them. Only
  cross-model critique counts.
- The done-when measures continuity: a milestone closes unattended. A single
  Claude session can do that. So can a label-triggered hosted coding agent plus
  any second-vendor review bot plus branch protection (**Spec:** whether these
  run on the operator's subscriptions instead of API billing needs checking).
- Nothing records whether the cross-family verifier caught a defect that the
  implementer's own self-review missed. A green pilot would be read as
  validating botfam while proving only that a pipeline exists.
- **Risk:** hollow-validation.
- **Killer question:** what does v2 deliver that the cheapest hosted
  alternative does not?
- **Cheaper alternative:** add a paired measurement to child 12. Log every
  verifier `changes_requested`, with whether a same-model self-review was also
  run and what it found. Report them alongside merges.
- **Cost:** about one extra run per PR, and the cross-model value may come out
  near zero.

### C3. The done-when can be passed by the product owner doing the work

- **Mechanism (Obs):** deep-cuts has no milestone, and child 12 has the human
  choose "3–6 small, decoupled issues".
- Everything defaults to `ask`, so the owner reviews every PR. A
  `changes_requested` review with detailed instructions is still "a review on
  an `ask` item", so the owner can co-author the milestone through reviews and
  the done-when still passes.
- **Risk:** hollow-validation, from self-selected difficulty and an unweighted
  touch count.
- **Cheaper alternative:** pre-register the milestone before any v2 code
  exists. Include at least one non-trivial issue, for example #10, the
  destructive DELETE. Count owner reviews by outcome: approved on first look,
  or changes requested with how many lines of instruction.
- **Cost:** the pilot can now visibly fail.

### C4. Identity separation is by convention, not enforced

- **Obs:** `gh` on this host is logged in as `robertolupi` via the keyring,
  with `repo` and `workflow` scopes. When `GH_TOKEN` is unset, `gh` falls back
  to that login. So any runner environment bug makes an agent act as the
  product owner: it is counted as a human touch and may get admin bypass.
- **Obs:** both actor tokens would be files in `~/.botfam` under one Unix user.
  An implementer with Bash can read the verifier's token. Child 07's "verifier
  token cannot push" does nothing to stop the implementer approving as the
  verifier.
- **Obs:** child 02 says "no env-var override exists", yet `gh`'s identity is
  an environment variable.
- **Obs:** no child names the identity the supervisor merges with in `auto`. If
  it is the owner's, every auto-merge breaks the done-when. If it is the
  verifier's, the forge audit shows the verifier merging.
- **Obs:** child 05 says "capture everything" and has no secret-scrubbing
  requirement. v1's #539 design asserted that a seeded secret is scrubbed.
- **Spec:** GitHub's terms appear to allow one free machine account per person.
  If so, the likely setup is one machine user plus one GitHub App. App
  installation tokens expire, so the runner would have to mint them, and no
  child covers that.
- **What dropping MCP loses:** the token lived in the server process, not in
  the agent's environment.
- **Risk:** misattributed-work.
- **Cheaper alternative:** either a separate OS user or container per actor, or
  a thin token-holding forge proxy, a narrow descendant of `botfam serve`.
  Otherwise drop "enforced" from Goal 3 for identity.
- **Cost:** OS-level setup, or keeping a server v2 meant to drop.

### C5. The per-role `gh` allowlist collapses into `gh api`

- **Obs:** in `gh` 2.103.3, `gh pr review` has only `--approve`, `--comment`,
  `--request-changes` and `--body`/`--body-file`. There is no `--commit` flag,
  so child 07's `gh pr review --commit <sha>` does not exist.
- Pinning a review to a SHA needs
  `gh api repos/…/pulls/N/reviews -f commit_id=…`. Allowlisting `gh api` grants
  arbitrary REST and GraphQL with that token: removing labels, merging,
  deleting branches.
- **Obs:** a plain `--approve` attaches to whatever the head is when the review
  is submitted. A push between `verify` and the approval therefore produces an
  approval on an unverified SHA, which is the 06-10 incident class. Dismissing
  stale reviews only covers pushes made after the approval.
- **Obs:** in `claude -p`, tool calls that are not allowlisted are denied, not
  prompted (v1 `run.go:224`: "default (deny, conservative)"). The failure mode
  is not the epic's "silent stall". It is agents working around the denial (for
  example `curl` with the token in the environment) or exiting `done` with
  partial work.
- **Risk:** violates `pattern-enforce-at-the-harness`.
- **Cheaper alternative:** the runner, as code, performs the forge writes that
  carry policy: the review submission pinned to the verified SHA, and label
  changes. Agents emit a verdict file, and agent-side `gh` stays read-only.
- **Cost:** a small writer surface, which is what v1's `forge_*` tools were.

### C6. `auto`/`ask` and the merge gate become prose again

- **Mechanism.** GitHub has no permissions per label. Any token with triage or
  write access, including the implementer's, can remove `needs/human` or add
  `mode/auto`.
- Child 08 is only enforceable if the supervisor checks who performed each
  `labeled`/`unlabeled` event in the issue timeline. It does not require that.
- **Obs:** "declared paths" for the scope-growth trigger have no defined place,
  format or author. The budget trigger is blocked on child 10.
- **Spec, per GitHub docs, verify:** if `botfam/verify` is a commit status
  posted with a user token, any write-token holder can post `success` for that
  context. Pinning a required check to an "expected source" works only for
  checks from a GitHub App. The `auto` gate would then be an approval plus a
  check the implementer can forge.
- **Risk:** hollow-validation; advisory-as-canonical.
- **Cheaper alternative:** run `verify` as a GitHub Actions job (a self-hosted
  runner on this Mac is enough) so the required check has a fixed source. Make
  the supervisor reject label changes made by anyone other than the product
  owner.
- **Cost:** CI setup, which deep-cuts currently lacks.

### C7. Unnamed flaw: a public repo moves the trust boundary

- **Obs:** `robertolupi/deep-cuts` is public, and v1's Gitea instance was
  private. On GitHub, anyone can comment on a public PR and submit
  `REQUEST_CHANGES`.
- Child 06's worklist ("PRs with `changes_requested` at head → implementer")
  and child 08's "two `changes_requested` → escalate" do not filter by who
  reviewed.
- Child 05 builds the prompt from the issue body and comments, then hands it to
  an unattended agent holding a repo-write token and `gh`.
- So any stranger can inject a prompt into a credentialed agent, or force
  escalations at will. No wiki page covers this, because v1 never faced it.
- **Risk:** none named.
- **Cheaper alternative:** an actor allowlist inside the adapter's `Reader`
  (child 03), so forge content from non-actors is never shown to the supervisor
  or to prompts. Or run the pilot on a private repo.
- **Cost:** a few lines now; much more after something goes wrong.

### C8. Twelve children before a run is the elaboration the epic warns against

- **Mechanism.** The mitigation gates code. `ungated-speculation` is about
  elaboration, and about 9.7k words of epic plus children already exist with
  zero runs.
- **Obs:** Phase 1 runs strictly in series from 01 to 07. Child 01 caps SPEC.md
  at about 1500 lines and blocks everything else.
- **Obs:** Phase 1 ends at "PR-approved", but child 07's verifier may approve
  only after `bottown verify` passes, and verify is child 09 in Phase 2. That
  is a dependency contradiction.
- **Obs:** children are labelled with `auto`/`ask` modes that exist only once
  child 08 lands, and Executor and SPEC author are both TBD. The plan's own
  metadata assumes its unbuilt mechanism.
- Goal 2 ("regenerable from spec") is not tested anywhere.
- **Obs:** the repo has been dormant since 2026-06-24. **Spec:** the binding
  constraint may be the owner's attention, not the architecture. v2 asks for at
  least six `ask` reviews before the first run.
- **Risk:** ungated-speculation; phase-inversion; speculative (Goal 2). The
  epic itself calls this the third attempt at the same supervisor.
- **Cheaper alternative:** move child 07's "execute each skill by hand once" to
  step 0. Run one deep-cuts issue end to end with `claude -p`, two tokens and
  `gh`, in one sitting, before SPEC.md. Keep children 02–12 as an unfiled
  backlog note.
- **Cost:** the spec gets written after contact with reality, and some of it is
  thrown away.

### Residual risks and disconfirming evidence

- **Rewrite tax.** Even with the carry-over table, rules that only existed in
  practice will be re-derived wrong. Watch for v1 incident classes reappearing
  in pilot retrospectives: fabricated SHAs, misattribution, quiet stalls.
- **C1 is wrong if** a day spent on v1 against Gitea deep-cuts cannot produce
  #538. That would show subtraction costs more than the epic assumes.
- **C5 is wrong if** a hand dry-run on GitHub completes, with reviews pinned to
  SHAs, under an allowlist that excludes `gh api`.
- **C2 and C3 are discharged if** the cross-family verifier records defects the
  implementer's self-review missed, and at least half of owner reviews are
  approvals on first look.
- **C7 is moot if** the pilot runs on a private repo, but the flaw then returns
  the day the repo goes public.
- **Watch the clock.** If child 01 has not merged within the First Expected
  Payoff budget, the dormancy reading in C8 is confirmed.

### Verdict

**Rework.** The direction (task-scoped agents, the tracker as canonical state,
enforcement at the forge) is the right lesson from v1. But the epic bundles a
forge migration, a rewrite and the first-ever real run into one unattributable
bet, and its enforcement claims fail against the actual `gh` CLI, a shared Unix
user and a public repo. Do the step-0 manual run and #538 on v1 first. Then
decide the rewrite against that evidence, with C4–C7's enforcement gaps
specified before child 03.
