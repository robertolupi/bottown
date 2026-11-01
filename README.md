# bottown

bottown is a multi-agent harness for one developer on one machine. A private
[Forgejo](https://forgejo.org) forge in a container is the control plane:
you file issues there, coding agents ("actors") work them in runs that
bottown scopes and records, a verifier from another model family reviews
each pull request at the exact commit, and you merge.

What bottown is for and how it is designed is in [docs/](docs/index.md);
instructions for agents working on this repository are in
[AGENTS.md](AGENTS.md).

## Requirements

- **Go** (the version in [go.mod](go.mod)), to build the CLI.
- **Node and npm**, to build the web UI (`make web`, which `make install`
  runs). The built UI is not tracked: a binary built without it, such as
  `make build`'s, serves a page that says to run `make web`.
- **Docker**, running: each instance is one container with Forgejo and the
  bottown server. `make e2e` also needs it.
- **git**.
- **An agent harness** for each actor you want to run: today
  [Claude Code](https://claude.com/claude-code) (`claude`), Antigravity
  (`agy`), [Kimi Code](https://www.kimi.com/code/docs/en/) (`kimi`),
  [Codex CLI](https://openaicli.com/docs) (`codex`) and
  [Crush](https://github.com/charmbracelet/crush) (`crush`).
- macOS is the tested host: the owner's keys live in the Keychain. On other
  systems they go to a `0600` file under the instance's host config.

## Install

```sh
git clone <this repository> bottown
cd bottown
make install            # writes ~/.local/bin/bottown; put it on your PATH
```

`make install` also builds the web UI (`make web`), refreshes the harness
slash commands (`bottown harness setup`, below) and, in a fam's main
checkout whose HEAD is `bottown/main`, upgrades the prod server to that
commit (`bottown up --ref HEAD --if-changed`); `UP=0` skips that step. It refuses to run from a linked
worktree, such as an actor's. `make build` writes `bin/bottown` instead,
without replacing the installed binary.

## Tutorial

### 1. Bring up an instance

The default instance is `prod` (Forgejo on port 3030, bottown on 3031).
`prod` only runs an image built from an explicit commit, never a working
tree, so from your bottown checkout:

```sh
bottown up --ref "$(git rev-parse HEAD)"
bottown status
bottown web forgejo     # opens Forgejo signed in as the owner
```

`bottown up` provisions everything: the owner account, the actor and
manager accounts, teams, labels and branch protection. Re-running it
converges and is otherwise a no-op. To experiment first, use a disposable
instance: `bottown up -i dev` builds from the working tree (ports
3040/3041), and every other command takes `-i dev` too.

Install the slash commands the actors use inside their harness:

```sh
bottown harness setup   # /bottown-implement, /bottown-verify, /bottown-discuss
```

In kimi they are skills: `/skill:bottown-implement 1`; in codex, skills in
`~/.codex/skills/`: `$bottown-implement 1`; in crush, skills in
`~/.config/crush/skills/`: `/bottown-implement 1`. Codex setup also adds allow rules
for plain `bottown` and the setup binary’s absolute path to
`~/.codex/rules/default.rules`, preserving other rules. `CODEX_HOME` relocates
both skills and rules. Restart Codex after setup to load the rules; more
restrictive managed or user rules still apply.

### 2a. Start a new project: a README as the first change

A repository needs one commit before it can be hosted. Make an empty one,
then let `bottown clone` set up the *fam*: the owner's checkout plus one
permanent worktree per actor.

```sh
mkdir -p ~/src/hello && cd ~/src/hello
git init -b main
git commit --allow-empty -m "Initial commit"

bottown clone ~/src/hello ~/src/fams/hello
```

This creates `bottown/hello` on the instance and:

```
~/src/fams/hello/main     your checkout; its `bottown` remote is the forge
~/src/fams/hello/claude   actor worktree for claude
~/src/fams/hello/agy      actor worktree for agy
…
```

`~/src/hello` has no upstream, so neither does `main`; add one later with
`git remote add origin URL`.

File the first issue in Forgejo (`bottown web forgejo` from
`~/src/fams/hello/main` opens the repository), for example
**#1 Write README.md** with a sentence on what the project is. Then have
one actor implement it and an actor from another family verify it. Either
start the run from the actor's worktree, inside its harness:

```sh
cd ~/src/fams/hello/claude && claude
> /bottown-implement 1
```

or from `main`, where bottown starts the harness in a fresh run worktree
(`-I` gives you its interactive UI, except for kimi, which cannot start
its UI with a prompt):

```sh
cd ~/src/fams/hello/main
bottown run implement -a claude 1
```

The implementer pushes `issue/1` and opens a pull request, say !2. Verify
it with another family:

```sh
cd ~/src/fams/hello/agy && agy     # then: /bottown-verify 2
# or, from main:
bottown run verify -a agy 2
```

When the verifier approves, merge !2 in Forgejo and pull it:

```sh
cd ~/src/fams/hello/main
git pull --ff-only bottown main
```

A repository can declare what to run after pulling in a `.bottown.toml`
at its root, `after_pull = ["make install"]`; bottown reads it from the
forge's `main` only. `bottown web ui` shows those steps after its Pull and
runs them only when you confirm.

If the verifier requests changes, start a new implement run on the same
issue: it resumes `issue/1`, and the new head needs a new verify run.

### 2b. Import an existing repository

From a local checkout or a URL:

```sh
bottown clone ~/src/myproject ~/src/fams/myproject
bottown clone https://github.com/you/myproject.git ~/src/fams/myproject
```

The branch checked out in the source (unpushed commits included) becomes
the default branch on the instance, and `main`'s `origin` stays the real
upstream: the source's own `origin`, `--origin URL`, or the URL itself.
Only committed history is copied. Actors can never push to `origin`; you
publish releases from `main` yourself.

To host a checkout in place instead of making a fam, run `bottown repo add`
inside it (it adds the `bottown` remote and pushes the current branch),
then `bottown actor setup claude agy` to create the actor worktrees next to
it.

From there it is the same loop as above: file issues, run implement and
verify, merge.

## Command guide

Every command takes `-i/--instance NAME` (default: `$BOTTOWN_INSTANCE`, else
inferred from the checkout's remotes, else `prod`). `bottown COMMAND --help`
has the details.

### Instances (owner)

| Command | What it does |
|---|---|
| `up [--ref COMMIT [--if-changed] \| --image TAG]` | Create or converge an instance: image, container, provisioning. `--if-changed` keeps the current image when the commits differ only in docs. |
| `status` | Container and provisioning state. |
| `logs [-f] [--tail N]` | Container logs (Forgejo and bottown). |
| `down [--purge]` | Stop an instance; `--purge` deletes its data for good. |
| `image build [--ref COMMIT]` | Build a bottown image without running it. |
| `web forgejo`, `web ui` | Open Forgejo or the bottown web UI signed in as the owner. |
| `credential password` | Print the owner's initial Forgejo web password. |
| `version` | Print the bottown version. |

### Repositories and actors (owner)

| Command | What it does |
|---|---|
| `clone SRC FAMDIR` | Make a fam: `FAMDIR/main` from SRC, hosted as `bottown/NAME`, plus actor worktrees. |
| `repo add [NAME]` | Host the current checkout on the instance under a `bottown` remote, with labels and protection. |
| `actor setup ACTOR…` | Create or repair actor worktrees (in the main checkout). |
| `actor ls` | List the main checkout's actor worktrees. |
| `harness setup [claude\|agy\|kimi\|codex\|crush]…` | Install the `/bottown-*` slash commands into the harnesses. |
| `git gc-branches [--dry-run] [--pattern P]` | Delete `issue/*` branches merged into the default branch, on the forge and locally (in the main checkout). Keeps any branch checked out in a worktree, with a live run, or with an open PR. |

### Runs (owner, from the main checkout)

| Command | What it does |
|---|---|
| `flow N --implement ACTOR --verify ACTOR [--rounds N] [--timeout D]` | Drive one issue through batch implement and cross-family verify runs, stopping at the owner action. |
| `run implement -a ACTOR [-I] ISSUE` | An agent implements the issue: pushes `issue/N`, opens a PR. |
| `run verify -a ACTOR [-I] PR` | An agent reviews the PR at its current head and submits one verdict. `--allow-same-family` waives the cross-family rule. |
| `run discuss -a ACTOR [-I] N [QUESTION]` | An agent reviews or answers a question on an issue, in a comment, without implementing. |
| `run ls [--all]` | List runs: local worktrees merged with the server's records. |
| `run show RUN` | A run's local record, server record (with its summary and the usage its harness reported) and log tail. |
| `run stop RUN` | Kill a live batch run's harness, end it as failed and post a stopped handover. |
| `run gc [--failed] [--older-than D]` | Remove finished runs, and revoke runs whose key expired while live. The server keeps their digests: summary and usage. |

Each actor can have one live batch run by default; interactive runs do not
count. To change this, set `max_batch_runs = N` in the actor's `[[actors]]`
table in the desired-state `bottown.toml` mounted at
`/data/bottown/bottown.toml` in the container. Set it to `0` for no limit.
The server checks and creates runs atomically, so concurrent commands cannot
exceed the limit.

### Agent CLI (actors, in their worktree)

These act as the worktree's actor, within its current run's role and
repository. They hold no token: the server enforces the policy.

| Command | What it does |
|---|---|
| `whoami` | The actor, its run or last run, and HEAD against the default branch. |
| `run start implement\|verify\|discuss N` | Start a run and print its goal prompt (what the slash commands call). |
| `run end [--failed] --summary-file -` | End the run with a handover summary. |
| `issue list`, `issue show [N]` | Read issues. |
| `issue create --title T --body-file -` | File an issue, in or outside a run. |
| `pr list`, `pr show [N] [--diff \| --stat]` | Read pull requests. |
| `pr create --title T --body-file -` | Open the PR for an implement run's pushed branch. |
| `comment [N] --body-file -` | Comment on the run's issue, or the PR in a verify run. |
| `review submit --event E --commit SHA --body-file -` | A verify run's one verdict: `APPROVED`, `REQUEST_CHANGES` or `COMMENT`. |
| `api METHOD PATH [--data JSON]` | Any Forgejo API endpoint, under the same policy. |

Plumbing that bottown sets up for itself: `credential` (git credential
helper), `serve` and `admin` (inside the container), and `mcp call`.

## Development

```sh
make build    # bin/bottown (the web UI only if make web ran)
make web      # the web UI, from web/ into internal/webui/dist/ui (needs npm)
make check    # gofmt, go mod tidy, vet, unit tests
make e2e      # end-to-end tests on throwaway e2e-* instances (needs Docker)
```

## License

[MIT](LICENSE.md).
