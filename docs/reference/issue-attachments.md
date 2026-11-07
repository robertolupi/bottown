# Issue attachments

Issue attachments reach harnesses as local files. An issue read lists the
files on the description and on each comment (`attachments`: id, name,
size, comment id), and `GET /v1/forge/issue/attachment?number=N&id=ID`
streams one file's bytes. The server fetches each file from Forgejo with the
actor's token, and only when the file is on an issue the key can read, so a
run stays within its repository and the agent needs no Forgejo login.
External links are not listed. The files belong to the run:

- **Where.** A launched run keeps them in
  `.bottown/runs/<id>.attachments/`. An actor worktree's run keeps them in
  `<gitdir>/bottown/attachments/`. Neither is in the checkout. Each file is
  `<id>-<name>`, with the name reduced to `[A-Za-z0-9._-]`, so two files
  with the same name get different paths. The directory is 0700, and files
  over 100 MiB are skipped.
- **When.** Starting a run downloads the files before the prompt is
  rendered. Each `issue show` in the run fetches files added since. A launched
  run's directory is removed when its launcher returns, however the run
  ended, and by `run gc` if the launcher died. The launcher saves the run's
  record, as preparing, before it downloads anything, so `run gc` finds
  the run, as dead, even when the launcher died during startup. An actor
  worktree's directory
  is removed when `run end` ends the run, when `run start` or `run end`
  recover an interrupted run (before the record is finalized, so a failed
  removal is retried), and before the next run downloads.
- **What the harness sees.** If there are attachments, the goal prompt has
  an `## Attachments` section with one line per file: its local path,
  original name, content type, size, and whether it is on the description
  or a comment. A file that could not be downloaded gets a line saying why,
  and a failed listing gets a line too. Neither stops the run or hides the
  issue text. `issue show` prints the same lines. `issue show --json` puts
  `path`, `content_type` and `error` on each attachment. Outside a run,
  `issue show` lists attachments without downloading them, and `--download
  DIR` saves them to DIR, which the caller then owns.
- **What stays out.** Prompts, `issue show`, comments and digests carry
  paths and metadata, never file contents. A harness that opens an image
  logs it base64-encoded, so a batch run's log is written with data URLs and
  base64 runs of 256 or more characters replaced by `[N encoded bytes elided
  by bottown]`. The Live Runs tail applies the same rule when it renders a
  log, including logs written before this change.
