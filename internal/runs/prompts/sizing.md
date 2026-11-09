{{- define "sizing"}}
**Size the whole issue** as filed, with its discussion: the change, its
tests and its docs, for one implementer. Read the issue and the code it
names; do not design the patch, and do not estimate from how many lines
you expect to write. Routine tests and docs do not raise a size.

**Factors.** List each, briefly, before you pick a size:

- **Boundaries:** the contracts the change crosses or adds, where each
  side has obligations to the other: CLI and server, server and Forgejo,
  a run and its policy, a stored record's format, a prompt an agent
  follows, the web UI and its API. A call through an existing contract
  that keeps its meaning is not one; a contract that gains ordering,
  retries, partial failure, ownership of state or compatibility with old
  data is.
- **Behaviours to define:** the cases the change must decide, as the
  issue states them or plainly implies: each a row of the test table
  you would expect, not a branch of the code you would write.
- **Novelty:** the existing pattern the change follows (name it), or
  that it follows none.
- **Open questions:** what the implementer must decide that the issue
  does not, and what it would ask the owner.

**Sizes.** The highest tier any factor reaches is the size.

| Size | Boundaries | Behaviours | Novelty | Open questions |
|---|---|---|---|---|
| **S** | none new; one place changes | a few, obvious | an existing pattern, applied once | none |
| **M** | one, or several through a pattern that already spans them | a handful, each clear | an existing pattern, adapted | minor, with an obvious answer |
| **L** | several, with new obligations between them | many, some interacting | a new mechanism next to existing ones | a few design choices the implementer makes and explains |
| **XL** | many, or one that is new and central | too many to list up front | a new subsystem or workflow | a design is needed before the code |

Counts, as rough guidance until the guide is calibrated: one boundary
for M, two to four for L, more for XL; up to three behaviours for S, up
to eight for M.

**Uncertainty is not a size.** When you cannot size the issue because
it does not say what done is, or contradicts itself, the outcome is
`needs-clarification` with your questions, not XL. When you can size it
but are unsure, give the size with `low` confidence and say what would
change it.

**Record:** the size (or `needs-clarification`), the deciding factor in
one sentence, the four factor lists, your confidence (`high`, `medium`,
`low`), and the scope you sized, with any assumptions: two estimators
who sized different scopes are not disagreeing about the rubric.
{{- end}}
