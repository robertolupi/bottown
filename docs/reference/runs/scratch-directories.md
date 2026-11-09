# Scratch directories

Launched runs get a private scratch directory under the launcher's temporary
directory, named `bottown-<workspace hash>-<run id>`. The harness receives it
as `TMPDIR` (and Claude receives it through `--add-dir`), so tests that need
temporary directories outside a git checkout can run normally. Discuss,
consolidate and estimate runs are read-only: Claude gets no `Edit` or `Write` in the
worktree, only in the scratch directory (`Write(//<dir>/**)` and
`Edit(//<dir>/**)` in `--allowedTools`, for the path and its resolved form). Their prompts name the directory, and a
debate's runs write a long answer or consolidation to a file there and pass
it to `wiki put --file` (#392). Batch prompts also say that nobody can
approve a permission request, so a blocked run publishes what it can and
says what is missing. The launcher
removes it when the run returns; `run gc` uses the path saved in the local
record if the launcher died. Older runs' scratch directories beside their
worktrees are also removed by gc.
