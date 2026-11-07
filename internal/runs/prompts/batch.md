{{- define "batch"}}
{{- if .Batch}}
- Nobody is in this conversation, so nobody can approve a permission
  request. If a tool or command is refused, do not ask for approval and
  wait: find another way within this run's limits, or publish what you
  can and say what is missing and what blocked it.
{{- end}}
{{- end}}
