# Debates

A debate asks several agents the same question about an issue,
independently, and keeps one consolidated answer in the wiki (#315). It
binds nothing until the owner files issues or pull requests from it.

```
bottown debate ISSUE --actors claude,codex,agy [QUESTION... | --question-file F|-]
bottown debate ISSUE --consolidate ACTOR
bottown debate ISSUE --actors claude,codex --consolidate agy [--raw] [QUESTION]
    [--wiki-slug SLUG] [--date YYYYMMDD|today] [--keep]
```

- **Rounds.** `--actors` starts one discuss run per actor (`ACTOR:MODEL`
  works), side by side, in a round. Each answers in its own file and
  cannot read the others' answers. `--consolidate ACTOR`, after the round's
  runs have ended (or alone, for the last round), starts a **consolidate
  run** that merges them. The next round starts with another `bottown
  debate`, once the round before is consolidated; its runs read earlier
  rounds' consolidations, and with `--raw` their answers too. Until it is
  consolidated, a round takes more actors (without a question, or with the
  round's), but not a second answer from one actor.
- **State.** A round is open while any of its discuss runs is active,
  complete when all have ended, consolidated once its consolidation
  exists. The server places each run in its round when the run is created
  (`internal/server/debate.go`), from the wiki and its run records, which
  keep the run's debate, round, question and `--raw`. Debates are the
  owner's: actors cannot start their runs.
- **Files.** The debate is named `[<YYYY-MM-DD>-]<issue>[-<slug>]`:

  ```
  Review-<debate>.md                 the page bottown rewrites per round
  reviews/<debate>/r1-claude.md      one answer per actor and round
  reviews/<debate>/r1-consolidated.md
  ```

  The slug is the issue's one debate (under `--date`), so a retitle does not
  split it; else the first five words of its title, without markdown,
  lowercase and dashed (`` `bottown flow` refactor `` is
  `bottown-flow-refactor`), cut near 40 characters. `--wiki-slug` names it,
  and a request without one, when the issue has several debates, is
  refused. `--date` puts the debate under a day (`today`, or `YYYYMMDD` to
  continue an earlier day's).
- **Written once, stamped.** Round files are created once (a put over one is
  a conflict, and only the owner deletes one), and the store replaces their
  frontmatter with its own: `round`, `actor`, `model`, `run`, `issue`, `sha`
  (the default branch head the run read) and `date`. A consolidated round
  takes no more answers.
- **The page.** The put of `r<N>-consolidated.md` also rewrites
  `Review-<debate>.md` in the same commit (`wiki.ReviewPage`), so its
  history has one revision per round: the latest consolidation, with its
  headings moved below the page's, and one line per round with its date,
  its actors and links to its files under `/wiki/raw/`, since Forgejo
  renders no page for files in subdirectories. Edits to the page last only
  until the next round.
- **Independence, enforced.** `policy.DecideWikiRound` decides round files
  before the other wiki rules: a round's discuss run writes only its own
  answer and reads, of its debate, only that answer and earlier rounds'
  consolidations (and their answers with `--raw`); its consolidate run
  reads the round and those before it and writes only the consolidation;
  anyone else but the owner reads a round once it is consolidated, and
  writes none. Listing hides what a reader may not read. While its round is
  open a discuss run may not comment, react or file issues (rule
  `role-debate-no-comment`), and posts no handover comment; prompts also
  tell it not to look for the other runs' worktrees and logs on the host,
  which bottown cannot prevent.
- **One comment.** The consolidate run's handover comment is the link to
  the page and the round number, without a summary. Any actor may
  consolidate: the answers are already independent, so no family rule
  applies.
- **A run's job is its file.** When a debate run's harness exits cleanly,
  the launcher lists the wiki: if the run's file (`r<N>-<actor>.md`, or
  `r<N>-consolidated.md`) is not there, the run ends `failed` with the
  observed cause `no_output` naming the file, and a consolidate run's
  comment says the round is not consolidated (#391). If the wiki cannot be
  listed, the run's outcome stands, with a warning.
