# bottown reference

How bottown behaves today, one topic per section. Read the section you
need, not the whole file; [index.md](index.md) is the orientation page.

## Forge reads and pagination

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

## Issue attachments

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

## Checks

Runs check their work with `bottown check [--e2e]`, which runs the
repository's checks: the commands `check` in `.bottown.toml` names, then,
with `--e2e`, those `e2e` names (by default `make check` and `make e2e`;
[web-ui.md §4.4](design/web-ui.md#44-repository-settings)). Like every
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
  commands, the duration and the run), through `POST /v1/forge/status`.
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
  `.bottown/runs/flow-<issue>-<sha>.e2e.log`, which the web UI's Live
  Runs and the flow's terminal tail follow while the commands run, as
  they do a run's, and which `run gc` removes once the issue has no open
  PR (or, with `--older-than`, once it is that old). A pass goes into the verify
  prompt, which then asks for `bottown check` without `--e2e`; a failure
  starts another implement round with the failure in its prompt
  ([web-ui.md §3.2](design/web-ui.md#32-the-loop)). So implementers need
  not run `--e2e` before opening a PR.

## Linux e2e

bottown is developed on macOS; `make e2e-linux` (`scripts/linux-e2e.sh`)
runs `make check` and `make e2e` on Linux. It needs
[Multipass](https://multipass.run) and runs them in an Ubuntu VM,
`bottown-linux` (4 CPUs, 8G memory, 30G disk; `BOTTOWN_LINUX_*` override
the name, size, Ubuntu image and Node major), with Docker installed by
cloud-init, the Go that `go.mod` names and the newest Node of the host's
major (26 without one), since `web/` declares none. Its user gets a global
git identity, as a developer's machine has.
The VM is a real Linux host, so the `e2e-*` containers are reachable at
`127.0.0.1` as on the Mac, which a container sharing the host's Docker
socket would not give. The VM is created on first use and kept for re-runs;
`--fresh` (`make e2e-linux ARGS=--fresh`) recreates it and `--delete`
removes it afterwards. It tests HEAD, not the working tree: HEAD and the
tags, as a git bundle, cloned into a directory of the checkout's own in
the VM, so the commit and `git describe` match. Runs take turns, whatever
checkout they come from: each holds a lock on the host
(`/tmp/bottown-linux-e2e-<vm>.lock`, with `flock` or macOS's `lockf`) from
before it touches the VM until it exits, so none deletes the VM or changes
its toolchains under another; each puts its own Go and Node first in its
`PATH`. The output, setup included, goes to `<gitdir>/bottown/linux-e2e.log`
(`BOTTOWN_LINUX_LOG`), and the script exits with the checks' status. It
covers linux/arm64 on Apple Silicon, where Multipass runs only arm64
guests; not amd64, and not the real agent harnesses, which e2e replaces
with `BOTTOWN_HARNESS_CMD` scripts. It is not one of the repository's
checks, and neither `bottown check --e2e` nor `bottown flow` runs it:
the owner runs it by hand before pushing `main` to GitHub. Until #305 is fixed,
two e2e tests usually fail in the VM: the server reads a PR's diff and
reviews before Forgejo has processed a push, which the slower VM exposes.

## Runs

### Reusing an issue branch

A retry on the same issue can reuse `issue/<n>` even when an earlier run
kept its worktree. Before creating the server run, the launcher detaches
that old worktree, preserving its commits and working files for inspection.
If its launcher is still alive, the start refuses with the holding run's
ID instead.

### Scratch directories

Launched runs get a private scratch directory under the launcher's temporary
directory, named `bottown-<workspace hash>-<run id>`. The harness receives it
as `TMPDIR` (and Claude receives it through `--add-dir`), so tests that need
temporary directories outside a git checkout can run normally. The launcher
removes it when the run returns; `run gc` uses the path saved in the local
record if the launcher died. Older runs' scratch directories beside their
worktrees are also removed by gc.

### What run gc removes

What `run gc` removes, besides the server's records of ended runs: each
finished run's worktree and its `<id>.*` files (succeeded and dead runs;
failed ones with `--failed`); `bottown flow`'s e2e logs,
`.bottown/runs/flow-<issue>-<sha>.e2e.log`, once the issue has no open PR
from `issue/<n>`, or with `--older-than` once the log is that old (while the
PR is open a resumed flow points to the log rather than checking again);
and the web UI's flow logs, `.bottown/flow-<issue>.log`, once that flow is
not running, that is, no `flow-<issue>.json` names a live process. Its
summary line counts the runs and the flow logs it removed. A run's
`<id>.check.json` is its own file, removed with it, and listing runs does
not warn about it.

### Retro runs

`bottown run retro -a ACTOR [-I] ISSUE` starts a run that writes a
blameless retrospective of an issue's flow from bottown's records. The flow
of issue N is every run whose issue is N or whose branch is `issue/N`, and
every run on one of the issue's pull requests (those Forgejo has from
`issue/N`, open or closed, or that a run's record ties to that branch),
such as a discuss run on the PR: its implement, plan, discuss and verify
runs, live or ended, purged runs' digests included; earlier retros too, but
not the retro itself.

N may be a pull request. One from an `issue/M` branch stands for issue M's
flow: the evidence is M's, and the prompt says the target is PR N of issue
M. One from another branch is a flow of its own: the runs on that pull
request or its head branch. Either way the retro is posted on N. The
launcher's selection of runs and the server's cross-family check below use
the same definition of the flow (`server.RetroFlow`).

Before the agent starts, the launcher gathers that evidence with the owner
key, since a run key may not list runs or read the audit log, into
`$TMPDIR/retro/` in the run's scratch directory:

- `README.md`: the runs, oldest first, at a glance.
- `runs.json`: each run's server record and digest (summary, usage,
  observed failure), the launcher's local record if this checkout launched
  it (exit code, stop request, notes, handover; never the key), and its
  policy denials and rejections.
- `transcripts/<id>.jsonl`: the run's log, as one `runs.LogLine` JSON line
  per message, tool call, tool output or status event, the format the web
  UI's log view uses for every harness, with each text field cut at 4000
  bytes. Interactive runs have no log, so they have no transcript.
- `prompts/<id>.md`: the run's goal prompt.

If gathering fails, the prompt says why, and the run works from the forge.
The agent reads the issue, the PR threads, reviews and commit statuses with
the agent CLI, and posts the retro as a comment on the issue. It may file
follow-up issues. Like a discuss run, it cannot push, open pull requests or
review, and its worktree is detached at the default branch's head. Unlike a
discuss run, its file edits are not refused: it may draft a design note or
a proposal in the worktree, and the launcher keeps a retro's worktree when
it has uncommitted changes, even if the run succeeded. In the web UI's run
history a retro is a standalone row, as a discuss run is.

As with verify, the server refuses a retro by an actor of the same family
as one of the flow's implement or plan runs unless `--allow-same-family`
is given, and records the override on the run. Retros start only from
the main checkout: `run start` in an actor worktree does not take them,
since the actor's credential cannot gather the evidence.

### Sharing the host

Runs share the host with each other and with the owner. The implement and
verify prompts say so: a run must not stop, kill or remove processes,
containers or worktrees it did not start, nor run `bottown up` or `down`;
when shared state blocks it, it waits or comments and stops. `bottown up`
and `down` also refuse inside a run (with `$BOTTOWN_RUN_FILE` set, in a run
worktree, or in an actor worktree whose run is live), except on the
throwaway `e2e-*` instances that `make e2e` creates and purges.

## Harness models

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

## The fam guide

Agents learn how to work in a fam from the **fam guide**, separate from the
project's own instructions. A project's AGENTS.md (or README) says how to
build and check that project; the guide says what every fam shares: the
layout with its real paths and actors, the `bottown` remote and protected
default branch, how runs start, the agent CLI, and what is allowed outside a
run. bottown's own AGENTS.md keeps only what is particular to bottown.

- **One source, in the binary.** The guide is a template,
  `internal/runs/prompts/fam.md`, rendered for the checkout it runs in.
  `bottown guide` prints it, in the owner's checkout, an actor worktree or a
  run worktree. It is versioned with the CLI, so a fam never reads a guide
  older than its binary.
- **Untracked, per actor worktree.** `actor setup` (and so `clone`) writes
  it into every actor worktree, in the file the worktree's harness loads by
  itself next to AGENTS.md, and `run start` refreshes it in its worktree:

  | Harness | File |
  |---|---|
  | claude | `CLAUDE.local.md` |
  | crush | `CRUSH.local.md` (crush also loads `CLAUDE.local.md`; it gets one) |
  | antigravity | `.agents/rules/bottown.md` |
  | kimi | `.kimi-code/AGENTS.md` |
  | codex | none: `AGENTS.override.md` would replace AGENTS.md |

  The files are listed in the checkout's shared `info/exclude`, so no
  worktree shows or commits them, and carry a marker line: bottown refreshes
  only files with the marker, never one the repository tracks, and removes
  its file from another slot when an actor's harness changes.
- **On demand, globally.** `harness setup` installs a `bottown` skill for
  claude, kimi, codex and crush: its description matches a checkout with a
  bottown remote, and it has the agent run `bottown guide`. It loads only
  when it applies, so it costs nothing in other repositories, and it is how
  codex finds the guide.
- **Committed only on request.** `clone --agents-md` appends a short pointer
  to `bottown guide` to the project's AGENTS.md (creating it if missing)
  and commits it before the first push. It is a pointer, not the guide, so
  it cannot drift from the CLI. Without the flag the project's history is
  left alone, since a commit would reach upstream pull requests.
- **Not an MCP resource.** Runs already get the rules in their goal prompt,
  and outside a run there is no MCP connection: it is per run and keyed.
- **Checked.** The web UI's actor worktree table shows each worktree's
  guide: current, outdated, missing, foreign (a file bottown did not write
  is in the slot) or none (codex).

## The wiki store

A repository's Forgejo wiki is an idea workspace (#311, #315), upstream of
issues. **It is never binding:** nothing reads it as state, and once an idea
becomes an issue or a design document, that is the source of truth. The
wiki is reached only through bottown's wiki store
(`internal/server/wiki.go`):

```
bottown wiki list
bottown wiki read PATH              # content to stdout; its blob SHA to stderr
bottown wiki put PATH [--base SHA] [--file F|-] [-m MSG]
bottown wiki rm PATH --base SHA [-m MSG]
bottown wiki generate [--check]
```

PATH is a file in the wiki (`Idea.md`, `reviews/12-x/r1-claude.md`) or a
page's slug (`Idea`). A slug also finds a page Forgejo stored as `Idea.-.md`,
which is how its web UI and API store a hyphenated title. In a run or an
actor worktree the commands act as the agent; anywhere else on the host,
as the owner (`--repo` outside a checkout).

- **No checkout for agents.** The API proxy denies Forgejo's wiki API
  (`/repos/*/*/wiki/**`), and the git proxy the wiki's repository
  (`*.wiki.git`), to everyone but the owner outside a run (rule
  `wiki-via-store`). The store reads Forgejo's own bare repository of the
  wiki, so it is never behind. It writes by committing there and pushing to
  Forgejo as the writer, so Forgejo's page history shows an ordinary
  commit, carrying a `Bottown-Run:` trailer when made in a run. A wiki that
  does not exist yet is created through Forgejo's API on its first write
  (a push to it would be refused). The creation's placeholder `Home` page
  does not survive that write, unless the owner edited it in between; pages
  the owner added in between stay too.
- **Puts are compare-and-swap.** `read` prints the file's blob SHA, and
  `put --base SHA` commits only if the file's text is unchanged since.
  Otherwise it fails with a conflict (`wiki_conflict`): read again and
  re-apply the edit. Changes to generated blocks alone are no conflict.
  Without `--base` a put creates the file, and fails if it exists. A push
  that loses a race, for example with the owner saving in the web UI, is
  rebuilt on the new head and retried, so it never overwrites that edit.
- **Deletes are too.** `rm PATH --base SHA` removes the file under the same
  compare-and-swap (a missing file is a 404), and regenerates the whole
  wiki in the same commit: backlinks drop the deleted page, and links to
  it show on the health page as broken. It is a write under the wiki
  rules, so by default the owner's alone. `Wiki-Health.md` cannot be
  deleted, since the generator recreates it. The audit log records
  `wiki_rm` entries, as it does `wiki_put`.
- **Path-scoped policy.** `policy.WikiRules` grants reads and writes by
  principal (the owner, an actor outside a run, or a run's role) and path,
  first match wins. The default: the owner reads and writes everything;
  actors and every run role read. #311 (garden runs, top-level pages) and
  #315 (round files) add their rules ahead of these.
- **Generated blocks.** Text between `<!-- bottown:<name>:generated -->`
  and `<!-- /bottown:<name>:generated -->` is bottown's. Blocks do not nest,
  and names come from the registry in `internal/wiki` (`backlinks`,
  `health`). A put discards whatever it is sent inside markers and fills
  the blocks again, so writers never maintain them. It refuses mangled
  markers and unknown names.
  - `backlinks`, at the end of every page except the health page and
    Forgejo's `_Sidebar`/`_Footer`, lists the pages linking to it.
  - `health`, on `Wiki-Health.md` (created when missing), lists broken
    links, pages stored as `Name.-.md`, unknown block names and pages
    whose markers do not pair. The generator leaves those pages alone.
  - Links are read from page text outside blocks, in the forms Forgejo
    resolves to a page (checked on Forgejo 15.0.11): `[t](Page)`,
    `[t](./Page)`, `[t](Page.md)`, `[t](/org/repo/wiki/Page)`, `[[Page]]`,
    `[[Page Name]]` (spaces become dashes) and `[[Text|Page]]`. A link to
    `Name` does not reach a page stored as `Name.-.md`: health reports it
    as broken and names the link that works. Files in subdirectories are
    not pages: Forgejo serves them only under `/wiki/raw/`. Their links
    are not read and they get no default blocks, but a block placed in one
    is filled and kept current like a page's, and their markers are
    checked.
- **`generate`.** It walks the whole wiki and regenerates every stale
  block, committing only the files that change, as bottown. Every put does
  the same in its own commit. The owner runs it after editing in the web UI,
  which leaves blocks stale. `--check` writes nothing, lists the stale
  blocks and fails when there are any; anyone who can read the wiki may run
  it. The generator is a pure function of the tree (no clock, no forge), and
  running it on its own output changes nothing.
