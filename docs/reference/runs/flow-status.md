# A flow's status file

While a flow holds its lock it writes a status file beside it,
`.bottown/runs/flow-<repo>-<issue>.json`, one step per change, so any
reader can tell what the flow is doing, whoever started it: the terminal
(`bottown flow`), `bottown milestone run`, or `merge-all --actor`'s repair.
It holds the flow's pid, the issue, the implementer and verifier with their
model specs, the review rounds, the round the flow is in, the step
(`implementing`, `checks`, `verifying`, `waiting for actor`, `between
runs`), a detail (for a wait, the actor and the run it waits for) and the
time the step started.

The file is a **view**, not the flow's state. The flow never reads it back,
and where an issue stands still comes from the forge and the server's run
records, so a killed flow is resumed by running the same command again. A
missing, stale or unreadable file costs a reader the detail and nothing
else.

The file is written atomically (a temporary file, renamed over the target),
so a reader sees the old file or the new one, never a partial write. When a
flow ends normally it removes the file; one that is killed, or that crashes,
leaves it behind as stale. A file is live only while its flow's lock is
held: `runs.HeldFlows` reports the flows whose lock is held, with a zero
view for one whose status file is missing or unreadable, and ignores a file
whose lock is free.
