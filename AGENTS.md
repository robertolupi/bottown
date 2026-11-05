# bottown: instructions for agents

bottown is a multi-agent harness for one developer on one machine. A private
Forgejo in a container is the control plane; coding agents ("actors") work
issues through runs that bottown scopes and records. bottown is developed with
itself: this repository is hosted on the prod instance as `bottown/bottown`.

> [!IMPORTANT]
> **Do not use `botfam` tools, CLI commands, or MCP servers.** `botfam` is the
> predecessor project (v1) and is entirely obsolete. bottown replaces it.
> Do NOT call `botfam` MCP tools (such as `orient`, `worktree_init`,
> `worktree_sync`, or `forge_*`) or run `botfam` CLI commands. All coordination
> and forge operations happen exclusively through the `bottown` CLI
> (`~/.local/bin/bottown`) and the local Forgejo control plane.

Read [docs/index.md](docs/index.md) first. `docs/` holds only current docs;
records of past work are in `docs/history/`: iteration 1's plan and build
record in [docs/history/iteration-1.md](docs/history/iteration-1.md), and the
bootstrap design, including Forgejo quirks we found, in
[docs/history/bootstrap.md](docs/history/bootstrap.md).

## Working in the fam

This repository is developed in a bottown fam, like any project bottown
hosts. What every fam shares (the layout, the `bottown` remote, how runs
start, the agent CLI and what is allowed outside a run) is in the fam guide:
run `bottown guide` in your checkout. Actor worktrees also have it, untracked,
in the file their harness loads (claude: `CLAUDE.local.md`), and runs get the
same rules in their goal prompt. The guide's source is
[internal/runs/prompts/fam.md](internal/runs/prompts/fam.md).

What is particular to developing bottown itself:

- **This fam is `~/src/fams/bottown`:** `main` is the owner's checkout (the
  real `.git`), and `claude`, `agy`, `codex` and `kimi` are actor worktrees.
- **Checks:** "the repository's checks" in your prompt means `make check`.
  Implementers need not run `make e2e` before opening a PR (they may run
  the e2e tests their change touches): `bottown flow` runs it itself at
  each new PR head, in a clean worktree, before it starts a verifier, and
  sends a failure back as another implement round. A verifier whose prompt
  says the flow's checks passed at its SHA does not run `make e2e`; one
  started by hand runs it. `bottown check --e2e` adds `make e2e` to the
  checks.
- **Never run `make install` from an actor worktree.** It replaces the
  shared `~/.local/bin/bottown`, which every actor and the harness commands
  use, with your branch's build, and can upgrade prod. It refuses to run
  from a linked worktree. Build with `make build` (`bin/bottown`).

## Code

Go module `github.com/robertolupi/bottown`; the CLI is `cmd/bottown`.

| Package | Role |
|---|---|
| `internal/cli` | All commands: host (`up`, `clone`, `actor`, `run`, `web`, …), container (`serve`, `admin`), agent CLI |
| `internal/flow` | Single-issue supervisor: implement, cross-family verify, rounds, resume and stop reasons |
| `internal/server` | bottown's HTTP server: health, runs, `/v1/forge`, MCP, the API and git proxies |
| `internal/client` | The one client for host-side calls to the server: timeouts, typed `*APIError` and sentinels |
| `internal/policy` | Proxy policy: the base deny-list, and run scope by role and repo |
| `internal/provision` | Reconciler that converges Forgejo to the desired state |
| `internal/config` | Desired state (`default.toml`) and per-instance host config |
| `internal/forgejo` | Forgejo SDK client, its extensions, the admin CLI, web login |
| `internal/runs` | Run worktrees, records, prompts, harness bindings (claude, agy) |
| `internal/secrets`, `internal/hostctl` | In-container secret store; Docker CLI and Keychain on the host |

- **Build:** `make build` writes `bin/bottown`. The web UI's built assets
  (`internal/webui/dist/ui`) are not tracked: `make web` builds them from
  `web/` (needs Node and npm, running `npm ci` when `web/node_modules` is
  missing or out of date); without them the binary serves a stub page.
  Change `web/src`, not built output.
- **Install:** `make install` builds the web UI, writes
  `~/.local/bin/bottown`, which the harness commands and credential helpers
  use, runs `bottown harness setup`, and, when HEAD is `bottown/main`, `bottown up -i prod --ref HEAD
  --if-changed` (`UP=0` skips it). `--if-changed` keeps prod's image when
  the commits differ only in docs.
- **Tests:** `make test` runs the Go unit tests (`go test ./...`) and the
  frontend behavioral tests (`cd web && npm test`, after `npm ci` if
  needed). `make e2e` needs Docker and starts throwaway `e2e-*` instances;
  it never touches prod or dev.
  `make e2e-linux` runs `make check` and `make e2e` on Linux in a
  Multipass VM ([docs/index.md](docs/index.md)); it is optional, and
  runs need not use it.
- **Checks:** `make check` is what "the repository's checks" means to
  implementers and verifiers (needs Go, Node and npm; installs
  `web/node_modules` reproducibly with `npm ci` on a clean checkout):
  `gofmt -l` clean, `go mod tidy -diff` clean, `make vet` (including
  `-tags e2e`), `make test` (Go and frontend tests) and `cd web && npm run
  check` (`svelte-check`).
- **Before a PR:** `make check` passes, and `make e2e` should (the flow
  runs it before a verifier sees the PR). New behaviour gets a test. Keep the docs in `docs/` true to the code; `docs/history/` is a
  record and is not kept current.
- **Style:** match the surrounding code, comment density included. Commit
  messages say what changed and why.

## Instances

| Instance | Ports (Forgejo/bottown) | Rules |
|---|---|---|
| `prod` | 3030 / 3031 | Hosts this repo. Never `down --purge` it. Upgrade only with `bottown up -i prod --ref <commit>`, from a commit on `main`. |
| `dev` | 3040 / 3041 | Disposable; `bottown up -i dev` rebuilds it from the working tree. |
| `e2e-*` | 3050+ | Created and purged by `make e2e`. |

**After merging (owner),** in `main`:

1. `git pull --ff-only bottown main`. The checkout does not track prod's
   `main` by itself, and the next step builds from it.
2. `make install`. It installs the CLI (including the run prompts, which
   the CLI renders), refreshes the slash commands, and upgrades prod to
   HEAD, which after step 1 is `bottown/main`. It skips the prod step,
   saying so, when HEAD is not `bottown/main`.

<!-- BEGIN TRANSITION: remove this section once every item below is done. -->
## Transition from the bootstrap (temporary)

bottown was built in `~/src/bottown` and moved here on 2026-10-31. Until this
section is removed:

- **Do not commit in `~/src/bottown`.** It is historical, and its commits
  reach neither this fam nor prod.
- **Some changes still bypass runs.** The owner, or Claude acting for the
  owner in `main`, may commit and push directly while the run commands are
  finished (step 7.5 of [iteration 1](docs/history/iteration-1.md)).
  Afterwards every change goes through an issue and a run.

Done when all of these hold; then remove this section in a run:

- [x] `bottown/main` deleted on prod (owner).
- [ ] Step 7.5 merged: `bottown run implement/verify` lease permanent
      worktrees, with ephemeral runs kept as a supported option.
- [x] The first pre-registered issue landed through a run: an implementer
      PR, a cross-family verifier approval, an owner merge (#10 via !24).
- [ ] `~/src/bottown` archived or removed (owner).
<!-- END TRANSITION -->
