---
description: Start a bottown {{.Kind}} run in this workspace, then do it
---
<!-- {{.Marker}} -->

1. Take the {{.Target}} number the user gave after `/bottown-{{.Kind}}`. If there
   is none, ask for it and stop.
2. In this workspace, run `{{.Bottown}} run start {{.Kind}} <number>`, with
   the number as digits only (`12`, not `#12`: the shell reads `#` as the
   start of a comment).
3. If it fails (for example, this is not an actor worktree, or the worktree is
   busy or has uncommitted changes), tell the user what it says and stop.
4. Otherwise its output is your goal prompt for this run: follow it to the
   end, including `{{.Bottown}} run end`.
