# What run gc removes

What `run gc` removes, besides the server's records of ended runs: each
finished run's worktree and its `<id>.*` files (succeeded and dead runs;
failed ones with `--failed`); `bottown flow`'s check logs,
`.bottown/runs/flow-<issue>-<sha>-<check>.e2e.log` (and `.check.log`, for
a check that ran the check group), once the issue has no open PR
from `issue/<n>`, or with `--older-than` once the log is that old (while the
PR is open a resumed flow points to the log rather than checking again);
and milestone child flow logs, `.bottown/flow-<issue>.log`, once that flow is
not running, as determined by the held issue lock
(`runs.FlowHolder`), rather than a UI process record. Its
summary line counts the runs and the flow logs it removed. A run's
`<id>.check.json` is its own file, removed with it, and listing runs does
not warn about it.
