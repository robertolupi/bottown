# Landing an approved pull request

`internal/land.Land(ctx, api, checker, policy, pr)` is the shared host-side
landing primitive for the [`bottown merge-all`](merge-all.md), milestone run
and milestone merge callers; this page describes the primitive itself. `api`
is an owner-authenticated `internal/client.Client`; the checker is the
flow's grouped checker.

`Policy` names the repository and shared runs directory, the check timeout
(default 30 minutes), and a `Kind`: `Main` runs `check` then `e2e`;
`Milestone` runs `check` only. Commands come from `.bottown.toml` on the
default branch, as described in [Checks](checks.md). The caller selects the
policy appropriate to the target branch.

Land proceeds in this order:

1. Take `runs.HoldFlow` for an `issue/<N>` head. Refuse an occupied lock.
   Re-read the PR: it must remain open, approved and synced, on its original
   base and head branch. Refuse a live run on its PR, branch or issue.
2. Trial-merge the base and report conflicting files without pushing.
   Request [the landing commit](landing.md) at the head just read.
3. If the returned assessment is not approved, stop for verification.
4. Run the policy's groups at that exact landing commit in the checker's
   clean detached worktree. Record each group's result as the owner's
   `bottown/check` or `bottown/e2e` gate status. Failed checks stop landing.
5. Request [the fast-forward-only merge](runs/milestone-child-merge.md)
   at that exact checked commit. A `base_moved` refusal rebuilds and
   rechecks the landing, with at most three retries after the first
   attempt. The PR is revalidated before each retry. No check is reused.

The flow lock stays held through checks and merge and is released on every
return. Final milestone PRs have no issue head and take no issue lock.
The server also guards against a live run or changed head at the landing
operation; merge pins the checked head and atomically refuses a moved base.

Outcomes are concrete types implementing `Outcome`, each with a one-line
`String()` containing the PR, head and reason for printing or commenting:

| Outcome | Meaning and additional fields |
|---|---|
| `Landed` | Merged successfully; `MergeCommit` identifies what landed. |
| `Conflict` | Trial merge failed; `Files` lists conflicts. Nothing pushed. |
| `NeedsVerify` | The landing commit's approval did not carry. |
| `ChecksFailed` | Failed at the landing commit; `Log` and `Description` identify the failure. |
| `BaseMoved` | The base kept moving through all three retries. |
| `Refused` | Invalid candidate, live run, busy lock or operational error; `Err` retains the cause. |

An optional `Policy.Repair(ctx, outcome)` hook runs only after `Conflict`
or `NeedsVerify`. Land releases the lock before calling it. If it returns
true, Land takes the lock again and revalidates from step 1; false returns
the original outcome. Callers bound their own repairs. Failed checks and
other refusals never invoke repair. Land itself does not comment or launch
agent runs; callers own those decisions.

An optional `Policy.Progress(step)` hook hears each step as it starts:
building the landing commit (again, after a moved base), running the check
groups at it, and merging once they pass. `merge-all` prints them; Land
prints nothing itself.

The web inbox shows every approved candidate as **Ready to land**, even
when behind its base, with `bottown merge-all N` to copy and run in the
terminal. Its landing checks show on Now as check items. The UI has no
landing worker, landing records or write action. Merging in Forgejo
instead skips these checks at the combined tree, as the inbox item explains.
The old PR-first merge action and `POST /v1/forge/pr/merge-base` are
removed; the GET status read remains.
