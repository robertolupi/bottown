# Closing keywords on pull requests

`pr create` ends the description with `Closes #N`, or `Part of #N` with
`--part`. Forgejo reads a `close`/`fix`/`resolve` or `reopen` keyword
before an issue reference (`#N`, `owner/repo#N`, or a link to an issue) in
a PR's description, comments or reviews as what merging the PR does to
that issue, and applies only the latest one per issue (quirk 8 in
[bootstrap.md](../history/bootstrap.md)). So the server refuses, with 400 and
the matched words, a `comment` on a pull request and a `review submit`
whose body holds one outside code; on an issue, where the keywords do
nothing, `comment` posts it. It parses the body with goldmark, the
Markdown parser Forgejo uses, so code spans and fenced or indented code
blocks are code exactly where Forgejo has them, inside list items and
block quotes too. Where Forgejo's reading is looser it refuses rather
than misses: emphasis or HTML tags around the keyword
(`**Reopen** #N`), link text, HTML blocks and backslash-escaped
characters still count. The implement and
verify prompts say so. Writes through `bottown api` are not checked, and
the flow's own round and stop comments go on the issue.
