# Activity

`internal/activity` derives what happened in a repository over a time
window, for the web UI's timeline and statistics views: both call
`activity.Load` (through `webui.Server.ComputeActivity`) rather than each
deriving on its own, and the browser shows what it returns. It keeps no
store: every fact is read again from the server's run records and the
forge, apart from an in-memory cache of comments and closed PRs' reads
(see Reads below).

- **Runs** that overlap the window, purged ones included, each with its
  thread key and its usage. A run's usage is reported when the harness
  gave tokens or a cost; a figure it did not give is nil, never zero, so
  the views can say "n of m runs reported".
- **Threads.** `activity.Group` groups runs into flow threads, and History
  groups them with it too: implement and verify runs for one issue and PR
  share a thread, each PR of an issue is its own, implement runs before
  the PR join its thread, and discuss and retro runs stand alone. The
  window's threads are those with a run that overlaps it, whose PR was
  created, updated, merged or closed in it, or is open, or with a stop in
  it or an owner wait that overlaps it. Each has its title (the issue's,
  else the PR's), its PR's created, merged and closed times, its implement rounds (`REQUEST_CHANGES`
  reviews) and check rounds (commits whose flow `bottown/e2e` status
  failed), as the flow counts them.
- **Checks.** Each flow `bottown/e2e` status on the PR's commits
  (`GET /v1/forge/pr/gate`, `checks`) ends at the status's time; its start
  is derived, that time less the duration the description ends with
  ("in 4m12s", "after 1m03s"), and absent when there is none.
- **Stops and owner waits.** The flow's stop comments
  (`**bottown flow: <reason>**`, written and parsed in `internal/flow`)
  are typed stop reasons, in the thread of the PR they name, else of the
  PR the issue's work belonged to then. Each leaves the thread waiting on
  the owner until its next run, the PR's merge or close (the issue's close
  for a thread with no PR), or the next stop, whichever is first; still
  waiting when none has happened. A `merged` stop, written when the PR was
  merged (or the issue closed) while the flow ran, leaves no wait. The flow's
  announcements of a round it starts share the prefix but are not stops.

**Reads.** Load reads the run list (all of it, so threads group as History
groups them and a wait that began before the window ends where it did),
the PR list and the issue list once. It then reads each PR of the window's
threads once and its gate once, and the repository's issue comments, where
the stops are, in one read (`GET /v1/forge/comments?since=`): every
comment the first time, then only those created or edited since the latest
the cache holds, less a minute. All of the comments count, since a wait may
have begun before the window, even before the flow's first run. That is
4 + 2 × PRs calls, for a 7-day window or any other; after the three lists
they run eight at a time. The server reads the comments from Forgejo's
repository-wide list page by page, with both `page` and `limit` (Forgejo
ignores `limit` without `page`, and caps a page at its maximum), until a
page is empty or brings nothing new. The web UI keeps the comments in
memory, and the reviews and gates of merged and closed PRs as of the PR's
`updated` time, reading one again only when that has changed. An open
PR's are read on every load: the flow's check statuses land on its commits
without updating it. A late review or status on a closed PR shows once
something updates the PR. A comment deleted from the forge stays in the
cache, since the comment read lists only new and edited ones. The cache
only saves reads, and a restart empties it. A failed read leaves its facts
out (for the comments, those not yet cached) and is named in `incomplete`,
sorted. The statistics reuse the issue list the activity read, and read
the merged PRs' diffstats (`GET /v1/forge/pr/diffstat`), eight at a time,
once each (kept in memory too). The server counts a diffstat with `git
diff --numstat` in Forgejo's repository, from the PR's merge base to its
head, the range Forgejo's diff shows, and parses Forgejo's whole diff only
when the repository or those commits are not there.

**Timeline.** `GET /api/timeline?since=&until=` (RFC 3339; `since`
defaults to six hours before `until`, and `until` to now, which leaves the
window `open`) lays the activity out for the web UI's Timeline tab
(`webui.BuildTimeline`), clipping every interval to the window:

- **Actor lanes:** each actor's runs packed into sub-rows so that none
  overlap, and its `busy` seconds, the union of its runs, interactive ones
  included.
- **Flow lanes:** each thread's `segments` in time order: its runs, its
  checks (a check without a duration is a zero-length segment at its end;
  a flow's checks still running, from the host's live check records, run
  to now), its owner waits with their stop reason, and `overhead` for each
  gap between the first and the last of those that none covers (sync
  waits, mirror fetches, waits for a busy actor; the data to tell them
  apart is only in the flow's local log). Each lane has the seconds spent
  on agents (the union of its runs), e2e, waiting on the owner and
  overhead.
- **Concurrency:** the number of batch runs (not interactive) live at each
  step, its peak and its mean over the window.

A run, check or wait still going is `live` and ends at the server's now;
in an open window the view grows it with the clock.

**Statistics.** `GET /api/stats?since=&until=` serves the web UI's Stats
tab (`webui.ComputeStats`, `internal/stats`), built on the same activity
derivation plus the issues' size labels and milestones and the merged PRs'
diffstats. Its window parses as the timeline's, but `since` defaults to thirty
days before `until`. It follows the ground rules of #309: every number
carries its coverage, missing data stays missing rather than becoming
zero, and tokens are never added up across model families. A cold load
reads every comment, and every PR in the window, from the forge and can
take tens of seconds: the tab shows a spinner until the first answer, and an
"Updating…" spinner in its header while a refresh runs over the numbers
already shown.

- **Per issue/PR vector:** its size label and milestone, lead time (issue
  created to PR merged) and cycle time (first run to merged), agent time by
  run kind, implement and check rounds, verifier rejections by verifier
  family, failed runs by cause, the owner's observable touches (interactive
  runs and flow stops), reported usage by model family with "n of m runs
  reported", and the measured diff of a merged PR (added and deleted lines
  and files touched, lock files excluded, `internal/diffstat`).
- **Breakdowns:** the vector grouped by implementer actor, implementer
  family, verifier family, pair, size and milestone; each cell gives its
  n and the median, minimum and maximum, never a mean, and never a ranking.
- **Size table:** each size label against the measured buckets (S ≤ 20,
  M ≤ 100, L ≤ 400 changed lines, XL above), for merged PRs with a diff.
- **Milestones:** each milestone's burndown (open over time) and burnup
  (closed versus total scope over time) from its issues' created and close
  times, weighted by size label when every issue has one and by count
  otherwise, so scope added late shows as a rising total.
