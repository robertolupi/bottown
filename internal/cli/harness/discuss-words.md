{{- /* What the words after the number are, in the *-discuss.md commands: a question, or a critical run's focus. */ -}}
{{define "discuss-word"}}{{if .Critical}}focus{{else}}question{{end}}{{end}}
{{- define "discuss-words"}}{{if .Critical}}the focus of the red team{{else}}the question to discuss{{end}}{{end}}
