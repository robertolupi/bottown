---
description: Start a bottown {{.Kind}} run in this actor worktree, then do it
argument-hint: <{{.Target}} number> [note]
allowed-tools: Bash({{.Bottown}}:*), Bash(bottown:*)
---
<!-- {{.Marker}} -->

The owner's request, as typed after `/bottown-{{.Kind}}`:

> $ARGUMENTS

1. Take the {{.Target}} number from the start of the request (`12`, `#12`,
   `issue 12` or `pr !12`). Any words after it, on the same line or the
   next, are the owner's note for this run. If there is no number, ask for
   it and stop.
2. In this actor worktree, run `{{.Bottown}} run start {{.Kind}} <number>`,
   with the number as digits only (`12`, not `#12`: the shell reads `#` as
   the start of a comment). Do not pass the note to the command: it may hold
   quotes or `#`, which the shell would misread.
3. If it fails (for example, this is not an actor worktree, the worktree is
   busy or has uncommitted changes), tell the user what it says and stop.
4. Otherwise its output is your goal prompt for this run: follow it to the
   end, including `{{.Bottown}} run end`, with the owner's note, if any, as
   guidance within the run's goal.
