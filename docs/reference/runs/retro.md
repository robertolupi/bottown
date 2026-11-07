# Retro runs

`bottown run retro -a ACTOR [-I] ISSUE` starts a run that writes a
blameless retrospective of an issue's flow from bottown's records. The flow
of issue N is every run whose issue is N or whose branch is `issue/N`, and
every run on one of the issue's pull requests (those Forgejo has from
`issue/N`, open or closed, or that a run's record ties to that branch),
such as a discuss run on the PR: its implement, plan, discuss and verify
runs, live or ended, purged runs' digests included; earlier retros too, but
not the retro itself.

N may be a pull request. One from an `issue/M` branch stands for issue M's
flow: the evidence is M's, and the prompt says the target is PR N of issue
M. One from another branch is a flow of its own: the runs on that pull
request or its head branch. Either way the retro is posted on N. The
launcher's selection of runs and the server's cross-family check below use
the same definition of the flow (`server.RetroFlow`).

Before the agent starts, the launcher gathers that evidence with the owner
key, since a run key may not list runs or read the audit log, into
`$TMPDIR/retro/` in the run's scratch directory:

- `README.md`: the runs, oldest first, at a glance. When the evidence
  visibly lacks runs, it starts with a warning that the evidence looks
  partial, which the prompt repeats: verify runs but no implement or plan
  run, or a pull request whose own last commit (its head, or the first
  parent below the merges, such as the flow's "Merge main into" commits)
  is by an author with no implement or plan run in the evidence, or
  commit statuses it could not read. Each run counts its policy denials
  and, apart, its harness denials: the transcript's `error` lines that
  start with `denied:`, tool calls the harness refused before the policy
  saw them. Below the runs, each of the flow's pull requests lists every
  commit status (context, state, time, description, creator) at its
  commits and at the heads its verify runs saw, oldest first, and flags a
  context with more than one status at one commit (checks that overlapped
  or ran again), saying so when they include a success and a failure.
- `runs.json`: each run's server record and digest (summary, usage,
  observed failure), the launcher's local record if this checkout launched
  it (exit code, stop request, notes, handover; never the key), its
  policy denials and rejections, and its harness denial count.
- `transcripts/<id>.jsonl`: the run's log, as one `runs.LogLine` JSON line
  per message, tool call, tool output or status event, the format the web
  UI's log view uses for every harness, with each text field cut at 4000
  bytes. Interactive runs have no log, so they have no transcript.
- `prompts/<id>.md`: the run's goal prompt.

If gathering fails, the prompt says why, and the run works from the forge.
The agent reads the issue, the PR threads, reviews and commit statuses with
the agent CLI, and posts the retro as a comment on the issue. It may file
follow-up issues. Like a discuss run, it cannot push, open pull requests or
review, and its worktree is detached at the default branch's head. Unlike a
discuss run, its file edits are not refused: it may draft a design note or
a proposal in the worktree, and the launcher keeps a retro's worktree when
it has uncommitted changes, even if the run succeeded. In the web UI's run
history a retro is a standalone row, as a discuss run is.

As with verify, the server refuses a retro by an actor of the same family
as one of the flow's implement or plan runs unless `--allow-same-family`
is given, and records the override on the run. Retros start only from
the main checkout: `run start` in an actor worktree does not take them,
since the actor's credential cannot gather the evidence.

## Windowed retros

A windowed retro (#383) looks back on a window of the default branch
instead of one issue's flow, and writes one wiki page instead of a
comment:

```
bottown run retro -a ACTOR [-I] --since WINDOW
```

The window is every commit on the default branch after its start,
first-parent, up to the default branch's head when the run is created.
WINDOW is the start:

- `git:REF`: after REF, a SHA, tag or other revision that is on the
  default branch's first-parent line. A commit an issue branch merged in
  is refused (409), as is one with nothing after it.
- `last`: after the head the newest windowed retro covered: of the wiki's
  `YYYYMMDD-<sha>-retrospective` pages whose SHA is on the default branch,
  the one furthest along it. Windows that follow each other with `last`
  neither overlap nor leave gaps.
- `YYYY-MM-DD`, `Nd`, `Nw`: after the default branch as it was at the
  start of that day (UTC), or N days or weeks before now: its newest
  first-parent commit committed before then.

The server resolves WINDOW in Forgejo's own repository
(`server.retroWindow`), so the start does not depend on the launcher's
checkout, and records it on the run (`window`: the start as given, the
base commit it resolved to, and the page), with the head as the run's SHA.
The launcher prints both, and `run ls` shows the run's target as
`<base>..<head>`.

The evidence is that of a retro, for every flow in the window: the pull
requests that the window's first-parent commits merged (by their merge
commit), each standing for its flow (`server.RetroFlow`, so a pull request
from `issue/M` brings issue M's whole flow, its other pull requests
included), each flow once. The README starts with the window and those
pull requests, oldest first, then the runs and commit statuses as for a
retro. Pull requests that have not merged are outside the window: they are
not yet a lesson.

The prompt asks for the retro across the flows: the window, the flows at a
glance, what went well and wrong, the root causes seen in more than one
flow with how many and which (saying so plainly when nothing repeated),
which lessons of the newest earlier windowed retro this window shows fixed
or wrong, and follow-ups. The agent writes it to the wiki page
`YYYYMMDD-<short head>-retrospective` (the day the run was created, in UTC,
and the head's shortest unique abbreviation in Forgejo's repository), the
only page it may write (`policy.DecideWikiRetroPage`); it comments on no
issue, and the launcher posts no handover comment. It may file follow-up
issues. The page is a record, never binding, like the rest of the wiki.

Windowed retros are the owner's: the server refuses one from an actor's
own key (403). They have no family rule, since a window's implementers are
usually of every family, so `--allow-same-family` is refused with
`--since`. Two windowed retros that would write the same page cannot be
live at once.
