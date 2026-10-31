# Iteration 1: one issue landed through bottown runs

> [!NOTE]
> **Status**: Plan (2026-10-31). Decisions below were agreed with rlupi in
> the design discussion of the same day. It builds on
> [bootstrap.md](bootstrap.md).

## Goal

One real, pre-registered issue on prod's `bottown` repo goes from issue to a
merged PR:

1. You file the issue (`bottown web forgejo`).
2. An implementer run opens a PR: `bottown run implement -a claude 12`.
3. A verifier run from another model family reviews it, pinned to the PR's
   head SHA: `bottown run verify -a codex 13`.
4. You merge (`ask` mode).

Every step is started by hand. The supervisor loop, `auto` mode and the
manager merging are iteration 2.

## Decisions

- **Actors are the forge identity; the role is per run.** Forge accounts are
  actors (`claude`, `codex`, `agy`, `kimi`), each with a model `family`.
  Implementer and verifier are *roles* that a run grants. Any actor can take
  either role on any issue. Commits, PRs and reviews always show the true
  actor, so switching to another subscription keeps attribution honest.
- **The actor is always named** (`-a <actor>`). bottown does not choose
  actors and does not track blocked subscriptions in this iteration.
- **The family rule is on by default, with an override.** A verify run whose
  actor has the same family as the PR author is refused, unless you pass
  `--allow-same-family`. The override is recorded on the run, in the audit
  log and in the review body.
- **MCP is HTTP only.** The run key in `Authorization` resolves on the server
  to `(actor, role, repo, issue/PR)`. Tools default to that context. For
  harnesses that cannot send headers, the fallback is
  `/mcp/k/<run-key>`; that route's path is never logged. A stdio shim gets
  built only if some harness needs one.
- **Runs live under the repo, isolated from your checkout.** Each run works
  from a bottown-owned bare mirror. The run never sees your checkout's
  config, `origin` or branches.
- **bottown works out its context from the checkout.** Inside a hosted
  checkout, `run` commands get the instance from the existing resolution
  order and the repo from the bottown remote. Outside one they need
  `--repo`.

## Design

### Actors (desired state)

These replace `[[bots]]`. `manager` stays as an internal, keyless account,
and the owner is unchanged.

```toml
[[actors]]
name = "claude"
family = "anthropic"
harness = "claude"

[[actors]]
name = "codex"
family = "openai"
harness = "codex"

[[actors]]
name = "agy"
family = "google"
harness = "antigravity"

[[actors]]
name = "kimi"
family = "moonshot"
harness = "kimi"
```

- **Forge permissions:** every actor joins one `actors` team, with code
  write so it can push branches. Protection keeps everyone off `main`. The
  approvals allow-list is all actors plus the owner.
- **Token scopes:** every actor gets `write:repository` and `write:issue`.
  The role restrictions are enforced by the proxy, below, not by the forge.
- **Migration:** the reconciler creates the actor accounts. Accounts listed
  in `retired` (`implementer`, `verifier`) get `prohibit_login`, which also
  makes Forgejo reject their tokens. bottown drops their tokens and API keys,
  but keeps the accounts so their history stays attributed. Their old teams
  (`implementers`, `verifiers`) are no longer managed; delete them by hand if
  you want.

### Run keys (server)

- **Minting:** `POST /v1/runs` (owner only) takes `{kind, actor, repo,
  issue|pr, allow_same_family}`.
  1. For verify runs, it reads the PR to get its head SHA and author, then
     checks the family rule.
  2. It mints a run key bound to all of that, valid for 12 h by default.
  3. It returns `{run_id, key, sha, branch}`.
- **Revoking:** `DELETE /v1/runs/{id}` revokes the key. `GET /v1/runs`
  lists the server's run records (for `run ls --all`).
- **Proxy role policy**, applied on top of the existing deny-list:
  - **verify runs:** no `git-receive-pack`, no PR or issue creation, no
    edits. They may read, comment, and submit a review. Filing a follow-up
    issue goes through `issue_create`, not the proxy.
  - **implement runs:** no review submission. They may push branches other
    than `main` (protection blocks `main` anyway), open and update PRs, and
    comment.
  - **all runs:** API and git calls are limited to the run's repo; anything
    else is denied.
- **Record:** the server keeps run records (key hash, binding, created,
  expires, revoked, last seen) in the secrets store. They are operational
  state that could be rebuilt, not part of the tracker.

### MCP tools

| Tool | Behaviour |
|---|---|
| `whoami` | Returns actor, family, role, repo, issue/PR and SHA for the run |
| `issue_get` | The run's issue, or a given number in the run's repo: body, comments, labels |
| `issue_create` | Files an issue as the actor. Allowed outside a run with a read-only key (repo required) and in any run (the run's repo only); `disable_actor_issue_create` turns it off |
| `pr_create` | Implement runs only; opens a PR from the run branch, closing the issue |
| `pr_get` | Head SHA, base, author, diff and reviews |
| `comment` | Comments on the run's issue or PR |
| `review_submit` | Verify runs only. Requires a `commit_id` equal to the PR's current head, rechecked on the server. Applies the family rule, adding a note when the run has the same-family override. |
| `forge_api` | Passthrough `(method, path, body)` through the same proxy policy |

### Run directories

```
<repo>/.bottown/
  mirror.git/                        bare mirror of the bottown remote
  runs/<run-id>/                     git worktree of mirror.git
  runs/<run-id>/.bottown-run.json    kind, target, actor, role, sha, status, exit code
```

- `.bottown/` is added to `.git/info/exclude`, so no `.gitignore` change is
  needed. `du -sh .bottown` shows the disk cost.
- **Worktree config** (`extensions.worktreeConfig`): `user.name` and
  `user.email` are the actor's. The only remote is the bottown one. The
  credential helper is reset, then set to `bottown credential --run <id>`.
- **Implement runs** use branch `issue/<n>`, created or resumed. A later run,
  possibly by another actor, continues the same branch to address review.
- **Verify runs** are detached at the PR's head SHA.
- **Not isolated:** this keeps runs away from your checkout's *git config*,
  not from the OS. Runs execute as your Unix user (problem statement §5.4).
  Editor indexers and file watchers may need `.bottown/` excluded.

### Commands

```bash
bottown run implement -a <actor> [-I] <issue>
bottown run verify -a <actor> [-I] [--allow-same-family] <pr>
bottown run ls [--all]        # local run dirs merged with server records; flags orphans
bottown run show <run-id>
bottown run gc [--failed]     # remove finished runs (failed ones only with --failed)
```

`bottown run implement` and `run verify` follow this sequence:

1. Fetch the mirror.
2. Create the run on the server.
3. Add the worktree and write the per-run git config and the harness's MCP
   config (`.mcp.json` for claude, or the equivalent for each harness).
4. Render the goal prompt from the issue or PR and the role's policy text.
5. Launch the harness with the worktree as its working directory, streaming
   its log to the run directory.
6. On exit, post a handover comment and revoke the key. Remove the worktree
   on success; keep it on failure.

`run ls` columns are run, kind, target, actor, family, mode, status, branch
or SHA, age and size.

**Batch or interactive.** Both modes use the same worktree, run key, MCP
config and actor identity, so roles are enforced the same way. Only the
launch differs:

- **Batch (default):** the harness runs headless (`claude -p`, `codex exec`),
  its output goes to the run log, and the run ends when the agent exits.
- **Interactive (`-I`):** the harness's own TUI runs in your terminal, seeded
  with the goal prompt as its first message, so you can discuss and steer
  while it works. The run ends when you quit, and bottown asks whether it
  succeeded.

Interactive runs are recorded as such, in the run record, `run ls` and the
handover comment. A result you steered is partly your work, and that should
stay measurable (red-team C3).

Later, both modes could run inside tmux, so a batch run could be joined with
`bottown run attach <id>`. That requires the interactive launch under the
hood, since a headless stream cannot be steered.

### Harness bindings (iteration 1)

- **claude:** `claude -p` with stream-json output, `--mcp-config` pointing at
  the run's file, and an allowlist of `mcp__bottown__*` plus the repo's build
  and test commands.
- **agy (Antigravity):** the second vendor for verification; see build step 5.
- **codex and kimi:** their actor accounts exist, but there are no bindings
  yet. kimi may come through crush and OpenRouter.

## Build order

1. ~~Actors in desired state, plus migration, the `actors` team and
   protection.~~ Done (2026-10-31).
2. ~~Run keys: the server endpoints, run records and proxy role and repo
   scoping. Unit tests for the policy.~~ Done (2026-10-31). Also added:
   - `POST /v1/reconcile`, which `up` calls when the container is already
     running, so it reports what *this* run changed;
   - the desired-state file is re-read on every reconcile, so edits apply
     without a restart;
   - images are built with `--provenance=false`, so identical builds keep
     their image ID and `up` no longer recreates the container needlessly;
   - run records carry an `interactive` flag.
3. ~~MCP tools, including the `/mcp/k/<key>` fallback.~~ Done (2026-10-31).
   Runs can review *only* through `review_submit`. The proxy denies raw
   review writes to every run key, so no review can skip the commit check.
   `review_submit` also refuses if the PR head has moved since the run
   started. PRs and reviews created by runs carry a footer naming the run,
   the actor and the family. A same-family review starts with an
   owner-override note.
4. ~~Run directories and `run implement`/`verify`/`ls`/`show`/`gc` with the
   claude binding.~~ Done (2026-10-31), with batch and `-I` modes.
   - **Per-run files sit next to the worktree, never inside it:** the record,
     log, prompt and MCP config. The MCP config holds the run key and is
     deleted when the run ends.
   - **The mirror is bare** and fetches into `refs/remotes/origin/*`. With
     `extensions.worktreeConfig`, `core.bare` must live in the mirror's own
     `config.worktree`; otherwise every run worktree thinks it is bare.
   - **Run worktrees call the bottown remote `origin`,** since it is their
     only remote.
   - **Testing hooks:** `BOTTOWN_HARNESS_CMD` runs a script instead of the
     actor's CLI, and `bottown mcp call TOOL JSON` calls a tool with the
     run's key, for scripts and debugging. The e2e suite runs a full
     implement → verify loop this way, without model tokens.
5. ~~The codex binding.~~ The second vendor is **Antigravity (`agy`,
   google)**: rlupi has no Codex subscription. The binding exists and is
   unit-tested:
   - **MCP config:** per run, in the workspace file
     `<worktree>/.agents/mcp_config.json` (`url` + `headers`). It is excluded
     through the mirror's `info/exclude` and deleted at run end. A repo that
     tracks its own copy is refused rather than overwritten.
   - **Batch:** `--dangerously-skip-permissions --output-format stream-json
     --print <prompt>`. agy has no allowlist tier.
   - **Interactive:** `-i <prompt>`.
   - **Not yet run live:** the auto-mode classifier blocks Claude from
     starting agy with `--dangerously-skip-permissions`, so the owner runs
     the first live check.

   **Option:** a `crush` binding with OpenRouter would give actors on many
   model families through one harness, at pay-per-token cost. It is not
   built; its per-run config and headless flags need checking first.
6. ~~e2e~~ Done (2026-10-31) with the scripted harness. The first **live**
   loop also ran on dev: claude implemented issue #3 as PR !4 (52 s), and
   agy approved it with an official review pinned to the PR head. Claude
   runs are isolated from the owner's personal setup: no user-level
   settings, plugins, hooks or auto-memory, and no Claude-host variables.
7. **Permanent actor worktrees and runs from inside the harness** (next;
   see below).
8. Prod: run `bottown up -i prod --ref <commit>`, then bootstrap the fam:
   `bottown clone -i prod ~/src/bottown ~/src/fams/bottown`. Then file and
   pre-register the first issue.

   `bottown clone SRC FAMDIR` (done 2026-10-31, tried on dev) does three
   things:
   - **Clones `FAMDIR/main` from SRC.** SRC is a checkout or URL; only
     committed history is cloned, with a warning if SRC is dirty. `origin`
     is set to SRC's own upstream, never to a local path.
   - **Runs `repo add`:** `bottown/<name>` on the instance, protected, with
     the instance remote.
   - **Runs `actor setup`** for each actor (`--actors` to pick).
9. The real run, with a written record covering:
   - which steps needed you;
   - what the verifier caught;
   - whether any of the §5.4 gotchas appeared.

## Step 7: permanent actor worktrees and in-harness runs

Decided with rlupi on 2026-10-31, after the first live loop. The goal is to
work in the agent harnesses' own UIs and start a run from inside one, with
`/bottown-implement 12`. Ephemeral `.bottown/runs/` worktrees stay supported
for normal use as well (e.g. running a verify while the actor's permanent
worktree is busy), in addition to e2e and dev.

### Layout (the fam layout of `~/src/fams/botfam-new`)

```
<fam>/main      the owner's checkout (the real .git), remotes origin + bottown
<fam>/claude    permanent git worktree of main, actor claude
<fam>/agy       permanent git worktree of main, actor agy
...
```

`bottown actor setup <actor>` (run in `main`) creates or repairs an actor
worktree. Runs then lease it instead of creating a directory.

### Per-actor state lives with the worktree

- **`config.worktree`** (`extensions.worktreeConfig` on) holds non-secret
  state: `bottown.actor`, `bottown.instance`, `bottown.run` (the active run
  id, empty when there is none), `user.name`/`user.email`, and the
  credential helper.
- **The run key** lives in `main/.git/worktrees/<actor>/bottown/run.json`
  (0600), outside the working tree: no `git add -A` can commit it, and it
  is never in git config, where `git config -l` would print it into
  transcripts.
- **Shared `main` config is neutralised per worktree.** Credential helpers
  are reset to the actor's own (no owner key), and `remote.origin.pushurl`
  is set to a blocked value, so agents cannot push upstream with the
  owner's credentials. Fetching from origin still works.

### Starting runs from inside a harness

- **The slash command** `/bottown-implement 12` (and `/bottown-verify 13`)
  is a harness command or skill that calls a new **`run_start`** MCP tool.
- **The actor starts its own run.** The server applies the same checks as
  `POST /v1/runs` (issue open, PR head SHA, author refusal, family rule) and
  binds the run to the calling actor's worktree. The owner can disable
  self-start in the desired state.
- **The worktree is then prepared:** branch `issue/<n>` is checked out, or
  the PR head detached for verify runs. This is refused if the worktree is
  dirty or already in a run.
- **`run_end`** (or `bottown run end`) ends the lease and revokes the key.
- **Outside a run,** MCP allows reads and `run_start` only. Writes need a
  run, so role and repo scoping still hold.
- **`bottown run implement/verify -a <actor> N`** from `main` keeps working.
  It can lease that actor's permanent worktree (then launching the harness
  batch, `-I`, or `--no-launch` to open the UI yourself), or run in an
  ephemeral worktree (`.bottown/runs/<id>/`) as an explicit option (default
  or flag, the owner's call), at least for `run verify`.

### Agents use a CLI, not an MCP shim

Agents reach the forge with `bottown` commands run in their worktree. Every
harness runs its shell tool in the workspace, so the working directory is
reliable. That is not true of stdio MCP servers, which some harnesses start
from a global directory. The CLI reads `bottown.actor` and `bottown.run`
from git config and the key from the worktree's git dir, so harnesses need
no MCP config at all.

```
bottown run start implement 12 | verify 13   # self-start; prints the goal prompt
bottown run end [--failed]                   # handover comment + revoke
bottown whoami                               # actor, run or last run, HEAD vs the default branch
bottown issue list [--state open|closed|all] [--repo R]
bottown issue show [N]                       # defaults to the run's issue
bottown issue create --title T [--body-file F]  # also outside a run
bottown pr list [--state open|closed|all] [--repo R]
bottown pr show [N] [--diff [--full] | --stat]
bottown pr create --title T [--body-file F]
bottown comment [N] (--body B | --body-file F | -)
bottown review submit --event APPROVED|REQUEST_CHANGES|COMMENT --commit SHA (--body-file F | -)
bottown api METHOD PATH [--data JSON]        # passthrough, same policy as the proxy
```

- **Output:** short text by default, `--json` for structured output.
- **One implementation:** the MCP tool logic moves into server functions
  exposed over REST (`/v1/forge/...`). The CLI and MCP share them, so the
  checks are identical. MCP stays available for harnesses that prefer it.
- **Allowlisting is safe.** `Bash(bottown:*)` is a safe allowlist entry,
  unlike `gh` (red-team C5): `bottown` holds no forge token, and the server
  enforces the run's role, repo and commit pin. This also covers agy, which
  has no allowlist tier, for forge calls.
- **The slash command** `/bottown-implement 12` is a small command or skill
  per harness: run `bottown run start implement 12`, then follow the
  prompt it prints.

### Build order for step 7

1. ~~`bottown actor setup`: worktree creation, worktree config, credential
   reset and blocked origin push.~~ Done (2026-10-31), plus `bottown actor
   ls` and `GET /v1/actors`.
   - **Location:** setup refuses unless the checkout is `<fam>/main`, or
     `--path` is given.
   - **Idle state:** the worktree is detached at the bottown remote's
     default branch.
   - **Credentials:** every inherited credential helper is reset, including
     URL-scoped ones such as the owner's bottown helper from `main`. The
     only helper left answers with the run key from the worktree's git dir.
     With no run, git finds no credentials, so even a fetch from bottown
     fails until step 2 adds read-only actor keys.
   - **Pushes:** every remote except bottown gets a blocked `pushurl`.
2. ~~Server: forge operations as shared functions behind REST `/v1/forge/...`
   (MCP tools reuse them); run leases bound to permanent worktrees; actor
   self-start (`run start`), and reads-only outside runs.~~ Done
   (2026-10-31):
   - **Key scopes:** actor keys are read-only by default (reads, git fetch,
     starting runs). `credential key --full` mints the old full key, as an
     owner escape hatch.
   - **Worktree keys:** `actor setup` stores a read-only key at
     `<gitdir>/bottown/actor.json`. The worktree's credential helper uses
     the run key while a run is active, and this read key otherwise.
   - **Self-start:** `POST /v1/runs` accepts an actor's own key, for itself
     only. It refuses `allow_same_family` and a key that already belongs to
     a run. Runs record `started_by`. An actor can read and end its own
     runs. `disable_actor_self_start` turns self-start off.
   - **One implementation:** forge operations live in `server/forge.go`,
     behind `/v1/forge/{issue,pr,comment,review}` and the MCP tools.
   - **Not done:** there are no `run_start`/`run_end` MCP tools; the CLI
     is the primary path.
3. ~~The agent CLI (`run start/end`, `whoami`, `issue`, `pr`, `comment`,
   `review submit`, `api`), resolving the actor and run from the working
   directory.~~ Done (2026-10-31):
   - **Finding the run:** agent commands use `$BOTTOWN_RUN_FILE` (launched
     runs), else the actor worktree they run in. They ignore instance
     resolution errors, since they talk to the server their run points at.
   - **`run start implement|verify N`:** refuses a dirty or busy worktree.
     It self-starts the run with the worktree's read key, checks out
     `issue/<n>` (or detaches at the PR head), records the key in
     `<gitdir>/bottown/run.json`, sets `bottown.run`, and prints the goal
     prompt.
   - **`run end`:** posts the handover from stdin or `--summary`, revokes
     the key and clears `bottown.run`.
   - **Prompts are CLI-first,** with the absolute path of `bottown` and the
     right remote. Claude's batch allowlist includes `Bash(bottown:*)` and
     the absolute path.
4. ~~`/bottown-implement` and `/bottown-verify` commands for claude, then
   agy.~~ Done (2026-10-31): `bottown harness setup [claude|agy]` installs
   them globally, naming `bottown` by absolute path.
   - **claude:** `~/.claude/commands/bottown-{implement,verify}.md`. The
     command itself runs `` !`bottown run start <kind> '$ARGUMENTS'` ``, which
     inlines the goal prompt, with `allowed-tools: Bash(bottown:*)`. The
     quotes matter: unquoted, the shell reads `#12` as a comment (#5).
   - **agy:** `~/.gemini/config/global_workflows/bottown-{implement,verify}.md`.
     A workflow cannot run commands itself, so it tells the agent to run
     `bottown run start <kind> <number>`, the number as digits only, and
     follow its output.
   - **Arguments:** `run start` accepts what users type,
     `/bottown-implement issue #12` as well as `12`, as separate words or
     as one quoted word.
   - **Safe re-runs:** re-running refreshes the files; a same-named file
     bottown did not write is left alone unless `--force`.
   - **Fallback for other harnesses:** a skill or instructions file that
     tells the agent which `bottown` commands to run. Every harness has a
     shell, so this works without harness-specific command support.
5. `bottown run implement/verify` from `main`: support leasing permanent
   worktrees (`--no-launch`, batch, `-I`), while keeping ephemeral runs
   supported as an explicit option (default or flag, the owner's call), at
   least for `run verify`. `run ls` reads the worktrees' state.
6. e2e: a scripted run through the CLI and an actor-started run; check that
   an owner-key push from an actor worktree is impossible.

## Done when

- The pre-registered issue is merged on prod.
- The commits are by the implementer actor.
- The approving review is by an actor of another family, pinned to the PR's
  final head SHA.
- Both runs show in `run ls` with their final status.
- The written record exists.
