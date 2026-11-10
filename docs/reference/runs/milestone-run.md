# Milestone run

Run lands each child through the shared [landing primitive](../land.md)
with the `Milestone` policy; that page describes its steps and outcomes.

`bottown milestone run <slug> --agents SPEC --parallel N [--rounds 3]
[--timeout 1h] [--check-timeout 1h] [--repo R]` (owner only) drives the
active children of plan `<slug>`'s epic to completion on
`milestone/<slug>`. There are no defaults for the pool or the
parallelism, and no token budget:

- `--agents` is the pool, comma-separated `actor[:model]=N` entries
  (`internal/agents`): `N` is the most flows the entry is paired in at
  once, as implementer or verifier, or `*` for no limit, and `:model` pins
  the harness model of that entry's runs. Run refuses an empty spec, a
  malformed entry, an unknown actor, an actor named twice, and a pool in
  which a family has no verifier of another family, naming the entry or
  family.
- `--parallel N` (`N ≥ 1`) caps the children in flight, and so the e2e
  suites running at once.
- `--rounds`, `--timeout` and `--check-timeout` are passed to each flow
  (`--check-timeout` to finish too).

Run refuses unless the plan has an Implementation Epic filed by `apply`.
It creates `milestone/<slug>` if it is missing, as `milestone branch`
does. When the last final PR from `milestone/<slug>` into the default
branch was merged, and the branch does not contain its merge, run first
merges the default branch into it as `manager`
(`POST /v1/forge/milestone/restart`, owner only), so a new round, children
added since by editing the plan and applying it again, starts from it; on
a conflict it stops and names the files. While a final PR is open, or none
was merged, the restart does nothing.

Then it loops, rebuilding everything from the forge each time:

1. **Children** are the epic's active children (`milestone.Epic`, as for
   finish). An issue linked to the epic by hand, without the plan's
   child marker from the owner, is ignored; an issue whose key left the
   plan (its marker kept, its edge from the epic dropped) is flagged. Both
   are named in the summary; neither is dispatched or needed to finish.
   Run checks the box of each closed child in the epic's `## Implementation
   issues` checklist that is still unchecked, as `apply` renders it, and
   updates the epic marker's sha to match, so the next `apply` finds
   nothing to rewrite. It edits the epic with the owner's key and prints
   `checked #N in epic #E`; a failed edit is a warning, and the next pass
   tries again. It never unchecks a box.
2. **Ready** children are open, with every issue they are blocked by
   closed (an issue the list does not hold counts as open). They are
   dispatched in issue order while fewer than `--parallel` are in flight.
   Closed children are done and skipped.
3. **Pairing:** a child whose `implement/<actor>` and `verify/<actor>`
   labels name two entries of the pool, of different families, keeps that
   pair, waiting for their capacity; otherwise it takes the pool's next
   cross-family pair with capacity, round-robin, and the labels are
   rewritten with the owner's key. A ready child no pair fits is reported
   as unpaired. Provisioning creates both labels, exclusive, for every
   actor.
4. **Flows:** each child gets a `bottown flow N --implement A --verify B
   --unattended` process of its own (the flow
   command is one supervisor per process), logged to `.bottown/flow-<N>.log`
   and discovered by the web UI through its held issue lock and status
   file. A child another flow is already driving is left to it and stops
   as `flow busy` in this invocation. Waiting for a busy actor is the flow's own
   retry. Interrupting run interrupts its flows, and it exits when they
   have stopped.
5. **Results:** when a flow exits, run reads its stop, the owner's last
   stop comment on the child since the flow started:
   - `ready to merge`: run lands the PR on `milestone/<slug>` with
     `land.Land` and the `Milestone` policy, as `bottown milestone merge`
     does ([the landing merge](milestone-child-merge.md)): it builds the
     landing commit, the branch's tip merged with the PR's head, runs the
     `check` group there (about a minute; `e2e` stays with finish), records
     `bottown/check` on it, and merges it fast-forward-only as `manager`,
     which closes the child and may make others ready. So a child landing
     behind a sibling's merge is checked at the combined tree, and a
     sibling that breaks it is named. A base that moves during the checks
     is rebuilt and checked again. The landing runs in the result loop:
     dispatch waits for it. If a final PR is open, run comments on it that
     its head has not been gated, so the owner does not merge it; finish
     re-gates it later. What else the landing can end in:
     - **Conflict** (the branch's tip conflicts with the head) or **needs
       verify** (the approval does not carry to the landing commit): the
       child goes back to its flow, once per invocation. Its `ended` mark
       is cleared and the loop dispatches it again with its recorded pair;
       the flow starts a conflict round although the PR is approved, and
       the verify that follows, and when it stops `ready to merge` again
       run lands it again. A second such outcome stops it as `merge
       failed`, with the outcome as the reason.
     - **Checks failed** at the landing commit: the child stops as
       `checks failed`, with the description and the check's log, for the
       owner. Nothing merged.
     - any other refusal, or a base that kept moving: the child stops as
       `merge failed`, with the reason.

     Flows that have ended by the time run lands are ready together (on a
     resume, several approved children's flows stop at once): run lands
     them one at a time in `land.Order`'s order, as `merge-all` does,
     from a fresh trial merge of the branch into each before each
     landing; if that fails, in the order their flows ended.
   - `agent did nothing`: the marker's actor leaves the pool until run
     exits; its child stays stopped, with no other actor substituted.
   - any other stop, or a flow that ended without one: the child is left
     to the owner as the flow left it.

   A child whose flow ended is not dispatched again in the same
   invocation, except once after a conflict or a lost approval at its
   landing.

When nothing is in flight and nothing more can be dispatched, run ends.
If every active child is closed, it runs `bottown milestone finish`.
Otherwise it comments a summary on the epic, `**bottown milestone run:**`,
listing the merged children, the stopped ones with their reasons, the
blocked ones with what blocks them, the unpaired ones, the removed pool
entries, and the ignored and flagged issues; it prints the same summary.

Run keeps no state of its own. Running it again resumes: stopped children
(open, their flows stopped earlier) are dispatched again once ready and
their flows resume on the existing PRs, labelled pairs are kept, and
entries removed after `agent did nothing` are back in the pool.

| Exit | When |
|---|---|
| 0 | every active child is closed and finish opened or updated the final PR |
| 3 | run stopped with children open, and an agent did nothing |
| 1 | it stopped otherwise with children open, finish failed, or an error |

The read-only Milestones tab offers `bottown milestone run SLUG --agents
POOL --parallel N` to copy. Start it in a terminal and interrupt it there
to stop it; run interrupts its child flows and waits for them to exit.
The UI shows the child flows through their held issue locks and status
files. It does not display the scheduler's pool, parallelism or running
status: the CLI publishes no scheduler status record.
