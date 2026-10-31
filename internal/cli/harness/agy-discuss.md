---
description: Start a bottown discuss run in this workspace, to review and discuss an issue without implementing it
---
<!-- {{.Marker}} -->

1. Take the issue number the user gave after `/bottown-discuss`, and any
   words after it: they are the question to discuss. If there is no number,
   ask for it and stop.
2. In this workspace, run `{{.Bottown}} run start discuss <number>`, with
   the number as digits only (`12`, not `#12`: the shell reads `#` as the
   start of a comment). Do not pass the question to the command.
3. If it fails (for example, this is not an actor worktree, or the worktree is
   busy or has uncommitted changes), tell the user what it says and stop.
4. Otherwise its output is your goal prompt for this run: follow it to the
   end, including `{{.Bottown}} run end`, with the user's question as the
   question to discuss.
