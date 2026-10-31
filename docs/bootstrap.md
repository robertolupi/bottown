# Bootstrap: from nothing to a provisioned forge

> [!NOTE]
> **Status**: Implemented (2026-10-31), step 1 of bottown. Verified by
> `make e2e` against Forgejo 15.0.11 (LTS).

`bottown up` takes a machine with Docker to a running, provisioned instance in
one command, without clicking anything in a browser. A second run converges and
is otherwise a no-op.

## Shape

```
host                                       container bottown-<instance> (one image)
────                                       ─────────────────────────────────────────
bottown up/down/status ──docker CLI──▶     s6 ─┬─ forgejo        :3000  sqlite, /data/gitea
~/.bottown/<instance>/config.toml              └─ bottown serve  :8080
Keychain "bottown-<instance>"                       ├ reconciler  (forgejo admin CLI + API)
agents ──Bearer btk_… ──▶ 127.0.0.1:<sp>            ├ /healthz  /v1/*  /mcp
git    ──Basic …:btk_… ─▶ 127.0.0.1:<sp>/git/       ├ /forgejo/api/v1/*  (policy proxy)
owner browser ─────────▶ 127.0.0.1:<fp>             └ /data/bottown/secrets.json (0600)
```

- **One image.** `docker/Dockerfile` builds bottown and layers it onto
  `codeberg.org/forgejo/forgejo:15.0.11`. The image's s6 supervises both
  processes. bottown runs as `git`, the same user as Forgejo, so it can run
  `forgejo admin`.
- **The trust root is the Docker socket.** The host gets the owner's
  credentials through `docker exec … bottown admin owner-credentials`, never
  over the network.
- **Forgejo tokens never leave the container.** Every client, the owner
  included, holds a bottown API key (`btk_…`). bottown swaps the key for that
  actor's Forgejo token on each proxied API call and git push. The proxy
  discards client `Authorization`, `Sudo` and cookie headers, and the `sudo`
  and `token` query parameters.

## Instances (blue/green)

| | prod | dev | e2e-* |
|---|---|---|---|
| ports (Forgejo/bottown) | 3030/3031 | 3040/3041 | next free pair from 3050 |
| image | explicit only (`--ref <commit>` or `--image`) | working tree, rebuilt on each `up` | working tree |
| git remote | `bottown` | `bottown-dev` | `bottown-e2e-…` |
| lifetime | long-lived; hosts the bottown repo | disposable | test only |

Each instance has its own container `bottown-<name>`, volume
`bottown-<name>-data`, `~/.bottown/<name>/` and Keychain service. Keys from one
instance get 401 on another.

**Promotion:** run `bottown up -i prod --ref <commit>`. This builds
`bottown:<sha12>` from `git archive` of that commit (so never from a dirty
tree), then recreates the container on the existing volume. To roll back,
re-run with the previous commit.

## Sequence

`bottown up` on the host:
1. Pings Docker and loads `~/.bottown/<i>/config.toml`, allocating ports on
   first use.
2. Resolves the image (see the table above).
3. Creates the container, starts it, or recreates it if the image ID changed.
   The volume is always kept.
4. Waits until `/healthz` reports `reconciled: true`.
5. Hand-off: if the host has no API key that works for the owner, it mints
   one with `docker exec`. On first creation it also stores the owner's
   initial Forgejo password in the Keychain, then tells the container to
   forget the password.

`bottown serve` in the container:
1. Waits for Forgejo's `/api/healthz`.
2. Ensures the site admin `bottown-admin` and its token, using the
   `forgejo admin` CLI with retries.
3. Converges the desired state in `internal/config/default.toml`. To
   override it, mount `/data/bottown/bottown.toml`. The state covers:
   - the owner, as a site admin;
   - the bots `implementer`, `verifier` and `manager`;
   - the org `bottown`, with teams that cover all repos;
   - org-wide labels;
   - one scoped token per actor;
   - branch protection on the default branch of every non-empty repo.

`bottown repo add [NAME]` is run inside a checkout:
1. Creates `bottown/NAME`.
2. Adds the instance's remote, leaving `origin` untouched as the external
   upstream.
3. Points git's credential helper for that URL at bottown, and only bottown.
4. Pushes the branch.
5. Waits for Forgejo to mark the repo non-empty (this is asynchronous), then
   sets the default branch and applies protection.

`bottown repo clone URL` will come later: it will clone with `origin` set to
URL, then run `repo add`.

## What Forgejo taught us (corrections to the problem statement §5)

These were found by the e2e suite. They change where some rules are
enforced:

1. **`prohibit_login` also rejects API tokens**, so bots cannot have it. Bots
   keep web login allowed, but their password is random and discarded, so no
   one can log in as them.
2. **Posting a PR review needs `write:repository`** token scope, so a
   read-only verifier token cannot work. The verifier gets
   `write:repository`. It still cannot push, because the `verifiers` team has
   read-only code access. (The e2e test checks that its push is rejected.)
3. **Commit statuses are covered by `write:repository`**, so token scope
   alone does not stop the implementer from forging `bottown/verify`. §5.2's
   "enforced by token scope" is wrong. The bottown proxy policy denies status
   writes to every bot (rule `commit-status`); bottown itself will post the
   status.
4. **Forgejo calls the admin-bypass guard `apply_to_admins`.**
   `block_admin_merge_override` is Gitea's name for it, and Forgejo silently
   ignores that field.
5. **`REQUIRE_SIGNIN_VIEW` makes `/api/v1/version` return 403** for anonymous
   callers. Readiness uses `/api/healthz` instead.
6. **Superseded tokens cannot be revoked by an admin.** Deleting a token
   needs the user's own basic auth. If an actor's scopes change, bottown mints
   a new token and logs a warning naming the old one.

## Proxy policy (step 1 rules)

The default is allow, because token scopes and team permissions already limit
what each bot can do. The owner is exempt from the rules below. For every
other actor, these writes are denied, because bottown performs them itself:

- merge;
- commit status;
- branch protection;
- issue and repo labels;
- repo settings and deletion;
- collaborators, hooks, deploy keys and transfer;
- `/admin`;
- user, org and team writes.

Every proxied call is logged as a `forge` audit line with actor, method, path,
decision, rule and status. The manager acts only inside bottown, so it can never
be issued an API key.

Known gap: an issue *created* through the proxy can carry labels in its body.
The supervisor's timeline check on `mode/*` (§5.3) covers that.

## Using it

```bash
bottown up -i dev                      # build working tree, provision
bottown -i dev credential password     # owner's Forgejo web password
bottown -i dev credential key implementer
claude mcp add --transport http bottown http://127.0.0.1:3041/mcp \
  --header "Authorization: Bearer <key>"
bottown -i dev repo add                # host the current checkout
bottown -i dev status
bottown -i dev down [--purge]
```

## Next steps (not in step 1)

- Per-run keys for agent `git push`, minted and revoked around each run.
- Forge MCP tools: a `forge_api` passthrough, typed issue/PR helpers, and a
  verdict submission pinned to a SHA. Also a `bottown api` CLI.
- A Forgejo Actions runner, so that `bottown/verify` comes from a fixed source
  (red-team C6).
- `bottown backup`, using `forgejo dump`.
- The supervisor loop.
