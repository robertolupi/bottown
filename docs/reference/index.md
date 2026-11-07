# bottown reference

How bottown behaves today, one topic per file. Read the file you need, not
all of them; [the docs index](../index.md) is the orientation page. When a
change adds a topic, it adds a file and a line here; other changes edit
only their topic's file.

- [Forge reads and pagination](forge-reads.md)
- [Issue attachments](issue-attachments.md)
- [Closing keywords on pull requests](closing-keywords.md)
- [Checks](checks.md) (`bottown check`, `.bottown.toml`)
- [Linux e2e](linux-e2e.md) (`make e2e-linux`)
- Runs:
  - [Reusing an issue branch](runs/issue-branch.md)
  - [A run's base](runs/base.md)
  - [Milestone branch and finish](runs/milestone-branch.md)
  - [Merging a milestone child](runs/milestone-child-merge.md)
  - [Milestone run](runs/milestone-run.md)
  - [Scratch directories](runs/scratch-directories.md)
  - [What run gc removes](runs/gc.md)
  - [Retro runs](runs/retro.md), including
    [windowed retros](runs/retro.md#windowed-retros)
  - [Critical discuss runs](runs/critical-discuss.md)
  - [Sharing the host](runs/sharing-the-host.md)
  - [One flow per issue](runs/one-flow-per-issue.md)
  - [Flow stops and exit codes](runs/flow-stops.md)
- [Harness models](harness-models.md)
- [The fam guide](fam-guide.md)
- [The wiki store](wiki-store.md) (`bottown wiki`)
- [Debates](debates.md) (`bottown debate`: independent rounds of answers,
  consolidated in the wiki)
- [Activity](activity.md) (`internal/activity`: runs, flow threads, checks,
  stops and owner waits over a window)
