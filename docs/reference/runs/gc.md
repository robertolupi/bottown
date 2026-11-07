# What run gc removes

What `run gc` removes, besides the server's records of ended runs: each
finished run's worktree and its `<id>.*` files (succeeded and dead runs;
failed ones with `--failed`); `bottown flow`'s e2e logs,
`.bottown/runs/flow-<issue>-<sha>-<check>.e2e.log`, once the issue has no open PR
from `issue/<n>`, or with `--older-than` once the log is that old (while the
PR is open a resumed flow points to the log rather than checking again);
and the web UI's flow logs, `.bottown/flow-<issue>.log`, once that flow is
not running, that is, no `flow-<issue>.json` names a live process. Its
summary line counts the runs and the flow logs it removed. A run's
`<id>.check.json` is its own file, removed with it, and listing runs does
not warn about it.
