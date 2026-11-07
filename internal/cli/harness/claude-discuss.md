---
{{if .Critical -}}
description: Start a critical bottown discuss run in this actor worktree, to red-team an issue's design without implementing it
argument-hint: <issue number> [focus]
{{- else -}}
description: Start a bottown discuss run in this actor worktree, to review and discuss an issue without implementing it
argument-hint: <issue number> [question]
{{- end}}
allowed-tools: Bash({{.Bottown}}:*), Bash(bottown:*)
---
<!-- {{.Marker}} -->

The owner's request, as typed after `/bottown-discuss{{if .Critical}}-critical{{end}}`:

> $ARGUMENTS

1. Take the issue number from the start of the request; the words after it
   are {{template "discuss-words" .}}. If there is no number, ask for it and stop.
2. In this actor worktree, run `{{.Bottown}} run start discuss {{if .Critical}}--critical {{end}}<number>`,
   with the number as digits only (`12`, not `#12`). Do not pass the
   {{template "discuss-word" .}} to the command: it may hold quotes or `#`, which the shell
   would misread.
3. If it fails (for example, this is not an actor worktree, the worktree
   is busy or has uncommitted changes{{if .Critical}}, or this actor is of the issue
   author's model family{{end}}), tell the user what it says and stop.
4. Otherwise its output is your goal prompt for this run: follow it to the
   end, including `{{.Bottown}} run end`, with the owner's {{template "discuss-word" .}} as
   {{template "discuss-words" .}}.
