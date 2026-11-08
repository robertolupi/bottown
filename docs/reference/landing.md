# The landing commit

The shared [landing primitive](land.md) describes the host-side steps,
policies and outcomes; command integration is separate work.

`POST /v1/forge/pr/landing` (owner only; `{"repo", "number", "head_sha"}`)
builds and pushes the landing commit for an open pull request: a merge
commit on the PR branch whose first parent is the base's tip and whose
second parent is the head, so the base can move to it with a
fast-forward-only merge while the base's history keeps one merge commit per
PR (design §3, §4 step 3, §9).

It refuses, with HTTP 409 and the condition that failed, unless the PR is
open, its head is exactly `head_sha`, no live run holds the PR or its
branch, and the trial merge of the base into the head is clean (a conflict
names the files). Nothing is pushed on a refusal.

The commit is built with `git commit-tree` on the trial merge's tree, base
first (`-p <base tip> -p <head>`). When the head already contains the
base's tip the tree is the head's own and the parents are the same, so the
base still gets its one merge commit per PR. Its title is
`Merge !N (<PR title>) into <base>` and its body names the head and the
base SHA; the author and committer are `bottown`. It is pushed to the PR
branch as the owner.

Forgejo processes a push in the background: it updates the PR ref, rechecks
mergeability and dismisses stale approvals for the new commits
(`internal/server/pushsync.go`). The operation waits for the PR ref to
reach the landing commit, then reconciles the approvals the push dismissed
when their patch carried over (`review.SamePatch`; the same reconcile step
`POST /v1/forge/pr/carries` runs), re-reads the PR and its reviews, and
returns `{head_sha, base_sha, assessment}`. `head_sha` is the landing
commit, `base_sha` the base tip it contains, and `assessment` is the
decision `pr show` gives at the new head: `approved` when the approval
carried over, otherwise `needs review`. The call is audited as `landing`.

`GET /v1/forge/pr/merge-base` (owner only) reports the trial merge as
before and now also lists the files the PR's own delta touches, from the
merge base to the head, a rename counted as both paths, for `merge-all`'s
overlap order (design §5.2).
