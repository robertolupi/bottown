# One flow per issue

`bottown flow N` holds a lock on the issue for its whole life:
`.bottown/runs/flow-<repo>-<issue>.lock` in the main checkout, an
exclusive flock with the flow's pid written in it. A second flow on the
same issue refuses at once, naming that pid and saying how to stop it
(`kill <pid>`, or Ctrl-C in its terminal). The kernel drops the lock when
the flow exits, however it ends, so a file left by a flow that died (its
pid dead) is not held and the next flow takes it over. The web UI's flow
start refuses in the same way before it starts the background flow, which
takes the lock itself. The file is not removed when the flow ends, and run
gc leaves it.
