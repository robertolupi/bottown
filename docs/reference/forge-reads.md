# Forge reads and pagination

Forge reads through the CLI, MCP, and `/v1/forge` return complete issue and
pull-request lists, issue comments, and pull-request reviews. The server follows
Forgejo pages internally for issues, pull requests, and reviews; the issue
comments endpoint returns all matching comments in one request. Callers do not
supply page numbers. A failed later page fails the read rather than returning
a partial list. PR list review status is the shared review assessment's
decision (`approved`, `changes requested`, `commented`, `needs review`;
[web-ui.md §3.3](../design/web-ui.md#33-state-and-restart)), and
is `unknown` when reviews cannot be read completely. The local UI also follows
pages for its inbox, run views, and issue timeline, retaining its existing
best-effort behavior when a read fails.
