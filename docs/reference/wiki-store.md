# The wiki store

A repository's Forgejo wiki is an idea workspace (#311, #315), upstream of
issues. **It is never binding:** nothing reads it as state, and once an idea
becomes an issue or a design document, that is the source of truth. The
wiki is reached only through bottown's wiki store
(`internal/server/wiki.go`):

```
bottown wiki list
bottown wiki read PATH              # content to stdout; its blob SHA to stderr
bottown wiki put PATH [--base SHA] [--file F|-] [-m MSG]
bottown wiki rm PATH --base SHA [-m MSG]
bottown wiki generate [--check]
```

PATH is a file in the wiki (`Idea.md`, `reviews/12-x/r1-claude.md`) or a
page's slug (`Idea`). A slug also finds a page Forgejo stored as `Idea.-.md`,
which is how its web UI and API store a hyphenated title. In a run or an
actor worktree the commands act as the agent; anywhere else on the host,
as the owner (`--repo` outside a checkout).

- **No checkout for agents.** The API proxy denies Forgejo's wiki API
  (`/repos/*/*/wiki/**`), and the git proxy the wiki's repository
  (`*.wiki.git`), to everyone but the owner outside a run (rule
  `wiki-via-store`). The store reads Forgejo's own bare repository of the
  wiki, so it is never behind. It writes by committing there and pushing to
  Forgejo as the writer, so Forgejo's page history shows an ordinary
  commit, carrying a `Bottown-Run:` trailer when made in a run. A wiki that
  does not exist yet is created through Forgejo's API on its first write
  (a push to it would be refused). The creation's placeholder `Home` page
  does not survive that write, unless the owner edited it in between; pages
  the owner added in between stay too.
- **Puts are compare-and-swap.** `read` prints the file's blob SHA, and
  `put --base SHA` commits only if the file's text is unchanged since.
  Otherwise it fails with a conflict (`wiki_conflict`): read again and
  re-apply the edit. Changes to generated blocks alone are no conflict.
  Without `--base` a put creates the file, and fails if it exists. A push
  that loses a race, for example with the owner saving in the web UI, is
  rebuilt on the new head and retried, so it never overwrites that edit.
- **Deletes are too.** `rm PATH --base SHA` removes the file under the same
  compare-and-swap (a missing file is a 404), and regenerates the whole
  wiki in the same commit: backlinks drop the deleted page, and links to
  it show on the health page as broken. It is a write under the wiki
  rules, so by default the owner's alone. `Wiki-Health.md` cannot be
  deleted, since the generator recreates it. The audit log records
  `wiki_rm` entries, as it does `wiki_put`.
- **Path-scoped policy.** `policy.WikiRules` grants reads and writes by
  principal (the owner, an actor outside a run, or a run's role) and path,
  first match wins. The default: the owner reads and writes everything;
  actors and every run role read. #311 (garden runs, top-level pages) adds
  its rules ahead of these; a debate's round files have their own
  (`policy.DecideWikiRound`, see [debates](debates.md)), and so does a
  windowed retro's page, which its run alone writes
  (`policy.DecideWikiRetroPage`, see [windowed retros](runs/retro.md#windowed-retros)).
- **Generated blocks.** Text between `<!-- bottown:<name>:generated -->`
  and `<!-- /bottown:<name>:generated -->` is bottown's. Blocks do not nest,
  and names come from the registry in `internal/wiki` (`backlinks`,
  `health`). A put discards whatever it is sent inside markers and fills
  the blocks again, so writers never maintain them. It refuses mangled
  markers and unknown names.
  - `backlinks`, at the end of every page except the health page and
    Forgejo's `_Sidebar`/`_Footer`, lists the pages linking to it.
  - `health`, on `Wiki-Health.md` (created when missing), lists broken
    links, pages stored as `Name.-.md`, unknown block names and pages
    whose markers do not pair. The generator leaves those pages alone.
  - Links are read from page text outside blocks, in the forms Forgejo
    resolves to a page (checked on Forgejo 15.0.11): `[t](Page)`,
    `[t](./Page)`, `[t](Page.md)`, `[t](/org/repo/wiki/Page)`, `[[Page]]`,
    `[[Page Name]]` (spaces become dashes) and `[[Text|Page]]`. A link to
    `Name` does not reach a page stored as `Name.-.md`: health reports it
    as broken and names the link that works. Files in subdirectories are
    not pages: Forgejo serves them only under `/wiki/raw/`. Their links
    are not read and they get no default blocks, but a block placed in one
    is filled and kept current like a page's, and their markers are
    checked.
- **`generate`.** It walks the whole wiki and regenerates every stale
  block, committing only the files that change, as bottown. Every put does
  the same in its own commit. The owner runs it after editing in the web UI,
  which leaves blocks stale. `--check` writes nothing, lists the stale
  blocks and fails when there are any; anyone who can read the wiki may run
  it. The generator is a pure function of the tree (no clock, no forge), and
  running it on its own output changes nothing.
