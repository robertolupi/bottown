{{- define "attachments"}}
{{- if or .Attachments .AttachmentsError}}

## Attachments

Files attached to #{{.AttachmentsOf}} and its comments are downloaded for
this run. Open them with your own tools (read an image to see it); they are
removed when the run ends. `{{.Bottown}} issue show {{.AttachmentsOf}}` lists
them again and downloads any added since.
{{range .Attachments}}
- {{.}}
{{- end}}
{{- if .AttachmentsError}}
- The attachments could not be listed: {{.AttachmentsError}}
{{- end}}
{{- end}}
{{- end}}
