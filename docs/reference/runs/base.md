# A run's base

An implement or plan run on issue `#<n>` has a base: the branch
`issue/<n>` starts from when the forge does not have it yet, which the
prompt names and `pr create` opens the pull request into. `POST /v1/runs`
decides it and keeps it on the run record (`base`), so `bottown run`,
`bottown flow` and leased runs all use the same rule, in this order:

1. the base of the open pull request from `issue/<n>`, if there is one;
2. `milestone/<slug>`, when the issue is a milestone child: its body ends in
   a `bottown:milestone` marker with a `key` (an epic's `role="epic"` does
   not count), the owner filed it (an actor's copy of a marker never counts),
   and the branch `milestone/<slug>` exists on the forge;
3. the repository's default branch.

So the owner starts a milestone by creating `milestone/<slug>` with
`bottown milestone branch <slug>` ([milestone branch and
finish](milestone-branch.md)); until then its children's runs
work against the default branch. Runs recorded before the base was kept use
the default branch.

Provisioning protects `milestone/*` next to the default branch, with the
default branch's approval settings, but only the manager and the owner on
the push and merge allow-lists: actors cannot push to, create or merge into
a milestone branch. The web UI's milestone view shows a plan's base as
`milestone/<slug>` once that branch exists.

When merging a pull request's base into it would conflict, the flow's stop
is "conflicts with base" and names the base; stops flows wrote before as
"conflicts with main" read back as the same stop.

When a pull request's base has moved and the trial merge is clean, the
flow does not merge the base into the PR: an unapproved PR is gated and
verified at its own head, and an approved PR stops `ready to merge` at its
own head with `: behind \`<base>\`, cleanly; \`bottown merge-all\` lands
it` in its detail. When the trial merge would conflict, the flow starts a
conflict implement round (up to `--rounds`) ahead of the `ready to merge`
stop, so an approved PR whose base later conflicts gets a conflict round
before any verify.

While a request for changes stands at the head, the flow does not start a
conflict round: the implement round answers the request first, and any
base conflicts are resolved at the head that round pushes.
