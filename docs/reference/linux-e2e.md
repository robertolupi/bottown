# Linux e2e

bottown is developed on macOS; `make e2e-linux` (`scripts/linux-e2e.sh`)
runs `make check` and `make e2e` on Linux. It needs
[Multipass](https://multipass.run) and runs them in an Ubuntu VM,
`bottown-linux` (4 CPUs, 8G memory, 30G disk; `BOTTOWN_LINUX_*` override
the name, size, Ubuntu image and Node major), with Docker installed by
cloud-init, the Go that `go.mod` names and the newest Node of the host's
major (26 without one), since `web/` declares none. Its user gets a global
git identity, as a developer's machine has.
The VM is a real Linux host, so the `e2e-*` containers are reachable at
`127.0.0.1` as on the Mac, which a container sharing the host's Docker
socket would not give. The VM is created on first use and kept for re-runs;
`--fresh` (`make e2e-linux ARGS=--fresh`) recreates it and `--delete`
removes it afterwards. It tests HEAD, not the working tree: HEAD and the
tags, as a git bundle, cloned into a directory of the checkout's own in
the VM, so the commit and `git describe` match. Runs take turns, whatever
checkout they come from: each holds a lock on the host
(`/tmp/bottown-linux-e2e-<vm>.lock`, with `flock` or macOS's `lockf`) from
before it touches the VM until it exits, so none deletes the VM or changes
its toolchains under another; each puts its own Go and Node first in its
`PATH`. The output, setup included, goes to `<gitdir>/bottown/linux-e2e.log`
(`BOTTOWN_LINUX_LOG`), and the script exits with the checks' status. It
covers linux/arm64 on Apple Silicon, where Multipass runs only arm64
guests; not amd64, and not the real agent harnesses, which e2e replaces
with `BOTTOWN_HARNESS_CMD` scripts. It is not one of the repository's
checks, and neither `bottown check --e2e` nor `bottown flow` runs it:
the owner runs it by hand before pushing `main` to GitHub. Until #305 is fixed,
two e2e tests usually fail in the VM: the server reads a PR's diff and
reviews before Forgejo has processed a push, which the slower VM exposes.
