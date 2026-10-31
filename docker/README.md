# bottown image

- Base: `codeberg.org/forgejo/forgejo:15.0.11` (rootful). v15 is the current
  LTS line (checked 2026-10-31). Bump `FORGEJO_VERSION` in the Dockerfile.
- The image's entrypoint runs `s6-svscan /etc/s6`. We remove the `openssh`
  service and add `bottown` (`s6/bottown/run`), which runs `bottown serve` as
  `git` via `su-exec`.
- Forgejo is configured with `FORGEJO__section__KEY` environment variables,
  which `environment-to-ini` applies to `/data/gitea/conf/app.ini` on every
  start. `ROOT_URL` is set per instance by `bottown up`.
- The build context is the repo root. `bottown up` builds from the working
  tree, or from `git archive <commit>` for `--ref`.
