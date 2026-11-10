# Checks

Runs check their work with `bottown check [--e2e]`, which runs the
repository's checks: the commands `check` in `.bottown.toml` names, then,
with `--e2e`, those `e2e` names (by default `make check` and `make e2e`;
[web-ui.md §4.4](../design/web-ui.md#44-repository-settings)). Like every
setting there, the commands are read from the forge's default branch, so
a branch cannot change what its own checks are.

- **How.** Each command runs with `sh -c` at the checkout's root, in
  order; after a failure the rest are skipped, and the command exits
  non-zero.
- **The log.** The full output goes to a log, replaced by the next check:
  `.bottown/runs/<id>.check.log` in a launched run (removed with the run's
  other files by `run gc`), `<gitdir>/bottown/check.log` in an actor
  worktree. The summary names it and lists each step with its result and
  duration, then, for a failed step, its first lines that look like a
  failure (Go test failures, compiler positions, panics, make's error
  line, npm and svelte-check errors), else its last lines, each with its
  line number in the log, so the agent reads the log there rather than
  re-running the checks.
- **Dependencies.** The summary flags the dependency manifests (`go.mod`,
  `package.json`, `Cargo.toml`, `pyproject.toml`, `requirements*.txt`, …;
  not lock files) that HEAD changes since its merge base with the default
  branch, with the first lines each adds and removes, so a verifier
  notices new third-party packages.
- **The record.** In an implement, plan or verify run, the result of each
  group that ran is recorded on HEAD as a commit status, `bottown/check`
  or `bottown/e2e` (success or failure, with a description naming the
  commands, the duration and the run; a failure also names the failing
  step's failing tests, Go's `--- FAIL:` and vitest's `FAIL` lines, as
  many as fit, e.g. `failed: make check (exit 2) after 59.6s:
  TestKeychainResults (verify run r-63a64a3e)`; the flow's checks
  describe their statuses the same way), through `POST /v1/forge/status`.
  The server writes it with the actor's token, only on the run's commit:
  a verify run's pinned SHA, or the head of an implement or plan run's
  branch on the forge, so the commit must be pushed first. Agents cannot
  write statuses through the API proxy. Nothing is recorded outside a
  run, in a discuss run, or when the worktree had uncommitted changes.
  `bottown check --report` records the last result again without
  re-running, once its commit is pushed. A status says what the run's
  actor reports it ran; it is not a verification.
- **Where it shows.** `pr show` lists the statuses at the head, the newest
  per context and creator, so the implementer's and the verifier's
  results show side by side (`checks` in `--json`, and in the MCP
  `pr_get`).
- **The flow's own run.** `bottown flow` runs the `e2e` commands itself
  at each new PR head before it starts a verifier, in a clean worktree
  with no agent, and records them as the owner's `bottown/e2e` status
  (marked `gate` in `--json`); the log is
  `.bottown/runs/flow-<issue>-<sha>-<check>.e2e.log`, which the web UI's Live
  Runs and the flow's terminal tail follow while the commands run, as
  they do a run's, and which `run gc` removes once the issue has no open
  PR (or, with `--older-than`, once it is that old). A pass goes into the verify
  prompt, which then asks for `bottown check` without `--e2e`; a failure
  starts another implement round with the failure in its prompt
  ([web-ui.md §3.2](../design/web-ui.md#32-the-loop)). So implementers need
  not run `--e2e` before opening a PR.

  The checker the flow uses can run either group or both, in a gate's
  request: `check` and then `e2e`, the commands `.bottown.toml` on the
  default branch names, one after the other in the same clean worktree
  detached at the head, stopping at the first group that fails. The flow
  and `milestone finish` ask for `e2e` alone. Each group logs to its own
  file, `.bottown/runs/flow-<issue>-<sha>-<check>.check.log` and
  `.e2e.log`, and while it runs a record
  (`flow-<issue>-<sha>-<check>.<group>.check`) shows it on the web UI's Now
  view as a tile of its own, named for its group. A group the repository names no
  commands for (`check = []`, `e2e = []`) is skipped, and the outcome says
  so (`the repository names no e2e commands`); with no group to run the
  whole check is skipped, as the flow's gate is when there are no `e2e`
  commands. A gate records each group that ran at the head as the owner's
  `bottown/check` or `bottown/e2e` status with the `gate` mark, through
  the path the flow's e2e gate records by (`POST /v1/forge/status`;
  `milestone finish` records none, and puts its result in the final PR's
  description). The outcome lists which groups ran and which failed, and
  its description, for more than one group, names each group's result
  after the group.
