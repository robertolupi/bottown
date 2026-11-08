# Design: the merge queue: landing approved pull requests

Status: **design**, settled with the owner in issue #397 after two debate
rounds (the wiki page `Review-397-design-a-merge-queue-for` is their
record; it binds nothing, this document does). Nothing here is built. The plan that builds it is
[`docs/milestones/merge-queue.md`](../milestones/merge-queue.md); where
this design and [the reference](../reference/index.md) differ once work
lands, the reference says what is built. Extends
[the local web UI and supervisor design](web-ui.md) (§3.2, §4.1) and
changes one rule of [the milestones design](milestones.md) (§1.1: see §7
here).

## 1. The problem, and what the evidence says

Concurrent flows keep open pull requests behind `main`. In the week the
windowed retro of 2026-11-07 looked at, bottown wrote 30
`Merge main into issue/N` commits on open PRs, ran 17 verify runs at
such heads, and ran its two-minute e2e gate at each of them. Four PRs
were verified again after they had been approved: !152, !358, !379 and
!380.

Before designing, this run checked those four. **All four re-verifies
were due under the rules we have.** !152 was a rebase on 2026-11-02,
before !211 taught the review assessment that an approval carries over a
rebase or clean merge of the same patch (`review.SamePatch`). !358, !379
and !380 each resolved a real conflict in their merge-main commit
(`git show --remerge-diff` shows resolution edits in `internal/cli/run.go`,
`internal/server/milestone.go` and twice in `docs/reference.md`), so the
patch changed and a new verify was right. None was a clean merge that lost
its carry on context lines.

So a queue does not save verify runs by itself. The docs split (#398,
merged) removes most of the conflicts that caused them. What the queue
does buy is this:

- **Safety.** An approved PR that is behind `main` lands today when the
  owner clicks merge in Forgejo. Nothing runs the checks on the combined
  tree first, and `main` ships to prod with `make install`. The branch
  protection of `main` has no up-to-date rule, and `review.SamePatch`
  proves review reuse, not that the patch still works on the new `main`.
- **Fewer moving heads.** The 30 merge commits came from `bottown flow`
  merging a clean base into a PR before verification (the attended
  prompt, and `--merge-milestone-base` for milestone children). Each one
  moved a head that a verifier, the e2e gate, or both then had to visit
  again.
- **One command.** Approved PRs land one at a time, in an order that
  keeps conflicts late, with `main` brought in, the checks run at the
  exact commit that lands, and no new verify when the patch is
  unchanged.

## 2. Goal and non-goals

**Goal.** A landing step that every merge bottown makes goes through,
for `main` and for `milestone/*` alike, and an owner command that lands
every approved PR into `main`:

```
bottown merge-all [PR...] [--actor A] [--verify V] [--dry-run] [--repo R]
```

With no PRs named it lands every approved PR into `main` that is not
part of a milestone. The owner invokes it; that is the authorization. bottown merges as the
`manager` account, as `bottown milestone merge` does today. The flow no
longer moves a PR's head after the implementer's last push, other than to
resolve a real conflict.

**Non-goals.** No daemon, no persistent queue, no state outside the forge
(§3.3 of [web-ui.md](web-ui.md)): `merge-all` is a synchronous command
that rebuilds its view from Forgejo and resumes when run again. No
batching of several PRs into one check (bors-style trains): one PR at a
time, measured first. No second scheduler: `milestone run` keeps its
dependency graph and actor pool. No prediction of conflicts beyond a
file-overlap count. No semantic analysis of what `main` changed: the
checks are the integration guard. The queue of *issues to run* that
[web-ui.md §3.6](web-ui.md#36-later-the-queue) sketches is a different
thing and is untouched.

## 3. Terms

- **Candidate:** an open PR whose review assessment (`internal/review`,
  the one the flow, `pr show` and the inbox read) is `approved` at its
  head, carried approvals included, into `main` or into a `milestone/*`
  branch.
- **Landing commit:** a merge commit on the PR branch whose **first
  parent is the base's tip** and whose second parent is the PR's head,
  with the title `Merge !N (<PR title>) into <base>`. It is what the
  checks run at and what lands. A PR that already contains the base's tip
  gets one too, so `main`'s first-parent history is one merge commit per
  PR, as it is today.
- **Carry:** `review.SamePatch` holding between the head the verifier
  approved and the landing commit: the diff from each one's merge base
  with the base branch has the same `git patch-id --stable`. A clean
  merge of the base keeps the carry unless `main` edited lines next to
  the PR's; a conflict resolution never does.
- **Policy:** what a base requires at the landing commit before it
  lands. `main`: the `check` and `e2e` groups. `milestone/*`: `check`
  only (§6).

## 4. The landing primitive

`internal/land` holds one function the three callers share (`merge-all`,
`milestone run`, `milestone merge`), `land.Land`, host-side, over owner
server operations. For one candidate, in order:

1. **Lock and revalidate.** Hold the issue's flow lock
   (`runs.HoldFlow`) when the PR is from `issue/<N>`, so no flow drives
   the issue meanwhile; refuse a PR with a live run (`liveRunOnPR`).
   Re-read the PR: still open, still a candidate, base unchanged.
2. **Trial-merge the base** into the head without a worktree
   (`gitmerge.MergeTree` on the forge's git directory, as
   `GET /v1/forge/pr/merge-base` does). A conflict is **Conflict**
   (§4.4); nothing is pushed.
3. **Push the landing commit** (`POST /v1/forge/pr/landing`, §9): the
   server builds the merge commit with `commit-tree` (base first), pushes
   it to the PR branch as `bottown` at the expected head, waits for
   Forgejo to finish processing the push (`waitPushSynced`), reconciles
   carries (`opReconcileCarries`: undismisses an approval Forgejo
   dismissed automatically when the patch carried), and returns the new
   head, the base SHA it contains, and the assessment at the new head.
4. **Check the carry.** If the assessment is no longer `approved`, the
   result is **NeedsVerify** (§4.4). Nothing lands.
5. **Run the checks** the policy requires at the landing commit, through
   the flow's checker extended to run both groups (`check` and `e2e`
   from `.bottown.toml` on the default branch, by default `make check`
   and `make e2e`), in a clean detached worktree, each recorded on the
   commit as the owner's `bottown/check` and `bottown/e2e` status with
   the `gate` mark, logged as the flow's gate is. A failure is
   **ChecksFailed**; the PR is left unmerged at the landing commit.
6. **Land** (`POST /v1/forge/pr/merge`, generalized, §9): as `manager`,
   merge the PR **fast-forward-only** at `HeadCommitId` = the landing
   commit. The landing commit contains the base's tip, so the base moves
   to it; if the base moved since step 3, Forgejo refuses, atomically,
   and the result is **BaseMoved**: Land goes back to step 2, a bounded
   number of times (three), then reports. On a `milestone/*` base the
   server then closes the child issue and comments on it, as today; on
   `main`, Forgejo applies the PR's `Closes #N`.

Each outcome names the PR, the head, the commit statuses or log, and the
reason, in one line the callers print and comment. Each refusal and each
merge is audited (`land`, replacing `milestone_merge`).

### 4.1 Why fast-forward-only

The invariant is: **the tree that lands is the tree that was checked, with
no base commit folded in unseen.** A Forgejo merge commit built at merge
time breaks it whenever the base moved during the checks, and a pre-check
on the server leaves a race that a bottown lock does not cover, since the
owner can merge in Forgejo at any time. Fast-forward-only with
`HeadCommitId` is Forgejo's compare-and-swap: it succeeds only if the base
is an ancestor of the exact head we checked. Forgejo 15.0.11 (prod's
version) offers the style, the SDK names it (`MergeStyleFastForwardOnly`),
and `bottown/bottown` already allows it (`allow_fast_forward_only_merge`).
Building the landing commit base-first keeps `main`'s history as it is:
`git log --first-parent main` walks merge commits, one per PR.

The first child of the plan is a spike that proves on a real instance what
this design assumes of the style: the PR is marked merged with the landing
commit as its merge commit, `Closes #N` closes the issue, the protection
of `main` counts an approval that `ReconcileCarries` undismissed, and a
base that moved makes the merge fail with 409.

**There is no fallback that this design authorizes.** A server-side check
that the base's tip is an ancestor of the head, followed by an ordinary
merge-commit merge, is not one: the owner can merge another PR in Forgejo
between the check and the merge, `HeadCommitId` pins only the PR's head,
and the merge commit then folds in a base commit nobody checked. That is
the race the first paragraph rules out, and a retry cannot see it,
because the unsafe merge succeeds. If the spike fails on any point, the
plan stops before `land-merge-op` and the issues that depend on it, and
this section is redesigned with the owner. A replacement must be an
atomic compare-and-swap on the base; the candidate to try first is a
fast-forward `git push` of the landing commit to the base as `manager`
with the expected old tip (`--force-with-lease=<base>:<tip>`, which git
refuses atomically if the ref moved), with the repository's
`autodetect_manual_merge` marking the PR merged. Whatever replaces the
style passes the spike's assertions again, the moved-base race included,
before it is built on.

### 4.2 Checks at the landing commit

Both groups run at every landing on `main`, at about three minutes a PR
here. The implementer's and verifier's `bottown/check` statuses at the
PR's head do not count: they are at another commit, and a status says
what an actor reports it ran. No result is reused in this version; the
first option, when the cost shows, is to reuse the head's passing gate
when the landing commit's tree equals the head's tree (`rev-parse
<sha>^{tree}`), which is exact. A `.bottown.toml` change on `main` changes
the commands for every later landing and voids nothing else.

### 4.3 When a change in `main` invalidates an approval

Never by itself. The approval covers the patch; the checks cover how it
interacts with `main`. An approval stops counting when:

- the patch changes: a push by the implementer, a conflict resolution,
  or a clean merge whose context lines `main` edited (`SamePatch` is as
  strict as `git range-diff`, and stays so);
- its reviewer requests changes later, or the owner dismisses it;
- the PR is retargeted;
- the head moved outside the landing step while it was running: Land
  stops that attempt, re-reads, and applies the same rule.

There is no overlapping-file rule and no "always re-review" path list.
A same-family approval still does not count, and the owner's approval
counts as it does elsewhere.

### 4.4 The exceptions

- **NeedsVerify:** the landing commit lost the carry (a clean merge whose
  context `main` edited). The patch's review is stale, not its
  implementation: the remedy is a verify run at the landing commit, not an
  implement round. `merge-all` starts one through the flow with the PR's
  verifier when it has one (§5.3); otherwise it reports.
- **Conflict:** the base cannot be merged cleanly. `merge-all` reports
  it and goes on with the other candidates; with `--actor` it runs the
  existing flow on the issue after every clean candidate landed, so the
  conflict is resolved once against the final `main`. The flow's conflict
  rounds (`conflicts with base`, bounded by `--rounds`) and the verify
  that follows them are the repair; the queue adds nothing of its own.
  The PR is still approved when the flow starts on it, so this works only
  because the flow decides the conflict round before its approved stop
  (§7).
- **ChecksFailed:** reported, never repaired. The PR stays open at its
  landing commit with the failed status on it; the owner, or a new
  implement run, decides. Turning a failed check into leave to change
  arbitrary code is not the queue's call.
- **BaseMoved:** retried from the trial merge, three times, then
  reported.
- **Owner-approved patches** (the only approval is the owner's) land like
  any other; a repair they need goes back to the owner, since no actor
  pair is on record for them.

## 5. `bottown merge-all`

Owner only; refuses an actor's or run's key. Synchronous: it prints a
line per landing as it goes and exits when the batch is done.

### 5.0 First, the local `main`

Before it reads a candidate, `merge-all` fetches the default branch from
the forge and fast-forwards the local `main` of the owner's checkout to it
(`git fetch bottown main:main`, in the checkout `mainCheckout` finds, as
`milestone run` does), because that `main` is often behind the forge's
after a day of merges. A local `main` that has diverged, or a checkout
with `main` checked out and uncommitted changes, stops the command before
anything lands. When the batch is done it fast-forwards it again, so the
checkout holds what landed; it does not run `.bottown.toml`'s
`after_pull` (`make install` here): upgrading prod stays the owner's
command.

### 5.1 The batch

With no arguments, the candidates into the default branch at the moment
the command starts **that are not part of a milestone**: open, assessed
`approved` at the head, not a draft, with no live run and no held flow
lock, and neither from a milestone child (an issue with the owner's
`bottown:milestone` child marker, whose work `milestone run` drives even
when its PR targets `main`) nor from a `milestone/*` branch (a final
milestone PR). Those two land only when named: PR numbers as arguments
restrict the batch to those numbers, milestone PRs included; one that is
not a candidate is reported and skipped. PRs approved after the start
wait for the next invocation. Each candidate is revalidated before it is
touched (§4 step 1).

### 5.2 The order

Recomputed before each landing, from a trial merge of the current base
into each remaining candidate and the files each candidate's own delta
touches (renames count both paths):

1. candidates whose trial merge is clean, before those that conflict;
2. among them, the fewest **other remaining candidates** that share a
   touched file, ascending, so a broad PR lands after the narrow ones it
   overlaps: landing it first could cost each narrow one a repair,
   landing it last costs it at most one;
3. then the lowest PR number.

A trial merge is `git merge-tree` on the forge's repository, cheap enough
to repeat. The order is a hint, never a judgement: it does not touch
approvals or checks, and it does not promise to avoid a conflict.

### 5.3 Repair

Without `--actor`, `merge-all` repairs nothing: a conflict or a lost carry
is reported and left. With `--actor A`, after the clean candidates
landed, it runs `bottown flow`'s supervisor in-process on each remaining
`issue/<N>` candidate with implementer `A` and verifier `--verify V`, or
the candidate's recorded pair when the flags are absent: the issue's
`implement/<a>` and `verify/<b>` labels (milestone children), else the
PR's author and the actor whose approval the assessment counted. The
flow enforces the cross-family rule as always, and refuses a same-family
pair. The lock Land held is released before the flow starts and taken
again after it ends; Land then revalidates from step 1 and lands the
repaired head if it is a candidate again. A final milestone PR is never
repaired here: it is not from `issue/<N>`, and `bottown milestone finish`
and the owner own its conflicts.

### 5.4 Output, rerun and exit

Per PR, one line: landed at which commit, or the outcome and its reason.
At the end, a table of the batch. `--dry-run` prints the order and each
candidate's trial-merge result and overlap count, and touches nothing.
Running `merge-all` again resumes: merged PRs are out of the batch, a PR
left at its landing commit is a candidate again if it is still approved.
Exit 0 when every candidate landed, 1 when any remains unmerged or on an
error.

## 6. Milestones

`milestone run` and `milestone merge` keep their roles and call
`land.Land` with the milestone policy instead of `POST /v1/forge/pr/merge`
directly.

- **Policy:** `check` only at the landing commit (about a minute here).
  It catches compile and unit breakage between siblings when a child
  lands behind a sibling's merge, and says which child did it; `e2e`
  stays with `milestone finish`, which merges `main` in and gates the
  whole branch once before the final PR. The landing runs synchronously
  in the scheduler's result loop, so dispatch pauses for that minute;
  a landing worker per target is the option if that shows.
- **A child that lands as ready at once.** The scheduler does not collect
  ready children to order them: landing one closes its issue and may
  unblock others, and a batch of one has one order. When several are
  ready together (a resume), the same comparator as §5.2 is free, and
  used.
- **A conflicting child is re-dispatched, not stopped.** Today a child
  whose merge fails after a sibling moved the branch stops as `merge
  failed` for the rest of the invocation. With Land, a **Conflict** or
  **NeedsVerify** outcome sends the child back through its own flow (its
  recorded pair, `--rounds` bounding the conflict rounds), once per
  invocation; a second Conflict stops it as today. The child's flow
  starts a conflict round on it although it is approved (§7), and the
  verify that follows. **ChecksFailed** stops
  it, with the log, for the owner.
- **The final PR** from `milestone/<slug>` into `main` is a `merge-all`
  candidate once the owner approves it and names it (§5.1). Land brings
  `main` in and runs both groups at the landing commit, which repeats finish's e2e when
  `main` moved since finish; when it did not, the landing commit's tree is
  finish's tree, and §4.2's reuse option would skip it. Its `Closes
  #<epic>` closes the epic on `main`.

## 7. The flow

The flow stops pushing clean merges of the base into a PR. The attended
prompt ("merge main into the PR?") and `--merge-milestone-base` go, and
with them `Options.MergeMilestoneBase`, `autoMerge` and `confirmMerge`.
What stays:

- a PR behind its base **with a conflict** still gets an implement round
  (`conflicts with base`, bounded by `--rounds`), **approved or not**,
  because a verifier should not review an unmergeable PR, an approved PR
  that conflicts cannot land, and the fix needs a verify;
- a PR behind its base **cleanly** is gated and verified at its own head.
  Approved, it stops `ready to merge`; the detail says it is behind and
  that `bottown merge-all` lands it.

The loop keeps the order it has (`internal/flow/flow.go`): the trial merge
of the base comes first, the conflict round is decided on it, and only a
PR that merges cleanly reaches the approved stop. Only the clean-merge
branch between them goes. That order is what makes the flow the repair
for Land's **Conflict** (§4.4, §5.3, §6): Land pushes nothing on a
conflict, so the flow it hands the PR to finds the same approved head and
the same conflict, starts a conflict round, and the patch the
implementer changes then loses its carry, so the verify follows. An
approved stop ahead of the conflict check would send that PR straight
back as `ready to merge`, and Land would see the same conflict again.
`TestFlowMergeBaseCleanApproved`, which pins the clean merge of an
approved PR, is replaced by a test that it stops `ready to merge` with no
new head; a new test starts from an approved PR that conflicts after a
sibling landed and asserts the conflict round and then the verify run.

This also deletes the milestones design's rule that `bottown` never merges
into `main` ([milestones.md §1.1](milestones.md#11-why-this-narrows-two-non-goals-without-reviving-v1)):
it does, as `manager`, when the owner runs `merge-all`, and only through
Land. The owner can still merge in Forgejo; the inbox says what that skips
(§8).

## 8. The web UI

The inbox's **Ready to merge** item today hides a PR that is behind and
offers "merge main into the PR" (`POST /v1/forge/pr/merge-base`, the
PR-first merge the flow also made). It becomes: every candidate shows as
**Ready to land**, with a **Land** action that runs `land.Land` for that
PR in the background like a flow, with its log under Live Runs, and a
note that merging in Forgejo instead skips the checks at the combined
tree. The PR-first merge operation and its action go once nothing calls
them. The Milestones tab shows a child's landing like its flow.

## 9. Server operations and policy

| Operation | Who | What |
|---|---|---|
| `GET /v1/forge/pr/merge-base` | owner | unchanged: trial merge of the base into the head; now also lists the files the PR's delta touches, for the order |
| `POST /v1/forge/pr/landing` `{repo, number, head_sha}` | owner | builds and pushes the landing commit at `head_sha` (409 if the head moved, or a live run holds the PR); waits for Forgejo; reconciles carries; returns `{head_sha, base_sha, assessment}` |
| `POST /v1/forge/pr/merge` `{repo, number, head_sha}` | owner | generalized from the milestone merge: base `main` or `milestone/*`; head exactly `head_sha`; assessment `approved`; merges fast-forward-only as `manager`; closes and comments on the child issue only for a `milestone/*` base and an `issue/<N>` head; `already_merged` on repeat; audited as `land` |
| `POST /v1/forge/status` | runs, owner | unchanged; the checker records `bottown/check` and `bottown/e2e` at the landing commit as the owner's gate |

Run keys and actor keys are refused by all three (403), as today. The
`manager` account is already on the merge whitelist of `main` and of
`milestone/*` (`internal/config/default.toml`), so no protection changes.
The repository setting `allow_fast_forward_only_merge` is provisioned on,
so a repository the owner created by hand gets it too.

## 10. Decisions recorded

Settled by the owner in this issue's discussion, 2026-11-08:

1. **Scope:** the full command, with the overlap order and `--actor`
   repair, over a flow-only fix, because the safety gap on `main` is real
   even though the re-verifies were due.
2. **Base guard:** fast-forward-only landing of a base-first landing
   commit, spiked first. An ancestor pre-check before a merge-commit
   merge is not a fallback (§4.1): a failed spike stops the plan for a
   redesign that keeps the base guard atomic.
3. **Milestone policy:** `check` at the combined head, `e2e` at finish.
4. **Flow:** no clean base merges at all; conflict rounds stay, and come
   before the approved stop (§7).
5. **Failed checks** are reported, not repaired. **A lost carry** costs a
   verify, not an implement round. **Who repairs:** the recorded pair,
   flags override, cross-family enforced; owner-approved patches go back
   to the owner. **Who merges:** `manager`; invoking is the authorization.
6. **A change in `main` alone never voids an approval** (§4.3).
7. **The default batch** leaves milestone work out: a milestone's PRs
   land through `milestone run` and, for the final PR, by name. **The
   local `main`** is fast-forwarded from the forge first and last (§5.0).

## 11. Risks

- **Fast-forward semantics** (§4.1): the spike is first in the plan for
  this reason; if it fails, the plan stops for a redesign rather than
  falling back to a guard with a race.
- **Forgejo's asynchrony:** a push returns before the PR ref, its
  mergeability and the stale-review dismissal settle. Land waits
  (`waitPushSynced`) and re-reads before trusting any assessment, and
  reconciles carries before the merge so the protection counts the
  approval.
- **Serial time:** N landings cost N×(check + e2e) on `main`. It is no
  worse than the verify runs those heads cost today, and §4.2 names the
  first optimization.
- **Two drivers of one branch:** Land holds the flow lock, refuses live
  runs, and the ff-only merge refuses a moved base; a flow the owner
  starts by hand on a candidate makes `merge-all` skip it, with the
  reason.
- **The order is a hint.** File overlap does not see every conflict and
  cannot remove one. The docs split is what cut the conflicts; keeping
  hotspots split stays a docs rule.

## 12. Out of scope

Batching PRs into one check; a persistent queue or daemon; merging on an
approval without the owner's command; conflict prediction beyond file
overlap; a re-review policy by path; repairing failed checks; landing a
final milestone PR that conflicts; cross-repository landings.
