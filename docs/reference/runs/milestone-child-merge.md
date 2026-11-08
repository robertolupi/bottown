# The landing merge

The shared [landing primitive](../land.md) describes the host-side steps,
policies and outcomes; [`bottown merge-all`](../merge-all.md), milestone run
and milestone merge are its callers.

`POST /v1/forge/pr/merge` (owner only; `{"repo", "number", "head_sha"}`)
is the one operation through which bottown merges a pull request, and it
acts as the `manager` account, which nothing else in the server uses. It
refuses, with HTTP 409 and the condition that failed, unless:

- the PR's base is the repository's default branch or a `milestone/*`
  branch;
- its head is exactly `head_sha`;
- the review assessment, the one the flow and the inbox read
  (`internal/review`), reports it approved at that head: a verifier of
  another family than the author's, or the owner. A same-family approval
  does not count. An approval at an older head counts when its patch
  carries over to the head ([the landing commit](../landing.md)).

Before it assesses, it undismisses the approvals Forgejo dismissed
automatically on a push whose patch carries over, or that are at the head
itself, as the landing step does, so that Forgejo's branch protection
counts them too.

Then, as `manager`, it merges the PR **fast-forward-only**, passing the
head to Forgejo (`HeadCommitId`). That is Forgejo's compare-and-swap on the
base: the merge succeeds only when the base's tip is an ancestor of that
exact head, which a [landing commit](../landing.md) built on the base's
tip is, and the base then moves to the head. No merge commit is built at
merge time, so the tree that lands is the tree that was checked. Forgejo
recomputes a pull request's mergeability asynchronously after a push, so a
merge right after the landing push can be refused with 405 until it
settles; the server waits and tries again, bounded (30 seconds). When the
base moved since the landing commit was built, Forgejo refuses (Forgejo 15
answers 500 with no message); the server then reads the base in the
forge's repository and, if its tip is no longer an ancestor of the head,
answers 409 with the error code `base_moved` (`client.ErrBaseMoved`), so a
caller can build the landing commit again. Any other refusal is reported
as Forgejo's error, without that code.

What happens after the merge depends on the base:

| Base | Head | After the merge |
|---|---|---|
| `milestone/*` | `issue/<C>` | closes `#<C>` and comments on it with the PR, the base and the head SHA, because Forgejo applies `Closes #<C>` only on merges into the default branch |
| `milestone/*` | any other branch | nothing: there is no child issue |
| default branch | any | nothing: Forgejo applies the PR's closing keywords; no issue is closed from a branch name |

Each refusal and each merge is audited as `land`, `deny` or `allow`.
Calling it again for a merged PR reports `already_merged` instead of
failing, and, for a milestone child, closes and comments on `#<C>` if it
is still open, so a call that stopped after the merge is finished by the
next. Run keys and actor keys are refused (403).

Its callers reach it through [`land.Land`](../land.md), which builds the
landing commit and checks it first.

`bottown milestone merge C [--sha SHA] [--check-timeout 1h] [--repo R]`
(owner only) lands the open PR from `issue/<C>` by hand, through the same
call as [milestone run](milestone-run.md): `land.Land` with the
`Milestone` policy builds the landing commit on the milestone branch's
tip, runs the `check` group at it and records `bottown/check` there, and
then merges that commit here. So a child merged by hand is also checked
at the combined head, behind whatever siblings landed since its approval.
`--sha`, if given, is the head the PR must be at. It prints Land's
outcome; on a conflict, an approval that does not carry to the landing
commit, or failed checks (the line names the check's log) it merges
nothing and exits 1. A child whose PR is merged already is reported as
merged.

When milestone run lands a child and the outcome is a conflict or a lost
approval, it sends the child back to its flow once per invocation, which
starts a conflict round and then a verify, and lands it again; the second
time it stops as `merge failed` ([milestone run](milestone-run.md)).
`milestone merge` repairs nothing.
