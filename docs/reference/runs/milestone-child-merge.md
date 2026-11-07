# Merging a milestone child

`POST /v1/forge/pr/merge` (owner only; `{"repo", "number", "head_sha"}`)
is the one operation through which bottown merges a pull request, and it
acts as the `manager` account, which nothing else in the server uses. It
refuses, with HTTP 409 and the condition that failed, unless:

- the PR's base is a `milestone/*` branch;
- its head branch is `issue/<C>`, the child issue it closes;
- its head is exactly `head_sha`;
- the review assessment, the one the flow and the inbox read
  (`internal/review`), reports it approved at that head: a verifier of
  another family than the author's, or the owner. A same-family approval
  does not count.

Then, as `manager`, it merges the PR with a merge commit (passing the head
to Forgejo, which refuses if it moved), closes `#<C>`, because Forgejo
applies `Closes #<C>` only on merges into the default branch, and comments
on `#<C>` with the PR, the base and the head SHA. Each step is audited as
`milestone_merge`. Calling it again for a merged PR reports
`already_merged` instead of failing, and closes and comments on `#<C>` if
it is still open, so a call that stopped after the merge is finished by
the next. Run keys and actor keys are refused (403).

`bottown milestone merge C [--sha SHA] [--repo R]` (owner only) calls it
for the open PR from `issue/<C>`, with `--sha` defaulting to the PR's
current head, so the owner can merge a child by hand.
