# bottown

bottown is a multi-agent harness for local development. Read this page in
full, then only the document, or section, your work needs.

## Documents

- [Problem statement](problem-statement.md): read for why bottown is shaped
  as it is: constraints, what v1 settled, identity and enforcement on the
  local forge, open decisions.
- [Reference](reference/index.md): how bottown behaves today, one file per
  topic. Read its index, then the one topic you need.
- [Local web UI and supervisor](design/web-ui.md): read for `bottown flow`
  (implement and cross-family verify), `bottown web ui` (runs, the inbox,
  host state) and `.bottown.toml`. Its status line says what is built.
- [Milestones: plan, check, apply and run](design/milestones.md): read for
  plan runs, plan files and milestone epics: a reviewed plan on `main`
  applied as an epic of dependency-wired child issues (a DAG in the web
  UI), then run on a protected `milestone/<epic>` branch that auto-merges
  verified child PRs. Its status line says what is built.
- [The merge queue](design/merge-queue.md): read for how approved pull
  requests land: one landing step for `main` and `milestone/*` (a
  base-first landing commit, the checks at it, a fast-forward-only merge
  as `manager`), `bottown merge-all`, and why the flow no longer merges a
  base into a PR. Its status line says what is built.
- History, not kept current; read only for how something came to be:
  [Iteration 1](history/iteration-1.md) (the first issue through runs,
  actors as forge identities, permanent actor worktrees, in-harness runs),
  [Bootstrap](history/bootstrap.md) (how `bottown up` provisions Forgejo
  and the server; what Forgejo corrected in the problem statement),
  [v1 rewrite epic](history/v1-rewrite-epic.md) (superseded; evidence
  tables, red-team challenges).

## What it is

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
  possible, different model vendors: a model reviewing its own family's work
  is the weakest reviewer we can buy. The supervisor is code, not a model;
  the product owner is you.
- **Enforced, not advised.** Rules live in the forge's branch protection and
  in supervisor checks over forge data, never only in prompts. Approvals die
  on new commits unless the patch only moved onto a newer main, reviews are
  bound to a commit, only the manager account merges, and a verification
  check cannot be forged by the agent that wrote the code.
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
unattended. The [problem statement](problem-statement.md) says what carries
over and what was dropped.
