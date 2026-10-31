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
- **Migration:** the reconciler creates the actor accounts. Old accounts
  (`implementer`, `verifier`) are not deleted automatically; a doc step says
  how to remove them.

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
    edits. They may read, comment, and submit a review.
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
bottown run implement -a <actor> <issue>
bottown run verify -a <actor> [--allow-same-family] <pr>
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

`run ls` columns are run, kind, target, actor, family, status, branch or SHA,
age and size.

### Harness bindings (iteration 1)

- **claude:** `claude -p` with stream-json output, `--mcp-config` pointing at
  the run's file, and an allowlist of `mcp__bottown__*` plus the repo's build
  and test commands.
- **codex:** the second vendor for verification. Remote MCP using a bearer
  token from an environment variable; check the exact config keys when
  building this.
- **agy and kimi:** bindings are added when needed; their actor accounts
  exist from the start.

## Build order

1. Actors in desired state, plus migration, the `actors` team and
   protection.
2. Run keys: the server endpoints, run records and proxy role and repo
   scoping. Unit tests for the policy.
3. MCP tools, including the `/mcp/k/<key>` fallback.
4. Run directories and `run implement`/`verify`/`ls`/`show`/`gc` with the
   claude binding.
5. The codex binding.
6. e2e:
   - a fake harness (a script) runs the implement and verify paths
     end-to-end;
   - the role policy denies cross-role and cross-repo calls;
   - the family rule refuses a same-family verify without the override and
     allows it with one;
   - `run ls` shows each state, and `gc` removes finished runs.
7. Prod: run `bottown up -i prod --ref <commit>`, then `bottown repo add`.
   File and pre-register the first issue.
8. The real run, with a written record covering:
   - which steps needed you;
   - what the verifier caught;
   - whether any of the §5.4 gotchas appeared.

## Done when

- The pre-registered issue is merged on prod.
- The commits are by the implementer actor.
- The approving review is by an actor of another family, pinned to the PR's
  final head SHA.
- Both runs show in `run ls` with their final status.
- The written record exists.
