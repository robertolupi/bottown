# Critical discuss runs

A critical discuss run is a red team: a discuss run with a fixed
adversarial stance, for attacking a design before it is built rather than
asking it a question. Start one from the main checkout or an actor
worktree:

```
bottown run discuss -a codex --critical 12 [FOCUS]
bottown run start discuss --critical 12     # or: /bottown-discuss-critical 12 [FOCUS]
```

`bottown harness setup` installs `/bottown-discuss-critical` for every
harness, next to `/bottown-discuss` (kimi: `/skill:bottown-discuss-critical`,
codex: `$bottown-discuss-critical`). The words after the number are the
focus of the critique, such as one part of the design or one risk; without
them the agent attacks the whole design.

The goal prompt is the discuss prompt with a red-team section: steelman the
design, then attack its assumptions, cost, failure modes, scope and what it
makes harder later, and the cheapest alternative; rank the findings by
severity; give a verdict (build it, rework it, or do not build it), saying
plainly if it should not be built; no praise for its own sake. Everything
else is a discuss run's: the run cannot push, open pull requests or review,
and it answers in one comment on the issue.

The critique comes from another model family than the issue's author (its
poster on the forge), as a verify run's does: the server refuses a critical
run by an actor of the author's family unless the owner gives
`--allow-same-family` (`run start` cannot), and records the override on the
run. An issue whose author is not an actor, such as the owner, has no
family to differ from. The server signs every comment the run posts with
`_bottown critical discuss run <id> (<actor>, <family>)_`, after a
same-family banner when the override was used, and the handover says the
run was critical and which family wrote it. `run show` marks the run
`critical`. Plain discuss runs keep no family rule.
