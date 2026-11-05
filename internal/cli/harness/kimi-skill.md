---
name: bottown-{{.Kind}}
description: Start a bottown {{.Kind}} run in this actor worktree, then do it
disableModelInvocation: true
---
<!-- {{.Marker}} -->

The user's request, as typed after `/skill:bottown-{{.Kind}}`:

> $ARGUMENTS

1. Take the {{.Target}} number from the start of the request; any words after
   it are the user's note for this run. If there is no number, ask for it and
   stop.
2. In this workspace, run `{{.Bottown}} run start {{.Kind}} <number>`, with
   the number as digits only (`12`, not `#12`: the shell reads `#` as the
   start of a comment). Do not pass the note to the command.
3. If it fails (for example, this is not an actor worktree, or the worktree is
   busy or has uncommitted changes), tell the user what it says and stop.
4. Otherwise its output is your goal prompt for this run: follow it to the
   end, including `{{.Bottown}} run end`, with the user's note, if any, as
   guidance within the run's goal.
