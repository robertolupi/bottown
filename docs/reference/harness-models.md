# Harness models

A launched run can choose its harness's model: wherever a command takes an
actor (`run implement|verify|discuss|plan -a`, `flow --implement` and
`--verify`), it also takes `ACTOR:SPEC`, split at the first colon. The actor
is the forge identity, with its family, key and worktree; the spec is opaque
to bottown and only the actor's harness binding reads it:

| Harness | Example | Passed as |
|---|---|---|
| claude | `claude:opus` | `--model opus` |
| antigravity | `agy:gemini-3.8-flash` | `--model gemini-3.8-flash` |
| kimi | `kimi:MODEL` | `-m MODEL` |
| codex | `codex:MODEL` | `-m MODEL` |
| crush | `crush:openrouter:~deepseek/latest`, `crush:lmstudio:qwen/qwen3.8-27b` | `-m PROVIDER/MODEL`: the spec's first colon separates the provider; a spec without one is a bare model |

- **Defaults.** `~/.bottown/<instance>/config.toml` may name a host's
  default spec per actor, used when the command gives none (the web UI's
  run and flow forms show it in their model fields):

  ```toml
  [actor_models]
  agy = "gemini-3.8-flash"
  ```

- **Checked by the harness.** bottown rejects only an empty spec or one
  with spaces or control characters. It keeps no list of models: an
  unknown one fails the run when the harness rejects it.
- **Listed by the harness.** `bottown models [ACTOR [ARGS...]]` runs the
  actor's harness's own listing, `agy models` or `crush models`, passing
  ARGS on (`bottown models crush openrouter`); without ACTOR it lists every
  actor's in turn. claude, kimi and codex have no listing command, and it
  says so: give them a name their model flag takes.
- **Recorded.** The server's run record and the local one keep the spec
  (`spec`), and `run ls`, `run show`, the handover comment, the web UI and
  the launcher's start line label it "model". A scripted harness
  (`BOTTOWN_HARNESS_CMD`) sees it as `$BOTTOWN_RUN_SPEC`.
- **Not in actor worktrees.** `run start` (and the slash commands) take no
  spec, and the server refuses one from an actor's key: the harness running
  there has already chosen its model.
- **The family stays the actor's.** The cross-family rule compares actors'
  declared families, never models. A harness that offers other vendors'
  models (agy's Claude models, crush through OpenRouter) can therefore put
  one family's model behind another family's actor, for instance
  `--verify agy:claude-…` on a Claude-authored PR. bottown does not detect
  this: choosing a model of the actor's own vendor is the owner's
  responsibility.
