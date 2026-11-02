{{- define "message"}}
{{- if .Message}}

## Note from the owner

The owner added this to the command that started this run. Follow it,
within the issue and this run's limits:

{{.Message}}
{{- end}}
{{- end}}
