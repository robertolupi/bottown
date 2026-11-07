# Flow stops and exit codes

Each stop of `bottown flow` is an issue comment, `**bottown flow:
<reason>**`, then its detail, then `Pull request: !N` when the flow had a
PR, and last a marker line a reader can parse without the prose:

```
<!-- bottown:flow-stop v=1 reason="out of rounds" -->
<!-- bottown:flow-stop v=1 reason="agent did nothing" actor="agy" -->
```

An `agent did nothing` marker also names the actor whose agent it was
(without its model), for `bottown milestone run`, which drops that actor
from its pool; no other stop carries an actor.

The reasons are `ready to merge`, `out of rounds`, `run failed`, `agent
did nothing`, `verifier undecided`, `conflicts with base`, `changes
requested`, `checks failed`, `actor busy` and `merged`. `bottown flow`
exits with:

| Exit | When |
|---|---|
| 0 | the flow stopped `ready to merge` |
| 3 | the flow stopped `agent did nothing` |
| 1 | any other stop, or an error |

`agent did nothing` is a batch implement or verify run whose agent made
no tool call (as `runs.LastError` reads its log), whether the harness
exited 0 or not: usually its actor is out of quota or signed out. Its
detail names the run, the actor and its model if one was pinned
(`--implement agy:MODEL`), the log and the harness's last error line. A
run that exited 0 without a tool call also fails `bottown run implement`
and `run verify` (exit 1, with the log's path), though the run's record
stays `succeeded`. Under the testing hook `BOTTOWN_HARNESS_CMD`, a script
that exits 0 is not checked, as its log is not the harness's stream.
`run failed` stays for a run that failed after a tool call, crashed, or
timed out.
