<!-- {{.Marker}} -->
# Working in a bottown fam

{{if .Repo}}`{{.Repo}}`{{else}}This repository{{end}} is developed in a bottown fam:
a private Forgejo on this machine is the control plane, and coding agents
("actors") work issues through runs that bottown scopes and records. The
project's own instructions (AGENTS.md, README) say how to build, test and
check it; this guide says how to work in the fam.
{{- if .Actor}} This worktree is actor **{{.Actor}}**'s.{{end}}

## The fam layout

```
{{.Main}}  the owner's checkout
{{- range .Actors}}
{{.Path}}  actor worktree for {{.Name}}
{{- end}}
```

- **The owner's checkout is the owner's.** Actors work only in their own
  worktree, and never edit the owner's checkout or another actor's worktree.
- **The `{{.Remote}}` remote is the forge.** Its default branch is protected:
  only the owner pushes or merges there. Actors push their `issue/<n>`
  branch during a run.
- **`origin`, if present, is the external upstream.** Pushes to it, and to
  every remote other than `{{.Remote}}`, are blocked in actor worktrees.

## How actors work

Work happens in runs. In your actor worktree:

```
{{.Bottown}} run start implement 12      # or: /bottown-implement 12
{{.Bottown}} run start verify 13         # or: /bottown-verify 13
{{.Bottown}} run start discuss 12        # or: /bottown-discuss 12 QUESTION
{{.Bottown}} run start plan 14           # or: /bottown-plan 14
```

`run start` prints the goal prompt for the run: follow it. It says what the
run may do and how it ends. Use the bottown CLI for the forge; it holds no
token, and the server enforces your run's role and repository:

```
bottown whoami                      # actor, run or last run, HEAD vs main
bottown issue list [--state open|closed|all] [--repo R] [--json]
bottown issue show [N] [--repo R] [--json] [--download DIR]
bottown issue create --title T --body-file -
bottown pr list [--state open|closed|all] [--repo R] [--json]
bottown pr show [N] [--repo R] [--diff [--full] | --stat | --since-review [--reviewer R]] [--json]
bottown pr create --title T --body-file - [--part]
bottown comment [N] [--repo R] --body-file -
bottown review submit --event APPROVED|REQUEST_CHANGES|COMMENT --commit SHA --body-file -
bottown api METHOD PATH [--data JSON]
bottown check [--e2e | --report] [--json]
bottown wiki list | read PATH | put PATH [--base SHA] --file F | rm PATH --base SHA
bottown run end [--failed] --summary-file -
```

`bottown` here is `{{.Bottown}}`.

- **Implementers** push `issue/<n>` and open a PR. They cannot review or
  merge.
- **Verifiers** review exactly the commit the run pinned, with one verdict.
  They cannot push. A verifier must be from another model family than the
  author; only the owner can waive that.
- **The owner lands approved pull requests** with `{{.Bottown}} merge-all`,
  one at a time, each checked at the combined commit before it merges. An
  approved PR behind the default branch cleanly waits for it; the flow no
  longer merges the base into a PR.
- **Discuss runs** review and discuss one issue without implementing it: the
  worktree is detached at the default branch, and the answer goes in a
  comment on the issue. They cannot push, open PRs or review. A critical
  one (`run start discuss --critical 12`, or `/bottown-discuss-critical 12
  FOCUS`) red-teams the issue's design, and must be from another model
  family than the issue's author; only the owner can waive that. In a
  **debate** round (`bottown debate`, the owner's), each answers the same
  question in its own wiki file instead, and may not comment or file
  issues until a **consolidate run** has merged the round's answers.
- **Retro runs** write a blameless retrospective of one issue's flow from
  bottown's records, posted as a comment on the issue, or of a window of
  the default branch, written to its own wiki page
  (`YYYYMMDD-<sha>-retrospective`). Like discuss runs, they cannot push,
  open PRs or review; they may file follow-up issues.
- **Plan runs** write a milestone plan for a kickoff issue: they push
  `issue/<n>` with only that file and open a PR. They cannot file issues,
  review or merge.
- **Only the owner edits issues, PRs or milestones:** actors cannot close,
  reopen, retitle or retarget them, or change milestones.
- **If an issue is unclear or you are blocked,** comment on the issue and end
  the run with `--failed`. Do not guess, and do not work outside the issue.
- **Checks:** `bottown check` runs the repository's checks (those its
  `.bottown.toml` names, by default `make check`), keeps the full output in
  a log, prints a short summary, and records the result on your committed,
  pushed HEAD.
- **The wiki** is reached only through `bottown wiki`, never Forgejo's wiki
  API or git. It holds ideas, not decisions: it is never binding, and an
  issue or a design document wins over it. Leave the blocks between
  `<!-- bottown:<name>:generated -->` markers alone: bottown rewrites them.
  Runs read the wiki; which runs may write it, and where, is the server's
  call (`put` and `rm` say when they refuse).
- **Review follow-ups** happen in a new implement run on the same issue: it
  resumes `issue/<n>`. Every new head needs a new verify run.

## Outside a run

- **Reading is allowed:** `whoami`, `issue list`/`show`, `pr list`/`show`,
  `wiki list`/`read`.
- **Anyone can file issues** (`issue create`), in or outside a run (except a
  plan run): follow-ups found while working, or what the owner asks you to
  file from a conversation. Filing is the one write a read-only key may make.
- **Issue attachments** (screenshots, logs) are downloaded for a run;
  outside a run, `issue show --download DIR` fetches them.
- **After a run,** your worktree stays where the run left it, often on code
  that has since merged. Run `bottown whoami`: it shows the last run and how
  HEAD compares with the default branch.
- **Interrupted runs:** retry `bottown run end` after a startup or ending
  failure; `run start` also recovers before starting another run.

This guide is written by bottown (`bottown guide` prints it) and refreshed by
`bottown actor setup` and `bottown run start`; do not edit it.
