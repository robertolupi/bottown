---
{{if .Critical -}}
description: Start a critical bottown discuss run in this workspace, to red-team an issue's design without implementing it
{{- else -}}
description: Start a bottown discuss run in this workspace, to review and discuss an issue without implementing it
{{- end}}
---
<!-- {{.Marker}} -->

1. Take the issue number the user gave after `/bottown-discuss{{if .Critical}}-critical{{end}}`, and any
   words after it: they are {{template "discuss-words" .}}. If there is no number,
   ask for it and stop.
2. In this workspace, run `{{.Bottown}} run start discuss {{if .Critical}}--critical {{end}}<number>`, with
   the number as digits only (`12`, not `#12`: the shell reads `#` as the
   start of a comment). Do not pass the {{template "discuss-word" .}} to the command.
3. If it fails (for example, this is not an actor worktree, the worktree is
   busy or has uncommitted changes{{if .Critical}}, or this actor is of the issue author's
   model family{{end}}), tell the user what it says and stop.
4. Otherwise its output is your goal prompt for this run: follow it to the
   end, including `{{.Bottown}} run end`, with the user's {{template "discuss-word" .}} as
   {{template "discuss-words" .}}.
