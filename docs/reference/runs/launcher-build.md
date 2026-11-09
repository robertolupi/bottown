# The launcher's build

A fix that has merged is not live until `make install` puts it in
`~/.local/bin/bottown`, the binary that renders the prompts and launches
the runs. So each run records which build launched it: `run`, `flow` and
the other launchers, and `run start` for a leased run, send it with the
run request (`RunRequest.build`), and the server's run record and the
launcher's local record both keep it as `build`:

```json
"build": {"revision": "eee49e5b74dd", "dirty": true}
```

- **`revision`** is the Go toolchain's VCS stamp (`vcs.revision` from
  `debug.ReadBuildInfo`), the full commit, when the binary has one. The
  Makefile builds with `-buildvcs=false`, so an installed bottown has
  none; its revision is then its `-ldflags` version, `git describe
  --always --dirty --abbrev=12` at build time, without the `-dirty`.
- **`dirty`** is `vcs.modified`, or the version's `-dirty` suffix: the
  tree it was built from had uncommitted changes.

A binary with neither (`go test`, or `go build` without the Makefile's
`-ldflags`, whose version is `dev`) records no `build`, and the run
starts as usual. The server refuses (400) a revision that is empty, has
spaces or control characters, or is over 100 bytes.

`bottown run show` prints it as `launched by bottown <revision>`, with
` (dirty)`: from the local record, else the server's, so a purged or
gc'd run still shows it. A [retro](retro.md)'s `runs.json` has it on
each run, so the retro can tell "the fix did not work" from "the fix
was not installed yet".
