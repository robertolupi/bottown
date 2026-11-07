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

When a pull request's base has moved and the trial merge is clean, an
attended `bottown flow` asks the owner before merging the base into the
head; an unattended one (`--unattended`, as the web UI starts it) leaves
the merge to the owner's inbox. `bottown flow --merge-milestone-base`
merges a clean `milestone/*` base without asking, attended or not, so a
milestone child stays up to date as its siblings land. The approval
carries over when the patch is unchanged; otherwise the flow verifies the
new head. The flag does nothing for any other base: the default branch is
still the owner's to merge. A conflicting base is never merged; it starts
an implement round, as without the flag, against `--rounds`.
