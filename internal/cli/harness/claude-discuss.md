---
description: Start a bottown discuss run in this actor worktree, to review and discuss an issue without implementing it
argument-hint: <issue number> [question]
allowed-tools: Bash({{.Bottown}}:*), Bash(bottown:*)
---
<!-- {{.Marker}} -->

The owner's request, as typed after `/bottown-discuss`:

> $ARGUMENTS

1. Take the issue number from the start of the request; the words after it
   are the question to discuss. If there is no number, ask for it and stop.
2. In this actor worktree, run `{{.Bottown}} run start discuss <number>`,
   with the number as digits only (`12`, not `#12`). Do not pass the
   question to the command: it may hold quotes or `#`, which the shell
   would misread.
3. If it fails (for example, this is not an actor worktree, or the worktree
   is busy or has uncommitted changes), tell the user what it says and stop.
4. Otherwise its output is your goal prompt for this run: follow it to the
   end, including `{{.Bottown}} run end`, with the owner's question as the
   question to discuss.
