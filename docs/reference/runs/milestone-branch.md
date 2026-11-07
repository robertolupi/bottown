# Milestone branch and finish

Two owner commands bracket a milestone's integration branch; the owner
runs them by hand, and `bottown milestone run` ([milestone run](milestone-run.md)) calls them. Each
calls an owner-only server operation
(`POST /v1/forge/milestone/{branch,merge-base,pr}`) that refuses actors and
run keys and writes to the forge as the `manager` account.

`bottown milestone branch <slug> [--repo R]` creates `milestone/<slug>`
from the default branch's head. The plan needs an Implementation Epic filed
by `milestone apply`: an issue the owner filed whose marker is the plan's
`role="epic"`. If the branch already exists, the command reports it and
succeeds.

`bottown milestone finish <slug> [--repo R] [--check-timeout 1h]` ends a
milestone. Its scope is the epic's **active children**: issues the owner
filed that carry a child marker for the plan and are in the epic's current
`blocked_by`, open or closed. When a key leaves the plan, `apply` drops
only its issue's edge from the epic, so that issue is no longer active,
even while it is open. `milestone.Epic` in `internal/milestone` is the one
definition. In order, finish:

1. refuses while an active child is open, naming each one (there is no
   `--force`: the owner merges by hand for a partial branch);
2. merges the default branch into `milestone/<slug>` as `manager`, the only
   time the default branch is merged in. If the branch already contains it,
   nothing is merged. On a conflict it stops, names the files and leaves
   the branch as it was, for the owner to resolve;
3. runs the e2e gate at the branch's new head exactly as `bottown flow`
   does at a PR head (`flowChecker`): the e2e commands `.bottown.toml`
   names on the default branch, run in a clean detached worktree, logged
   to `.bottown/runs/flow-<epic>-<sha>-<check>.e2e.log`, within
   `--check-timeout` (default one hour, the flow's);
4. if the checks pass, or the repository names none, opens the final pull
   request from `milestone/<slug>` into the default branch as `manager`,
   titled with the epic's title, or updates the description of the one
   already open. The description lists each active child with its pull
   requests merged into the milestone branch, and ends `Closes #<epic>`,
   so the owner's merge closes the epic. The server refuses if the branch
   has moved since the checked head. Updating an open PR also comments on
   it that the head is gated, answering the comment `milestone run` leaves
   there when a merge moves the head past the gated commit;
5. if the checks fail, comments on the epic with the failure summary and
   the log's path, opens no pull request, and exits non-zero.
