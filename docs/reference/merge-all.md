# `bottown merge-all`

`bottown merge-all [PR...] [--actor A] [--verify V] [--dry-run] [--repo R]`
lands approved pull requests into the default branch, one at a time, each
through [the landing primitive](land.md) with the `Main` policy: it brings
the base in, runs the repository's `check` and `e2e` commands at the exact
combined commit, records them as the owner's `bottown/check` and
`bottown/e2e` gate statuses, and
[merges fast-forward-only](runs/milestone-child-merge.md) as the
`manager` account. The owner runs it; that is the authorization. The
command needs the repository's owner key and runs in the owner's checkout.

## First and last, the local `main`

Before it reads a candidate, merge-all fetches the default branch from the
forge and fast-forwards the local branch of the owner's checkout to it,
because that checkout is often behind the forge's after a day of merges.
It stops, before anything lands, when the local branch has diverged from
the forge's, or is checked out with uncommitted changes to tracked files.
When the batch is done it fast-forwards it again, so the checkout holds
what landed. It never runs `.bottown.toml`'s `after_pull` (`make install`
here): upgrading prod stays the owner's command.

With `--repo R` for another repository of the instance, the base is R's
default branch on the forge, and the command leaves the local checkout's
branches alone: it holds no local copy of R.

## The batch

With no arguments the batch is every open pull request into the default
branch that `pr list` assesses `approved` at its head, is not a draft, has
no live run and no
[held flow lock](runs/one-flow-per-issue.md), and is not part of a
milestone: neither a milestone child's pull request (its issue carries the
owner's `bottown:milestone` child marker) nor a `milestone/*` head. Those
land through [milestone run](runs/milestone-run.md), or by naming them.

Pull request numbers as arguments restrict the batch to those numbers,
milestone pull requests included; one that is not an open, approved,
synced, unheld, non-draft candidate is reported and skipped. A named pull
request lands into its own base, a `milestone/*` branch included, not into
the default branch: the batch filter, not the landing, is what keeps the
default run on the default branch. A pull request approved after the
command starts waits for the next invocation, and each candidate is
revalidated just before it is touched.

## The order

The order is recomputed before each landing, from a trial merge of the
current base into each remaining candidate and the files its own delta
touches, with `land.Order`: clean candidates before conflicting ones, then
the fewest other remaining candidates that share a touched file, then the
lowest number. The order is a hint, never a judgement: it does not touch
approvals or checks, and it does not promise to avoid a conflict.

## Repair

Without `--actor` a conflict and a lost carry (a landing commit whose
review did not carry over, reported `needs verify`) are reported and left.
With `--actor A`, after the clean candidates have landed, merge-all runs
[the flow's supervisor](runs/flow-stops.md) in-process on each
remaining `issue/<N>` candidate, with implementer `A` and verifier
`--verify V`, else the candidate's recorded pair: the issue's
`implement/<a>` and `verify/<b>` labels, else the pull request's author and
the actor whose approval the assessment counted. The flow enforces the
cross-family rule, and merge-all lands a candidate again at the repaired
head if it is one. A final milestone pull request (a `milestone/*` head)
is never repaired here, and an owner-approved pull request with no
verifier on record is reported rather than repaired.

## Output and exit

One line per pull request as it goes: where it landed, or the outcome and
its reason. A table of the batch follows. `--dry-run` prints the order,
each trial merge and its overlap count, and the local `main` step,
touching nothing. The exit status is 0 when every candidate landed, 1 when
any remains unmerged or on an error, or a named number was not a
candidate. Running `merge-all` again resumes: merged pull requests are out
of the batch, and one left at its landing commit is a candidate again if
it is still approved.
