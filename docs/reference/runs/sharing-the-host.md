# Sharing the host

Runs share the host with each other and with the owner. The implement and
verify prompts say so: a run must not stop, kill or remove processes,
containers or worktrees it did not start, nor run `bottown up` or `down`;
when shared state blocks it, it waits or comments and stops. `bottown up`
and `down` also refuse inside a run (with `$BOTTOWN_RUN_FILE` set, in a run
worktree, or in an actor worktree whose run is live), except on the
throwaway `e2e-*` instances that `make e2e` creates and purges.
