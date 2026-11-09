# The sizing guide

The `size/S`, `size/M`, `size/L` and `size/XL` labels mean what the
**sizing guide** says they mean, so that sizes set by different agents,
or by the owner, are consistent enough to compare. The design, and the
parts not built yet (sizing in implement runs, `bottown estimate`), are
in [the sizing design](../design/sizing.md).

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
  request's description.
- **The labels' descriptions** in `internal/config/default.toml` are
  one-line summaries of the guide's tiers.
