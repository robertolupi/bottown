# Design: sizing issues: a guide, implement-time sizing and `bottown estimate`

Status: **design**, settled from issue #439 after two debate rounds (the
wiki page `Review-439-auto-estimate-issue-size-for` and its round files are
their record; they bind nothing, this document does) and the owner's
comments of 2026-11-09. Parts 1 and 2 of the plan (§6) are built;
`bottown estimate` is not. Where this design and
[the reference](../reference/index.md) differ once work lands, the
reference says what is built.

## 1. The problem

The `size/S`, `size/M`, `size/L` and `size/XL` labels
(`internal/config/default.toml`) are the owner's estimate, or a planner's
proposal that `milestone apply` sets on an issue with none
(`internal/milestone/apply.go`, `sizeLabel`). They feed the estimate
against measured table (`internal/stats/breakdown.go`) and weigh a
milestone's burn chart (`internal/stats/stats.go`, `sizeWeights`). Most
issues carry none, because sizing every issue is a chore the owner does
not do, so both uses see little data.

Agents read every issue they implement. This design has them size what
they read, by one shared guide, so that sizes are filled in where they
are missing, and are consistent enough to compare.

What it does not do: replace a size someone set, gate any run on an
estimate, reward an actor for its sizes, or estimate with several agents
at once (§7).

## 2. Decisions

The debate's round-2 consolidation left eight questions for the owner;
the owner's comments answered three (the `needs-clarification` outcome,
what implement does with an unclear or XL issue, and that races with the
owner do not matter). This design settles the rest:

| Question | Decision | Why |
|---|---|---|
| Rubric form | Qualitative tiers define the sizes; the estimate must list its factors (§3) | Counted bands invite the false precision of line counts, and nobody has calibrated them. The lists are where two estimates that disagree show why |
| Run kind | A new `estimate` role | It has its own policy (it may set a size, nothing else), its own outcomes and prompt. A flag on discuss would add an exception to every discuss check |
| Actor flag | `--actors`, milestone run's `actor[:model]=N` grammar, without the family rule | The owner's spelling and grammar. Estimates are not verified, so a single-family pool is valid |
| Provenance | The server writes one structured comment per estimate, plus the run's outcome | A comment outlives purged run records, and is where the owner reads it. No new store |
| Backlog | Without issue arguments, `estimate` sizes at most `--max` (default 10) issues | A first run over a large backlog should be a choice, not a default |
| Calibration snapshot | Current open issues only, for now | Pinning a run to a historical commit needs server support that no run has (discuss-like runs take the default branch head). Filed for later (§7) |
| Owner race | Documented, not prevented | The owner does not size issues while a sizing run is active (2026-11-09) |
| Replacing a size | Never by an agent | A size someone set is kept; only the owner changes one |

## 3. The sizing guide

The guide is a prompt template, `internal/runs/prompts/sizing.md`
(`{{template "sizing" .}}`), rendered into the prompts of implement,
estimate and plan runs, so that every fam bottown hosts gets it, not only
this repository. A new `docs/reference/sizing.md` points to it as its source,
like the fam guide. Its version is the first 12 hex digits of the SHA-256
of the template's text, computed at build time, so it changes whenever
the text does and nobody has to remember to bump it. The label
descriptions in `default.toml` become one-line summaries of the tiers.

The guide's text, which part 1 (§6) copies into the template:

> **Size the whole issue** as filed, with its discussion: the change, its
> tests and its docs, for one implementer. Read the issue and the code it
> names; do not design the patch, and do not estimate from how many lines
> you expect to write. Routine tests and docs do not raise a size.
>
> **Factors.** List each, briefly, before you pick a size:
>
> - **Boundaries:** the contracts the change crosses or adds, where each
>   side has obligations to the other: CLI and server, server and Forgejo,
>   a run and its policy, a stored record's format, a prompt an agent
>   follows, the web UI and its API. A call through an existing contract
>   that keeps its meaning is not one; a contract that gains ordering,
>   retries, partial failure, ownership of state or compatibility with old
>   data is.
> - **Behaviours to define:** the cases the change must decide, as the
>   issue states them or plainly implies: each a row of the test table
>   you would expect, not a branch of the code you would write.
> - **Novelty:** the existing pattern the change follows (name it), or
>   that it follows none.
> - **Open questions:** what the implementer must decide that the issue
>   does not, and what it would ask the owner.
>
> **Sizes.** The highest tier any factor reaches is the size.
>
> | Size | Boundaries | Behaviours | Novelty | Open questions |
> |---|---|---|---|---|
> | **S** | none new; one place changes | a few, obvious | an existing pattern, applied once | none |
> | **M** | one, or several through a pattern that already spans them | a handful, each clear | an existing pattern, adapted | minor, with an obvious answer |
> | **L** | several, with new obligations between them | many, some interacting | a new mechanism next to existing ones | a few design choices the implementer makes and explains |
> | **XL** | many, or one that is new and central | too many to list up front | a new subsystem or workflow | a design is needed before the code |
>
> Counts, as rough guidance until calibration (§5) sets them: one boundary
> for M, two to four for L, more for XL; up to three behaviours for S, up
> to eight for M.
>
> **Uncertainty is not a size.** When you cannot size the issue because
> it does not say what done is, or contradicts itself, the outcome is
> `needs-clarification` with your questions, not XL. When you can size it
> but are unsure, give the size with `low` confidence and say what would
> change it.
>
> **Record:** the size (or `needs-clarification`), the deciding factor in
> one sentence, the four factor lists, your confidence (`high`, `medium`,
> `low`), and the scope you sized, with any assumptions: two estimators
> who sized different scopes are not disagreeing about the rubric.

## 4. How sizes are set

### 4.1 The `issue_size` operation

One server operation, modelled on `review_submit` (`opReview` in
`internal/server/forge.go`): `POST /v1/forge/issue-size`, the MCP tool
`issue_size`, and the agent CLI `bottown issue size`. Its input:

```
size:            S | M | L | XL | needs-clarification
deciding_factor: one sentence
boundaries, behaviours, novelty, questions: lists of short strings
confidence:      high | medium | low
scope:           the scope sized, with assumptions
```

The CLI takes `bottown issue size SIZE --file F|-`, the rest as TOML or
JSON in the file, so an agent writes one document.

- **Who may call it:** an implement run on its own issue, and an estimate
  run on its issue (`requireRole`, and the issue is the run's). Plan runs
  size in the plan file's `size` field, as today, and do not call it. The
  owner outside a run may call it too, for tests and for hand-written
  estimates. The proxy's `issue-labels` deny rule stays: this operation
  is the only way an agent sets a label.
- **Already sized is a success:** the server reads the issue's labels; if
  any `size/*` label is present it changes nothing and returns
  `already-sized` with that size, so a retry, or a run that raced the
  owner, is harmless. Its estimate is still posted as a comment, marked
  as not applied, since a disagreement with the existing size is data.
- **What it writes,** in this order:
  1. One comment on the issue, rendered by the server from the input, so
     that its format is the server's and stays parseable:
     a short human summary, then a fenced block:

     ````
     ```bottown-estimate
     version = 1
     guide = "3f9a0c1b2d4e"
     run = "r-71df03f7"
     actor = "claude"
     family = "anthropic"
     role = "estimate"
     sha = "c21814e…"
     size = "L"
     applied = true
     confidence = "medium"
     ```
     ````

     For `needs-clarification`, the summary is the questions, addressed
     to the owner.
  2. The label: `size/<S>`, or for `needs-clarification` the
     `needs/clarification` label (new in `default.toml`, not exclusive;
     the owner removes it when the issue is clarified).
  3. The run's outcome (`recordOutcome`): `sized`, `already-sized` or
     `needs-clarification`.

  The steps can fail separately. The comment carries the run's ID, so a
  retry that finds this run's comment reuses it rather than posting a
  second one, and then applies the label; a label that is missing after a
  comment says `applied = true` is visible and harmless, and the next call
  fixes it. The operation re-reads the labels just before writing, and
  holds a server-side lock per issue while it writes, so two bottown runs
  sizing one issue at once apply one size; the owner adding a label at
  the same moment is not guarded against (§2).

### 4.2 Implement runs

When the issue has no `size/*` label as the implement prompt is rendered
(every round, resumes included), the prompt gets a **Size the issue**
section, with the guide: before editing anything, size the issue and
record it with `bottown issue size`. A size set later in the run is still
recorded; the comment's time against the run's first commit shows it was
late. When the issue is already sized, the section is absent.

- **Nothing gates on it.** A missing or failed estimate does not fail the
  run, block `pr create`, or count against the implementer. A gate would
  push agents to size in hindsight, from the patch they wrote.
- **Needs clarification:** the implementer records the outcome with its
  questions, then still implements, on the assumptions it states in the
  pull request's description, for the owner to review (2026-11-09). The
  prompt's closing rule, "If the issue is unclear … comment and stop",
  narrows to being blocked: unclear but workable issues proceed.
- **XL:** the implementer comments a plan that splits the issue into
  pull requests, implements the first, and opens it with `--part`. The
  owner starts the next implement run for the next part. (`milestone run`
  will do the same for an XL child; that is out of scope here.)

### 4.3 `bottown estimate`

```
bottown estimate [ISSUE...] --actors SPEC [--max N] [--timeout D] [--repo R]
```

An owner-only host command that starts one estimate run per issue and
prints, per issue, the outcome, the size and the deciding factor.

- **Which issues:** the ones named; without any, the open issues with no
  `size/*` and no `needs/clarification` label, excluding pull requests
  and milestone epics, oldest first, at most `--max` (default 10). Named
  issues that are already sized are skipped, saying so, unless named with
  `--again`, which posts a new estimate without changing the label: that
  is how the owner collects a second opinion before panels exist.
- **Actors:** `--actors` takes milestone run's pool grammar,
  `actor[:model]=N` with N a number or `*`, parsed by the entry parser
  factored out of `agents.Parse` without its other-family rule. N is the
  actor's concurrent runs (default 1); issues go to the next actor with a
  free slot, so a pool of several families spreads them. Required.
- **The run:** the `estimate` role, a new constant in
  `internal/policy/run.go`. Like a discuss run it reads the default
  branch's head (recorded as its SHA), in a read-only worktree, and cannot
  push, open pull requests or review. It may call `issue_size` on its
  issue, and comment on it. Its prompt, `internal/runs/prompts/estimate.md`,
  is the guide, the issue and its discussion, and one task: size it and
  call `issue_size` once. It runs as a batch run (like a debate's), with
  a default `--timeout` of 15 minutes. A run that ends without calling
  `issue_size` is failed, and the summary says so.

## 5. Checking that estimates converge

Not built in this issue (§7), but the design must make it possible, and
it does: every estimate is a comment with a parseable block (§4.1), and
`estimate --again` collects independent second estimates without
touching labels. The protocol, for the calibration issue:

- **A frozen set** of about 20 open issues, stratified so that each size
  is represented (an M-heavy set flatters agreement), each estimated
  independently by at least three families, with the existing label and
  other estimates hidden from the prompt.
- **Measures:** exact agreement and agreement within one tier, pairwise;
  a weighted ordinal statistic (quadratic κ or Krippendorff's α); each
  family's signed bias; and test-retest on a subset by the same actor.
  Every two-tier gap is read by hand: compare the factor lists and the
  stated scope first, since the debate's own dog-food showed most of its
  spread came from sizing different scopes.
- **Reliability is not validity.** Agreement says the guide is applied
  consistently; whether a size predicts the work is the estimate against
  measured table, read once enough estimated issues have landed.
- **When the guide changes,** its version changes (§3), and the set is
  estimated again.

The targets (the debate proposed 65–75% exact, 90–95% within one tier,
κ or α of 0.6–0.7) are set by the calibration issue from its first run,
not here.

## 6. Plan

This is one issue, landed in three pull requests, each `Part of #439`
until the last; not a milestone. The parts depend on each other in a
line, so a milestone's DAG and its parallel runs buy nothing, and each
part is a single implement run.

1. **The guide:** `internal/runs/prompts/sizing.md`, its version, its
   inclusion in the plan prompt (in place of "Size each issue for one
   implement run"), `docs/reference/sizing.md`, and the label
   descriptions. Size S.
2. **`issue_size` and implement-time sizing:** the operation, its MCP
   tool and agent CLI, the `needs/clarification` label, the comment block
   and its parser, the implement prompt's section and its narrowed
   "unclear" rule, tests for each outcome and for a retry. Size M.
3. **`bottown estimate`:** the `estimate` role and its policy, the prompt,
   the pool entry parser, issue selection, the batch runs and the summary,
   with e2e coverage of one estimate. Size M, medium confidence: L if the
   new role needs more than a name in the web UI's run filters.

Dog-food, sized by the guide in §3, as the owner asked: the whole issue
is **L**, medium confidence, scope as in this section (panels and
calibration excluded). Deciding factor: three contracts change together
(an operation agents call, its comment format that later tools parse, and
a new run role), each following an existing pattern (`review_submit`,
the debate's batch runs). Boundaries: agent CLI and MCP to server, server
to Forgejo labels and comments, the run role in policy, the comment
format. Behaviours: sized, already sized, needs clarification, retry
after a partial write, late estimate, XL split, `--again`, selection
without arguments. Novelty: `opReview` for the operation, debate's batch
runs for `estimate`. Open questions: none blocking. This agrees with the
debate's estimates at matched scope (agy L and codex L, without panels).

## 7. Later, filed as issues

- **Estimation panels** (#441): several agents estimate the same issue
  independently, the CLI aggregates (estimators do not write labels), and
  a disagreement beyond one tier runs a short reconcile round, giving a
  size and a rough design.
- **Calibration** (#442): the protocol in §5, its targets, and the guide's
  numeric guidance revised from its results.

Later still, and not filed until the above report: estimates on
historical issues pinned to the commit they were filed against, and
splitting the stats' estimate against measured table by the size's
source (owner, planner, implementer, estimate run).
