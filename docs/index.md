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
  on new commits unless the patch only moved onto a newer main, reviews are
  bound to a commit, only the manager account
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

Issue attachments reach harnesses as local files. An issue read lists the
files on the description and on each comment (`attachments`: id, name,
size, comment id), and `GET /v1/forge/issue/attachment?number=N&id=ID`
streams one file's bytes. The server fetches each file from Forgejo with the
actor's token, and only when the file is on an issue the key can read, so a
run stays within its repository and the agent needs no Forgejo login.
External links are not listed. The files belong to the run:

- **Where.** A launched run keeps them in
  `.bottown/runs/<id>.attachments/`. An actor worktree's run keeps them in
  `<gitdir>/bottown/attachments/`. Neither is in the checkout. Each file is
  `<id>-<name>`, with the name reduced to `[A-Za-z0-9._-]`, so two files
  with the same name get different paths. The directory is 0700, and files
  over 100 MiB are skipped.
- **When.** Starting a run downloads the files before the prompt is
  rendered. Each `issue show` in the run fetches files added since. A launched
  run's directory is removed when its launcher returns, however the run
  ended, and by `run gc` if the launcher died. The launcher saves the run's
  record, as preparing, before it downloads anything, so `run gc` finds
  the run, as dead, even when the launcher died during startup. An actor
  worktree's directory
  is removed when `run end` ends the run, when `run start` or `run end`
  recover an interrupted run (before the record is finalized, so a failed
  removal is retried), and before the next run downloads.
- **What the harness sees.** If there are attachments, the goal prompt has
  an `## Attachments` section with one line per file: its local path,
  original name, content type, size, and whether it is on the description
  or a comment. A file that could not be downloaded gets a line saying why,
  and a failed listing gets a line too. Neither stops the run or hides the
  issue text. `issue show` prints the same lines. `issue show --json` puts
  `path`, `content_type` and `error` on each attachment. Outside a run,
  `issue show` lists attachments without downloading them, and `--download
  DIR` saves them to DIR, which the caller then owns.
- **What stays out.** Prompts, `issue show`, comments and digests carry
  paths and metadata, never file contents. A harness that opens an image
  logs it base64-encoded, so a batch run's log is written with data URLs and
  base64 runs of 256 or more characters replaced by `[N encoded bytes elided
  by bottown]`. The Live Runs tail applies the same rule when it renders a
  log, including logs written before this change.

Launched runs get a private scratch directory under the launcher's temporary
directory, named `bottown-<workspace hash>-<run id>`. The harness receives it
as `TMPDIR` (and Claude receives it through `--add-dir`), so tests that need
temporary directories outside a git checkout can run normally. The launcher
removes it when the run returns; `run gc` uses the path saved in the local
record if the launcher died. Older runs' scratch directories beside their
worktrees are also removed by gc.

Runs share the host with each other and with the owner. The implement and
verify prompts say so: a run must not stop, kill or remove processes,
containers or worktrees it did not start, nor run `bottown up` or `down`;
when shared state blocks it, it waits or comments and stops. `bottown up`
and `down` also refuse inside a run (with `$BOTTOWN_RUN_FILE` set, in a run
worktree, or in an actor worktree whose run is live), except on the
throwaway `e2e-*` instances that `make e2e` creates and purges.

A launched run can choose its harness's model: wherever a command takes an
actor (`run implement|verify|discuss|plan -a`, `flow --implement` and
`--verify`), it also takes `ACTOR:SPEC`, split at the first colon. The actor
is the forge identity, with its family, key and worktree; the spec is opaque
to bottown and only the actor's harness binding reads it:

| Harness | Example | Passed as |
|---|---|---|
| claude | `claude:opus` | `--model opus` |
| antigravity | `agy:gemini-3.8-flash` | `--model gemini-3.8-flash` |
| kimi | `kimi:MODEL` | `-m MODEL` |
| codex | `codex:MODEL` | `-m MODEL` |
| crush | `crush:openrouter:~deepseek/latest`, `crush:lmstudio:qwen/qwen3.8-27b` | `-m PROVIDER/MODEL`: the spec's first colon separates the provider; a spec without one is a bare model |

- **Defaults.** `~/.bottown/<instance>/config.toml` may name a host's
  default spec per actor, used when the command gives none (the web UI's
  run and flow forms show it in their model fields):

  ```toml
  [actor_models]
  agy = "gemini-3.8-flash"
  ```

- **Checked by the harness.** bottown rejects only an empty spec or one
  with spaces or control characters. It keeps no list of models: an
  unknown one fails the run when the harness rejects it.
- **Listed by the harness.** `bottown models [ACTOR [ARGS...]]` runs the
  actor's harness's own listing, `agy models` or `crush models`, passing
  ARGS on (`bottown models crush openrouter`); without ACTOR it lists every
  actor's in turn. claude, kimi and codex have no listing command, and it
  says so: give them a name their model flag takes.
- **Recorded.** The server's run record and the local one keep the spec
  (`spec`), and `run ls`, `run show`, the handover comment, the web UI and
  the launcher's start line label it "model". A scripted harness
  (`BOTTOWN_HARNESS_CMD`) sees it as `$BOTTOWN_RUN_SPEC`.
- **Not in actor worktrees.** `run start` (and the slash commands) take no
  spec, and the server refuses one from an actor's key: the harness running
  there has already chosen its model.
- **The family stays the actor's.** The cross-family rule compares actors'
  declared families, never models. A harness that offers other vendors'
  models (agy's Claude models, crush through OpenRouter) can therefore put
  one family's model behind another family's actor, for instance
  `--verify agy:claude-…` on a Claude-authored PR. bottown does not detect
  this: choosing a model of the actor's own vendor is the owner's
  responsibility.

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
  of verified child PRs and a final pull request into `main`. (Built:
  Phase 1, the plan format, `milestones_dir` and `bottown milestone check`;
  Phase 2, the `plan` run kind and owner-only issue edits; Phase 3,
  `bottown milestone apply`.)

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
