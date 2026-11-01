# Built web UI

`make web` builds `web/` with vite into `dist/ui/`, which is not tracked.
The Go binary embeds this directory and, when `dist/ui/` was not built,
serves a stub page that says to run `make web`. This file keeps the
directory, and so `//go:embed all:dist`, valid in a fresh checkout.
