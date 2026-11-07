# Reusing an issue branch

Implement and plan run pushes are confined by the git proxy to
`refs/heads/issue/<n>` for the run's issue. Every ref update is checked,
including deletions: pushes to other issue branches, milestone branches,
the default branch, and tags are refused. A push containing any other ref
is rejected in full before its pack is forwarded. Owner keys and non-run
principals are unchanged.

A retry on the same issue can reuse `issue/<n>` even when an earlier run
kept its worktree. Before creating the server run, the launcher detaches
that old worktree, preserving its commits and working files for inspection.
If its launcher is still alive, the start refuses with the holding run's
ID instead.
