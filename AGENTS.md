# bottown: instructions for agents

bottown is a multi-agent harness for one developer on one machine. A private
Forgejo in a container is the control plane; coding agents ("actors") work
issues through runs that bottown scopes and records. bottown is developed with
itself: this repository is hosted on the prod instance as `bottown/bottown`.

Read [docs/index.md](docs/index.md) first. The current plan and its status
are in [docs/iteration-1.md](docs/iteration-1.md), and the bootstrap design,
including Forgejo quirks we found, is in [docs/bootstrap.md](docs/bootstrap.md).

## The fam layout

```
~/src/fams/bottown/main     the owner's checkout (the real .git)
~/src/fams/bottown/claude   actor worktree for claude
~/src/fams/bottown/agy      actor worktree for agy
~/src/fams/bottown/codex    actor worktree for codex
~/src/fams/bottown/kimi     actor worktree for kimi
```

- **`main` is the owner's.** Actors work only in their own worktree, and
  never edit `main` or another actor's worktree.
- **The `bottown` remote is the prod forge.** `main` is protected: only the
  owner pushes or merges there. Actors push their `issue/<n>` branch during
  a run.
- **`origin`, if present, is the external upstream.** Pushes to it are
  blocked in actor worktrees.

## How actors work

Work happens in runs. In your actor worktree:

```
bottown run start implement 12      # or: /bottown-implement 12
bottown run start verify 13         # or: /bottown-verify 13
```

`run start` prints the goal prompt for the run: follow it. Use the bottown
CLI for the forge; it holds no token, and the server enforces your run's role
and repository:

```
bottown whoami
bottown issue list [--state open|closed|all] [--repo R] [--json]
bottown issue show [N]
bottown issue create --title T --body-file -
bottown pr list [--state open|closed|all] [--repo R] [--json]
bottown pr show [N] [--diff]
bottown pr create --title T --body-file -
bottown comment [N] --body-file -
bottown review submit --event APPROVED|REQUEST_CHANGES|COMMENT --commit SHA --body-file -
bottown api METHOD PATH [--data JSON]
bottown run end [--failed] --summary-file -
```

- **Implementers** push `issue/<n>` and open a PR. They cannot review or
  merge.
- **Verifiers** review exactly the commit the run pinned, with one verdict.
  They cannot push.
- **Anyone can file issues,** in or outside a run: follow-ups found while
  working, or what the owner asks you to file from a conversation. Outside
  a run the repository is the one your worktree's `bottown` remote points
  at. Filing is the one write a read-only key may make.
- **Cross-family rule:** a verifier must be from another model family than
  the author. Only the owner can waive it.
- **If an issue is unclear or you are blocked,** comment on the issue and end
  the run with `--failed`. Do not guess, and do not work outside the issue.

## Code

Go module `github.com/robertolupi/bottown`; the CLI is `cmd/bottown`.

| Package | Role |
|---|---|
| `internal/cli` | All commands: host (`up`, `clone`, `actor`, `run`, `web`, …), container (`serve`, `admin`), agent CLI |
| `internal/server` | bottown's HTTP server: health, runs, `/v1/forge`, MCP, the API and git proxies |
| `internal/policy` | Proxy policy: the base deny-list, and run scope by role and repo |
| `internal/provision` | Reconciler that converges Forgejo to the desired state |
| `internal/config` | Desired state (`default.toml`) and per-instance host config |
| `internal/forgejo` | Forgejo SDK client, its extensions, the admin CLI, web login |
| `internal/runs` | Run worktrees, records, prompts, harness bindings (claude, agy) |
| `internal/secrets`, `internal/hostctl` | In-container secret store; Docker CLI and Keychain on the host |

- **Build:** `make build` writes `bin/bottown`.
- **Install:** `make install` writes `~/.local/bin/bottown`, which the
  harness commands and credential helpers use.
- **Tests:** `make test` runs the unit tests. `make e2e` needs Docker and
  starts throwaway `e2e-*` instances; it never touches prod or dev.
- **Before a PR:** `make vet`, `make test` and `make e2e` pass. New
  behaviour gets a test. Keep the docs in `docs/` true to the code.
- **Style:** match the surrounding code, comment density included. Commit
  messages say what changed and why.

## Instances

| Instance | Ports (Forgejo/bottown) | Rules |
|---|---|---|
| `prod` | 3030 / 3031 | Hosts this repo. Never `down --purge` it. Upgrade only with `bottown up -i prod --ref <commit>`, from a commit on `main`. |
| `dev` | 3040 / 3041 | Disposable; `bottown up -i dev` rebuilds it from the working tree. |
| `e2e-*` | 3050+ | Created and purged by `make e2e`. |

<!-- BEGIN TRANSITION: remove this section once every item below is done. -->
## Transition from the bootstrap (temporary)

bottown was built in `~/src/bottown` and moved here on 2026-10-31. Until this
section is removed:

- **Do not commit in `~/src/bottown`.** It is historical, and its commits
  reach neither this fam nor prod.
- **Some changes still bypass runs.** The owner, or Claude acting for the
  owner in `main`, may commit and push directly while the run commands are
  finished (step 7.5 of the plan). Afterwards every change goes through an
  issue and a run.
- **A stray prod repo, `bottown/main`,** was created by a since-fixed
  `repo add` bug. It is not this repository; ignore it until the owner
  deletes it.

Done when all of these hold; then remove this section in a run:

- [ ] `bottown/main` deleted on prod (owner).
- [ ] Step 7.5 merged: `bottown run implement/verify` lease the permanent
      worktrees.
- [ ] The first pre-registered issue landed through a run: an implementer
      PR, a cross-family verifier approval, an owner merge.
- [ ] `~/src/bottown` archived or removed (owner).
<!-- END TRANSITION -->
