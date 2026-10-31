---
description: Start a bottown {{.Kind}} run in this actor worktree, then do it
argument-hint: <{{.Target}} number>
allowed-tools: Bash({{.Bottown}}:*), Bash(bottown:*)
---
<!-- {{.Marker}} -->

!`{{.Bottown}} run start {{.Kind}} $ARGUMENTS`

The output above comes from bottown. If it is an error (for example, this is
not an actor worktree, the worktree is busy or has uncommitted changes, or the
{{.Target}} number is missing), tell the user what it says and stop.

Otherwise it is your goal prompt for this run: follow it to the end,
including `{{.Bottown}} run end`.
