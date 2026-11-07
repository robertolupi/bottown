# The fam guide

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
