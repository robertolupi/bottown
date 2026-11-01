# bottown

bottown is a multi-agent harness for local development.

## What it is going to be

bottown turns a developer's issue tracker into the control plane for coding
agents. You file issues; a supervisor on your machine reads the tracker,
starts one agent per piece of work in its own worktree, lets a second agent
from a different model family verify the result, and lands the pull request.
You review only what you asked to review.

It is built for one developer with several AI subscriptions on one computer,
not for a team or a cloud. That shapes everything:

- **Self-contained.** One command deploys the whole system: a private Forgejo
  forge in a container, the bot accounts, labels and branch protection, and
  the supervisor. Releases are pushed to GitHub as plain git; work is never
  tracked in two places.
- **Two agent roles, two identities.** An implementer turns an issue into a
  PR; a verifier reviews it at the exact commit, runs the checks, and
  approves or requests changes. They are different forge accounts and, where
  possible, different model vendors, because a model reviewing its own
  family's work is the weakest reviewer we can buy. The supervisor is code,
  not a model; the product owner is you.
- **Enforced, not advised.** Rules live in the forge's branch protection and
  in supervisor checks over forge data, never only in prompts. Approvals die
  on new commits, reviews are bound to a commit, only the manager account
  merges, and a verification check cannot be forged by the agent that wrote
  the code.
- **Two operating modes per issue.** `ask` (the default) stops before merge
  and on anything unusual and waits for you. `auto` merges once the verifier
  and the checks are green. Escalation from `auto` to `ask` is mechanical:
  a protected path, two rejections, scope growth, a blown budget, or an
  agent that says it is stuck.
- **Budget-aware.** Agents are paid for by subscriptions with rate windows.
  The supervisor schedules around an unavailable vendor instead of quietly
  running the work under another identity.
- **Nothing canonical outside the forge.** The supervisor keeps no state it
  cannot rebuild by re-reading the tracker. Restart means re-poll.

The design follows OpenAI's [Symphony](https://github.com/openai/symphony)
(tracker as control plane, scheduler plus runner, give agents goals rather
than state transitions) and the lessons of botfam v1, which spent fourteen
days building agent-to-agent coordination and never shipped a milestone
unattended. The problem statement below says what carries over and what was
dropped.

Forge reads through the CLI, MCP, and `/v1/forge` return complete issue and
pull-request lists, issue comments, and pull-request reviews. The server follows
Forgejo pages internally for issues, pull requests, and reviews; the issue
comments endpoint returns all matching comments in one request. Callers do not
supply page numbers. A failed later page fails the read rather than returning
a partial list. PR list review status is the shared review assessment's
decision (`approved`, `changes requested`, `commented`, `needs review`;
[web-ui.md §3.3](design/web-ui.md#33-state-and-restart)), and
is `unknown` when reviews cannot be read completely. The local UI also follows
pages for its inbox, run views, and issue timeline, retaining its existing
best-effort behavior when a read fails.

## Documents

- [Problem statement](problem-statement.md) — what bottown solves, constraints,
  what v1 settled, the shape of the solution, identity and enforcement on the
  local forge, open decisions.

### Designs

- [Local web UI and supervisor](design/web-ui.md) — `bottown flow` takes an
  issue through implement and cross-family verify; `bottown web ui` is a
  local view of runs, the inbox and the repository's host state (built:
  Phase 3, host UI and health view; Phase 4, inbox; Phase 5, runs, live and
  history, with flow and run actions; Phase 6, `.bottown.toml`
  with after-pull steps; Phase 7, the audit trail and run denials).
- [Milestones: plan, check, apply and run](design/milestones.md) — two-step
  milestone pipeline: Step 1 turns a kickoff/design issue into a reviewed plan
  file on `main`, applies it to create an implementation epic and dependency-wired
  child issues, and visualizes the DAG in `bottown web ui`; Step 2 runs an
  implementation epic on a protected `milestone/<epic>` branch with auto-merge
  of verified child PRs and a final pull request into `main`.

### History

Records of past work, not kept current:

- [Iteration 1](history/iteration-1.md): plan and build record for landing
  the first issue through bottown runs, with actors as forge identities and
  roles per run, then permanent actor worktrees and in-harness runs.
- [Bootstrap](history/bootstrap.md): how `bottown up` gets from nothing to a
  provisioned Forgejo plus bottown server, and what Forgejo corrected in the
  problem statement.
- [v1 rewrite epic](history/v1-rewrite-epic.md) — superseded; evidence
  tables and the red-team challenges.
