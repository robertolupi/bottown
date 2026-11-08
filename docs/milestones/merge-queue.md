+++
title = "Merge queue: land approved PRs through one checked step"
kickoff = 397
design = "docs/design/merge-queue.md"
+++

Approved pull requests into `main` land today when the owner clicks merge
in Forgejo, with nothing run at the combined tree, and `bottown flow`
moves PR heads with clean merges of the base that cost e2e runs and
verifies. [The merge queue design](../design/merge-queue.md) replaces both
with one landing step, `land.Land`: a base-first landing commit on the PR
branch, the checks at that commit, and a fast-forward-only merge as
`manager` that refuses a base that moved. `bottown merge-all` lands every
approved PR that is not part of a milestone, in an order that keeps
conflicts late; `milestone run` and `milestone merge` land children
through the same step with `make check` at the combined head; the flow
stops merging bases.

Owner decisions, from #397: the full command with the overlap order and
`--actor` repair; fast-forward-only landing, spiked first, and no
fallback with a race (a failed spike stops the plan for a redesign);
`check` only at a milestone child's
landing, `e2e` at finish; no clean base merges in the flow; failed checks
reported, not repaired; a lost carry costs a verify, not an implement
round; the default batch leaves milestone PRs out; the local `main` is
fast-forwarded from the forge first.

The first five issues are independent and can run in parallel. The spike
goes first in the order because its result decides the merge operation's
shape. Out of scope (design §12): batching PRs into one check, a daemon
or persistent queue, merging without the owner's command, repairing
failed checks, landing a conflicting final milestone PR.

## Spike: fast-forward-only landing on a real Forgejo

```issue
key = "ff-only-spike"
depends = []
size = "S"
```

Prove, with an e2e test against a throwaway instance, what the design
assumes of Forgejo's `fast-forward-only` merge style (design §4.1), before
the merge operation is built on it. The test, in `e2e/`, drives the
Forgejo API with the owner's and `manager`'s tokens as the e2e suite does,
and goes through:

1. a PR from `issue/<N>` into `main`, approved by a verifier of another
   family at its head, whose description ends `Closes #N`;
2. a landing commit built on the forge's repository with `git commit-tree
   <tree> -p <main tip> -p <head>` and pushed to `issue/<N>` as `bottown`
   (the way `opPRMerge` in `internal/server/merge.go` pushes its merge,
   with the parents swapped), followed by `ReconcileCarries` if Forgejo
   dismissed the approval;
3. `MergePullRequest` as `manager` with `Style: fast-forward-only` and
   `HeadCommitId` = the landing commit.

It asserts that: the PR is merged with `merged_commit_id` equal to the
landing commit and `main` at it; issue `#N` is closed by the merge; the
protection of `main` (required approval, dismiss stale) let the merge
through with the undismissed approval; and that, when another commit is
pushed to `main` between steps 2 and 3, the merge fails with 409 and
`main` is untouched. It also records whether the repository setting
`allow_fast_forward_only_merge` must be provisioned (it is on in
`bottown/bottown`; check a fresh repository).

### Acceptance criteria

- The e2e test passes under `make e2e`, or fails on a named assertion.
- A comment on this issue reports each assertion's result. If any fails,
  the comment says which, and `land-merge-op` and the issues that depend
  on it do not start: design §4.1 is redesigned with the owner, around an
  atomic compare-and-swap on the base (the candidate it names is a
  `--force-with-lease` push of the landing commit with
  `autodetect_manual_merge`), and the replacement passes these assertions,
  the moved-base race included, in a revised spike. An ancestor check
  before a merge-commit merge is not an acceptable replacement.
- Provisioning sets `allow_fast_forward_only_merge` on repositories it
  creates if the spike finds it is not on by default.

## Flow: no clean base merges; conflicts still come before the approved stop

```issue
key = "flow-no-base-merge"
depends = []
size = "M"
```

Remove the flow's clean merges of the base into a PR (design §7). In
`internal/flow/flow.go`: delete the `merge.Behind && merge.Clean` branch
that calls `MergeBase`, with `Options.MergeMilestoneBase`, `autoMerge`,
`confirmMerge` and the attended prompt; drop the `--merge-milestone-base`
flag from `bottown flow` (`internal/cli/flow.go`) and from the process
`milestone run` launches (`internal/cli/milestone_run.go`). Keep the
conflict rounds (`conflicts with base`, bounded by `--rounds`): a PR
behind its base with a conflict still gets an implement round before any
verify. A PR behind its base cleanly is gated and verified at its own
head.

Keep the loop's order: the trial merge and the conflict round stay ahead
of the approved stop, so an **approved** PR that conflicts with its base
still gets a conflict round (design §7). That is what lets `merge-all
--actor` and `milestone run` repair Land's `Conflict` by running the flow
on a PR that is still approved; do not move the approved stop ahead of
the conflict check. When an approved PR is behind cleanly, say so in the
stop's detail: "!N is approved at <sha>: behind `main` by K commits,
cleanly; `bottown merge-all` lands it". `TestFlowMergeBaseCleanApproved`
(`internal/flow/flow_test.go`) pins the clean merge of an approved PR;
replace it with the tests below.

The inbox's "merge main into the PR" offer stays until `web-ui-land`
replaces it; nothing else calls `MergeBase` from the flow after this.

### Acceptance criteria

- Flow tests: an approved PR behind a clean base stops `ready to merge`
  with no new head and the detail above; an unapproved PR behind a clean
  base is gated and verified at its own head, with no merge commit; a PR
  behind a conflicting base still gets a conflict round; a PR **already
  approved** whose base then conflicts (a sibling landed after the
  approval) gets a conflict round, not `ready to merge`, and the verify
  run follows the implementer's push; the e2e
  expectations that mention the merge prompt or `Merge main into` are
  updated.
- `bottown flow --help` and `milestone run` no longer know
  `--merge-milestone-base`.
- `docs/reference/runs/flow-stops.md`, `milestone-run.md` and
  `docs/design/web-ui.md` §3.2 describe the loop without the base merge.

## Server: the landing commit operation

```issue
key = "landing-commit"
depends = []
size = "M"
```

Add `POST /v1/forge/pr/landing` `{repo, number, head_sha}` (owner only;
design §4 step 3, §9) in `internal/server`, next to `opPRMerge` in
`merge.go` and reusing `prMergeState`, `mergeGit` and `pushMerge`:

1. refuse (409) if the PR is not open, its head is not `head_sha`, a live
   run holds the PR or its branch (`liveRunOnPR`), or the trial merge of
   the base into the head conflicts (name the files);
2. build the landing commit with `commit-tree` on the merged tree, **base
   first**: `-p <base tip> -p <head>`; when the head already contains the
   base's tip, the tree is the head's tree and the parents are the same;
   title `Merge !N (<PR title>) into <base>`, body naming the head and the
   base SHA, author and committer `bottown`;
3. push it to the PR branch, wait for Forgejo to process the push
   (`waitPushSynced`, `internal/server/pushsync.go`), run the carry
   reconciliation `opReconcileCarries` does, re-read the PR and its
   reviews, and return `{head_sha, base_sha, assessment}` with the
   assessment `pr show` would give at the new head.

Audit it as `landing`. Extend `GET /v1/forge/pr/merge-base`'s result with
the files the PR's own delta touches (`git diff --name-only
<merge base>..<head>`, renames as both paths), for the order in
`merge-all`.

### Acceptance criteria

- Server tests over `reviewtest.PatchCases`' repositories and a fake
  Forgejo: the landing commit's parents and tree; the carry kept on a
  clean merge and lost when the base edited the diff's context; the
  refusals for a moved head, a live run and a conflict; the files list.
- An actor's or run's key gets 403.
- `docs/reference/` gains a landing page describing the operation, linked
  from the index.

## Server: one merge operation for `main` and `milestone/*`

```issue
key = "land-merge-op"
depends = ["ff-only-spike"]
size = "M"
```

Generalize `opMilestoneMerge` (`internal/server/milestone_merge.go`) into
the landing merge of design §4 step 6 and §9, keeping the route
`POST /v1/forge/pr/merge` and the request `{repo, number, head_sha}`:

- the base may be the default branch or `milestone/*`;
- the head must be exactly `head_sha`, and the assessment at it
  `approved` (same `assessReviews` + `carryOver` path); also undismiss
  carried approvals (`undismissCarried`) before merging, so Forgejo's
  protection counts them;
- merge as `manager` with `Style: fast-forward-only` and `HeadCommitId`;
  Forgejo 15 refuses a moved base with a 500 and no message (the spike,
  design §4.1), so on a 409 or 500 re-read the base: if its tip is no
  longer an ancestor of the landing commit, return its own condition
  (`base_moved`), distinct from the other refusals; otherwise return
  Forgejo's error;
- close and comment on the child issue only when the base is
  `milestone/*` and the head is `issue/<N>`; on the default branch rely on
  the PR's closing keywords and never close an issue from a branch name;
- `already_merged` on repeat, as today; audit as `land`, allow and deny.

This issue starts only after the spike passed. If it failed, this issue
waits for the redesign of design §4.1, and is rewritten to its atomic
guard; it is never built on an ancestor check followed by a merge-commit
merge, which lets a base commit that moved in between land unchecked.
The spike (#406) found only the status wrong (500, not 409); the owner
kept the style with the read-after classification above (design §4.1).
`bottown milestone merge` keeps working unchanged through the operation.

### Acceptance criteria

- The tests of `milestone_merge_test.go` pass with the new style, plus:
  a merge into the default branch closes nothing itself; a base that
  moved is reported as `base_moved`; a carried approval is undismissed
  before the merge; run and actor keys are refused.
- `docs/reference/runs/milestone-child-merge.md` becomes the landing
  merge page and says what differs per base.

## Checker: run the `check` group as well as `e2e`

```issue
key = "checks-both-groups"
depends = []
size = "M"
```

`flowChecker` (`internal/cli/flowcheck.go`) runs only the `e2e` group.
Give `flow.CheckRequest` the groups to run (`check`, `e2e`, or both; the
flow and `milestone finish` keep asking for `e2e` only), run each group
in the same clean detached worktree in order, stop at the first failure,
log each to its own file (`flow-<issue>-<sha>-<check>.check.log` and
`.e2e.log`), and record each group that ran as the owner's
`bottown/check` or `bottown/e2e` status at the SHA with the `gate` mark,
through the path the flow's e2e gate uses. The `CheckOutcome` says which
groups ran and which failed. `runs.CheckRecord` and Live Runs show both
logs. A repository that names no commands for a group skips it and says
so, as the e2e gate does today.

### Acceptance criteria

- Checker tests with a fake `.bottown.toml`: both groups run and are
  recorded; a `check` failure stops before `e2e`; a missing group is
  skipped and reported; the flow's requests still run `e2e` only.
- `docs/reference/checks.md` ("The flow's own run") describes the groups
  a gate can run and both logs.

## The landing order

```issue
key = "land-order"
depends = []
size = "S"
```

A pure function in `internal/land`, `Order(candidates) []Candidate`,
where a candidate carries its PR number, whether its trial merge with the
current base is clean, and the files its delta touches (design §5.2):
clean before conflicting; then the fewest other candidates sharing a
touched file, ascending; then the lowest PR number. Also
`Overlaps(candidates)` for the dry run's report (each candidate's count
and the files it shares). The caller recomputes after each landing with
fresh trial merges.

### Acceptance criteria

- Table tests: clean first; a broad PR overlapping three narrow ones
  goes last; ties by number; renames count both paths; a single candidate;
  no candidates.

## The landing primitive

```issue
key = "land-primitive"
depends = ["landing-commit", "land-merge-op", "checks-both-groups"]
size = "L"
```

`internal/land`: `Land(ctx, api, checker, Policy, pr) Outcome` (design
§4), host-side, over the server operations through `internal/client`:

1. hold the issue's flow lock for an `issue/<N>` head (`runs.HoldFlow`);
   re-read the PR; refuse one that is not open, not `approved`, or whose
   base changed;
2. `POST /v1/forge/pr/landing` at the head seen: a conflict is
   `Conflict{Files}`;
3. if the returned assessment is not `approved`: `NeedsVerify`;
4. run the policy's groups (`Main`: `check` and `e2e`; `Milestone`:
   `check`) at the landing commit through the checker: a failure is
   `ChecksFailed{Log, Description}`;
5. `POST /v1/forge/pr/merge` at the landing commit: `base_moved` goes back
   to step 2, at most three times, then `BaseMoved`; success is
   `Landed{MergeCommit}`.

Every outcome has a one-line `String()` the callers print and comment
(PR, head, reason, log). Land takes a `Repair` hook the callers may set
(none in this issue): when it returns true after a `Conflict` or
`NeedsVerify`, Land releases the lock around the hook, then revalidates
from step 1.

### Acceptance criteria

- Unit tests with a fake API and checker, under `-race`: each outcome;
  the bounded `base_moved` retry; the lock held across the landing and
  released around the repair hook; a live-run refusal; the milestone
  policy running `check` only.
- `docs/reference/` describes Land's steps, outcomes and policies as the
  one page the three callers' pages link to.

## `bottown merge-all`

```issue
key = "merge-all"
depends = ["land-primitive", "land-order", "flow-no-base-merge"]
size = "L"
```

`bottown merge-all [PR...] [--actor A] [--verify V] [--dry-run] [--repo R]`
(owner only; design §5), in `internal/cli`:

- **First** fetch the default branch from the forge and fast-forward the
  local `main` of the owner's checkout (`mainCheckout`); stop if it has
  diverged or has uncommitted changes with `main` checked out. Do it again
  when the batch is done. Never run `after_pull`.
- **Batch:** with no arguments, the open PRs into the default branch
  that `pr list` assesses `approved`, not drafts, with no live run and no
  held flow lock, and not part of a milestone: their issue has no trusted
  `bottown:milestone` child marker, and their head is not `milestone/*`.
  Arguments restrict the batch to those numbers, milestone PRs included;
  a number that is not a candidate is reported and skipped.
- **Loop:** order the remaining candidates (`land.Order`, with fresh trial
  merges from `GET /v1/forge/pr/merge-base`), land the first with the
  `Main` policy, print its outcome line, repeat.
- **Repair:** without `--actor`, `Conflict` and `NeedsVerify` are
  reported. With `--actor A`, after the clean candidates landed, run the
  flow's supervisor in-process on each remaining `issue/<N>` candidate
  with implementer `A` and verifier `--verify V`, else the recorded pair:
  the issue's `implement/` and `verify/` labels, else the PR's author and
  the actor whose approval the assessment counted; the flow enforces the
  cross-family rule. Then land again. A final milestone PR is never
  repaired. An owner-approved PR with no verifier on record is reported.
- **Output:** one line per PR as it goes, a table at the end;
  `--dry-run` prints the order, each trial merge and overlap count, and
  the local `main` step, touching nothing. Exit 0 when every candidate
  landed, 1 otherwise.

### Acceptance criteria

- CLI tests with a fake server: the default batch leaves out a milestone
  child's PR into `main` and a `milestone/*` PR, and takes them when
  named; the order is followed and recomputed; a `Conflict` is reported
  and the batch continues; with `--actor` the repair runs after the clean
  landings with the right pair; starting from an already-approved PR that
  conflicts once a sibling landed, the repair's flow runs an implement
  round and then a verify run (it does not stop `ready to merge` at the
  unchanged head), and the PR lands at the repaired head; `--dry-run`
  writes nothing; exit codes;
  the local `main` fast-forward, and the stop on a diverged `main`.
- An e2e test: two approved PRs behind `main` land with no new verify run,
  both with gate statuses at their landing commits and `main`'s
  first-parent history showing one merge commit each; a third whose
  context `main` edited is reported `NeedsVerify`; `main` moved during a
  landing's checks is retried and lands at a new landing commit.
- `docs/reference/` gains the `merge-all` page; the fam guide names the
  command among the owner's.

## Milestones land through Land

```issue
key = "milestone-land"
depends = ["land-primitive", "flow-no-base-merge"]
size = "M"
```

`milestone run` (`internal/milestone/run.go`, `internal/cli/milestone_run.go`)
and `milestone merge` call `land.Land` with the `Milestone` policy instead
of `POST /v1/forge/pr/merge` directly (design §6):

- `handle(FlowReady)` lands the child; `Landed` is reported as today, and
  the "head not gated" comment on an open final PR stays;
- `Conflict` and `NeedsVerify` re-dispatch the child to its flow once per
  invocation (clear its `ended` mark and let the loop pick it up with its
  recorded pair); a second such outcome stops it as `merge failed` with
  the reason, as today;
- `ChecksFailed` stops the child with the log for the owner;
- when several children are ready together (a resume), land them in
  `land.Order`'s order.

`bottown milestone merge C` lands through the same call, so a child merged
by hand is also checked at the combined head.

### Acceptance criteria

- Scheduler tests with the fake forge and a fake Land: a child landing
  behind a sibling is checked with `check` only; an approved child that
  conflicts after a sibling landed is re-dispatched once, and its flow
  runs an implement round and then a verify run before it lands again; a
  child that conflicts again after that is stopped; `ChecksFailed` stops with the log;
  several ready children are landed in order.
- The e2e milestone scenario asserts a sibling landing after a child
  became ready is landed only at a checked combined head.
- `docs/reference/runs/milestone-run.md` and
  `milestone-child-merge.md` describe the landing and the re-dispatch.

## Web UI: land from the inbox

```issue
key = "web-ui-land"
depends = ["land-primitive"]
size = "M"
```

The inbox (`internal/webui/inbox.go`, `web/src`) hides a PR that is
behind from **Ready to merge** and offers "merge main into the PR". Make
every candidate a **Ready to land** item (design §8) with a **Land**
action: `POST /api/actions/land` runs `land.Land` for that PR in the
background with the `Main` policy, like a flow, with a record and log
under Live Runs and a stop control; the item says that merging in Forgejo
instead skips the checks at the combined tree. Remove the PR-first merge
action and, when nothing else calls it, `POST /v1/forge/pr/merge-base`'s
merge (keep the status read). The Milestones tab shows a child's landing
on its node like its flow.

### Acceptance criteria

- Behavioral frontend tests and inbox tests: a behind, approved PR is a
  Ready to land item; the action starts a landing and its record shows
  under Live Runs; the old action is gone. Fixtures gain a landing in
  progress.
- `docs/design/web-ui.md` §4.1 and `docs/reference/` describe the item
  and the action.
