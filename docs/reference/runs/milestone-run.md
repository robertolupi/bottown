# Milestone run

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
   with a `.bottown/flow-<N>.json` record while it runs, so the web UI
   shows and can stop it. The record is written once the flow holds the
   issue's lock, and removed only while it is still that flow's. A child
   another flow is already driving (from the web UI, or another run) is
   left to it, its record untouched, and stops as `flow busy` in this
   invocation. Waiting for a busy actor is the flow's own
   retry. Interrupting run interrupts its flows, and it exits when they
   have stopped.
5. **Results:** when a flow exits, run reads its stop, the owner's last
   stop comment on the child since the flow started:
   - `ready to merge`: run merges the PR with `POST /v1/forge/pr/merge` at
     its head, which closes the child and may make others ready. The
     merge is fast-forward-only ([the landing merge](milestone-child-merge.md)),
     so a head that does not contain the milestone branch's tip is refused
     as `base_moved`, and the child stops as `merge failed`. If a
     final PR is open, run comments on it that its head has not been
     gated, so the owner does not merge it; finish re-gates it later.
   - `agent did nothing`: the marker's actor leaves the pool until run
     exits; its child stays stopped, with no other actor substituted.
   - any other stop, or a flow that ended without one: the child is left
     to the owner as the flow left it.

   A child whose flow ended is not dispatched again in the same
   invocation.

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

The web UI's Milestones tab starts and stops it (owner only, like its
other actions). An open epic's group has **Run milestone**, which asks for
`--agents` and `--parallel`: `POST /api/actions/milestone-run`
(`internal/webui/milestone_run.go`) checks the pool with the same
`internal/agents` parser against the instance's actors, showing its
message in the dialog, refuses a slug whose run the UI is already
running, and starts `bottown milestone run SLUG` in the background,
logged to `.bottown/milestone-<slug>.log` with a
`.bottown/milestone-<slug>.json` record (its pool, parallelism and PID)
while the process is alive. The group then shows that it is running, its
pool, its parallelism and its child flows (the group's nodes with a flow
record), and **Stop** sends `milestone-<slug>` to the stop action, which
signals the run's process group with SIGTERM as it does a flow's and
removes the record; run interrupts its flows and exits when they have
stopped.
