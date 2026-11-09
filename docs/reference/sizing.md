# The sizing guide

The `size/S`, `size/M`, `size/L` and `size/XL` labels mean what the
**sizing guide** says they mean, so that sizes set by different agents,
or by the owner, are consistent enough to compare. The design, and the
part not built yet (`bottown estimate`), are in
[the sizing design](../design/sizing.md).

- **One source, in the binary.** The guide is a template,
  `internal/runs/prompts/sizing.md` (`{{template "sizing" .}}`), rendered
  into the prompts of the runs that size issues, so every fam bottown
  hosts gets it, not only this repository. Read the guide there; this page
  does not copy it.
- **What it asks.** Size the whole issue as filed, from what it says and
  the code it names, not from the lines the patch would take. List four
  factors (boundaries crossed or added, behaviours to define, novelty,
  open questions), and take the highest tier any of them reaches. An issue
  that does not say what done is gets `needs-clarification` with
  questions, not XL. An estimate records the size, the deciding factor,
  the factor lists, a confidence (`high`, `medium`, `low`) and the scope
  sized.
- **Its version** is `runs.SizingGuideVersion`: the first 12 hex digits of
  the SHA-256 of the template's text, as embedded in the binary. It
  changes whenever the text does, with nothing to bump by hand, so
  estimates can say which guide they followed.
- **Where it is used.** Plan runs: the plan prompt includes the guide, and
  the planner gives each proposed issue a `size` in its block (which
  `milestone apply` sets as the issue's label when it has none), splits
  one that comes out XL, and puts the rest of each estimate in its pull
  request's description. Implement runs on an unsized issue: below.
- **The labels' descriptions** in `internal/config/default.toml` are
  one-line summaries of the guide's tiers. `needs/clarification` (not
  exclusive) marks an issue an estimate found unclear; the owner removes
  it.

## Recording an estimate: `issue_size`

One server operation (`internal/server/size.go`): `POST
/v1/forge/issue-size`, the MCP tool `issue_size`, and the agent CLI

```
bottown issue size S|M|L|XL|needs-clarification --file F|- [--issue N --repo R] [--json]
```

whose file holds the rest of the estimate as TOML or JSON
(`deciding_factor`, `boundaries`, `behaviours`, `novelty`, `questions`,
`confidence`, `scope`; unknown keys are refused, so a misspelt factor is
not dropped). A size needs its deciding factor, confidence and scope;
`needs-clarification` needs its questions.

- **Who.** An implement run, on its own issue only. Outside a run, the
  owner, on any issue (`--issue`, `--repo`). Other runs and actor keys
  outside a run are refused. It is the only way an agent sets a label:
  the proxy still denies the issue labels endpoints.
- **What it writes,** in order, with the actor's token:
  1. A comment the server renders from the input (`internal/estimate`):
     a summary (for `needs-clarification`, the questions, addressed to the
     owner with an @-mention), the factor lists, then a fenced
     `bottown-estimate` block of TOML: `version` (1), `guide`, `run`,
     `actor`, `family`, `role` (the run's kind, or `owner`), `sha` (when
     the run has one), `size`, `applied`, `confidence` and, when not
     applied, `existing`. `estimate.Parse` reads the last such block of a
     comment.
  2. The label, `size/<SIZE>` or `needs/clarification`, when the issue
     has no `size/*` label.
  3. The run's outcome: `sized`, `already_sized` or
     `needs_clarification`. An implement run's outcome is its pull
     request, so there the estimate is recorded only while the run has
     no outcome, and `pr_opened` replaces it.
- **Already sized** is a success: a `size/*` label someone set is never
  replaced. The estimate is still posted, with `applied = false` and the
  existing size, as a second opinion; no label is written, not even
  `needs/clarification`.
- **Retries are safe.** A call finds this run's earlier estimate (a
  comment by the run's actor whose block names the run) and keeps it, not
  posting another and ignoring the new input; if that estimate was
  applied and the issue still has no size label (the label write failed),
  it adds the label. Estimates are written one at a time on the server,
  so two runs sizing one issue at once apply one size. The owner labelling
  the issue at the same moment is not guarded against.

## Sizing in implement runs

When the issue has no `size/*` label as the implement prompt is rendered
(each round, so a resumed run does not size an issue sized since), the
prompt has a **Size the issue** section with the guide: size the issue
before editing, and record it with `bottown issue size`. On a sized
issue the section is absent.

- **Nothing waits on it:** a missing or failed estimate does not fail the
  run or block `pr create`.
- **`needs-clarification`:** the implementer records its questions, then
  implements anyway, on assumptions it states in the pull request's
  description. The prompt's closing rule stops only a blocked run, or one
  on an issue too unclear to implement on any reasonable assumption.
- **`XL`:** the implementer comments a plan that splits the issue into
  pull requests, implements the first, and opens it with `--part`; the
  owner starts a run for each next part.
