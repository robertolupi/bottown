# Design: milestones: plan, check, apply and run

Status: **design**, written with the owner in issue #74; Phases 1–4
(§5) are built (`internal/milestone`, `bottown milestone check`; the base
issue/PR/milestone rules, the `plan` run kind, `bottown run plan` /
`milestone plan` and `/bottown-plan`; `bottown milestone apply`; the web
UI's Milestones tab and `GET /api/milestones`). Extends
[the local web UI and supervisor design](web-ui.md) (§3.7).

## 1. Goal

The web UI milestone (#55 → #58–#65) was set up and driven by hand: a design
doc was reviewed and merged, its follow-up issues were filed manually with
dependencies written only in prose, and each child PR was merged into `main`
one by one by the owner.

This design automates milestone setup, visualization, and execution in **two
steps**:

- **Step 1 — Planning, applying, and visualizing dependencies:** Go from a
  design/kickoff issue to a reviewed milestone plan file on `main`
  (`bottown milestone plan`, `bottown milestone check`), apply that plan into
  Forgejo (`bottown milestone apply`) to create an **Implementation Epic
  issue** and its **child implementation issues** wired with native Forgejo
  `blocked_by` dependencies, and visualize the dependency graph in
  `bottown web ui`. At the end of Step 1, the owner can already run the
  milestone's issues manually with `bottown run` and `bottown flow`.
- **Step 2 — Running an implementation epic (`bottown milestone run`):**
  Create a protected milestone integration branch (`milestone/<epic>`), run
  ready child issues in parallel with cross-family implement/verify pairs from
  an owner-named actor pool, **automatically merge verified child PRs into
  `milestone/<epic>`** (closing each child issue so Forgejo dependencies
  unlock downstream work), and open a single final pull request from
  `milestone/<epic>` into `main` for the owner to review, approve, and merge.

### 1.1 Why this narrows two non-goals without reviving v1

- **No autonomous planner directing agents ([problem statement](../problem-statement.md) §6).**
  v1 failed when planner agents dynamically dispatched and coordinated with
  other agents. Here, a `plan` run only proposes a static markdown file in a
  pull request; server policy forbids it from creating issues or starting runs,
  and base policy forbids all actors from editing or closing existing issues,
  changing milestones, or changing dependencies. A cross-family verifier
  reviews the plan PR, the owner merges it into `main`,
  `bottown milestone apply` (deterministic Go code run by the owner) creates
  the issues and dependencies from `main`, and a mechanical scheduler
  (`bottown milestone run`) dispatches flows from Forgejo's dependency state.
  No agent ever directs another.
- **Merging into `main` stays the owner's ([web UI design](web-ui.md) §3, §10).**
  `bottown` never merges into `main`. In Step 2, `bottown` (using the internal
  `manager` account) merges child pull requests **only into the milestone
  branch `milestone/<epic>`**, and only after the child PR has a valid
  cross-family `APPROVED` review at its exact head SHA. Landing
  `milestone/<epic>` into `main` remains a single pull request authored by
  `manager` that the owner reviews, approves, and merges in Forgejo.

---

## 2. Step 1: Plan, Check, Apply, and Visualize

### 2.1 Pipeline overview

```
owner creates Forgejo milestone M
owner files design/kickoff issue #K in M (and merges design doc if separate)
        │
        ▼
bottown milestone plan K -a <actor>    (or /bottown-plan K in a harness)
        │  plan run writes docs/milestones/<slug>.md
        │  runs `bottown milestone check <slug>`
        │  pushes issue/K and opens PR into main (creates no issues)
        ▼
cross-family verify run on PR ──▶ APPROVED ──▶ owner merges PR into main
        │
        ▼
bottown milestone apply <slug>         (owner only, reads from main on forge)
        │  creates/updates in milestone M:
        │    • Implementation Epic issue #E (task list + blocked_by all children)
        │    • Child implementation issues #C1..#Cn (with blocked_by edges)
        ▼
bottown web ui (Milestones tab: DAG diagram) + manual `run`/`flow` (Step 1)
   or `bottown milestone run E --agents ...` (Step 2)
```

- **The owner owns Forgejo milestone lifecycle.** The owner creates the
  Forgejo milestone in the tracker and assigns the kickoff/design issue `#K`
  to it. Neither `plan` nor `apply` creates or closes Forgejo milestones.
- **The plan is a reviewed file on `main`.** A plan is never applied from a
  working tree or an unmerged branch: `apply` reads both `.bottown.toml` and
  the milestone plan file from `main`'s commit on the forge.

### 2.2 The `plan` run kind and base issue-mutation policy

A plan run is a distinct run role, `policy.Plan` (`"plan"`), rather than an
ordinary `implement` run with a different prompt, paired with base-policy
rules reserving issue edits and milestone mutations for the owner and
`manager`:

- **Why a distinct role and base issue-edit denial:**
  An `implement` run is allowed to create follow-up issues
  (`POST /repos/*/*/issues` and `/v1/forge/issue/create`). If `plan` reused
  `implement`, "the planner creates no issues" would be prompt advice only
  ([problem statement](../problem-statement.md) §2). Furthermore, because
  Step 2's scheduler uses issue `closed` state, milestone membership, and
  provenance markers in issue bodies to drive readiness and base-branch
  selection, **no actor may edit or close existing issues or modify
  milestones**.
- **Base policy (`internal/policy/policy.go`):**
  In addition to the existing deny rules (`issue-labels`,
  `issue-dependencies`, `issue-blocks`, `merge`, `commit-status`,
  `branch-protection`), `policy.Default` denies for all non-exempt actors:
  - `issue-edit`: `PATCH` and `DELETE` on `/repos/*/*/issues/*` — actors
    cannot close or reopen issues, change an issue's `milestone`, or edit an
    existing issue's title or body (preserving provenance markers).
  - `pull-edit`: `PATCH` and `DELETE` on `/repos/*/*/pulls/*` — actors cannot
    close PRs or retarget a PR's base branch through the API proxy.
  - `milestones`: `POST`, `PUT`, `PATCH`, `DELETE` on
    `/repos/*/*/milestones/**` — actors cannot create, edit, or close Forgejo
    milestones.
- **Run scope for `plan` (`internal/policy/run.go`, `internal/server/forge.go`):**
  - *Git:* identical to `implement` — may fetch from the run's repository and
    push to the run's branch (`issue/<K>`).
  - *API and forge tools:* may read the run's repository, comment on issues
    and PRs, and create the pull request for `issue/<K>` (`opPRCreate` accepts
    `policy.Implement` or `policy.Plan`).
  - *Denied writes:* issue creation (`POST /repos/*/*/issues` and
    `opIssueCreate` return `403 Forbidden` with rule `role-plan-no-issues`),
    PR reviews, and all base-policy denials above.
- **Prompt (`internal/runs/prompts/plan.md`):**
  - Renders the kickoff issue `#<K>`, its comments, and the repository's
    `milestones_dir` setting.
  - Instructs the agent to read `#<K>` and any design document it references
    on `main`, write `<milestones_dir>/<slug>.md` in the micro-format (§2.4),
    run `bottown milestone check <slug>` plus `make check` (or the
    repository's checks), commit only the milestone plan file on `issue/<K>`,
    push, and open a PR into `main`.
- **CLI and harness commands:**
  - Host batch/interactive launcher: `bottown milestone plan -a ACTOR [-I] K`
    (alias for `bottown run plan -a ACTOR [-I] K`).
  - In-worktree agent start: `bottown run start plan K`.
  - Harness slash command installed by `bottown harness setup`:
    `/bottown-plan <K>`, which runs `bottown run start plan <K>` and follows
    its prompt to `bottown run end`.
  - Once the plan PR is open, it is verified by another model family with
    `bottown run verify` (or `bottown flow K`) and merged by the owner like
    any other PR.

### 2.3 Repository setting: `milestones_dir` in `.bottown.toml`

`config.RepoSettings` (`internal/config/repo.go`, decoded with
`DisallowUnknownFields`) gains one optional field:

```toml
after_pull = ["make install"]
milestones_dir = "docs/milestones"
```

- Default when unset or when `.bottown.toml` is absent: `"docs/milestones"`.
- Validation: must be a clean, repo-relative directory path (`path.Clean(p) == p`,
  not empty, not `"."`, no leading `/`, no `..` segments). Invalid values are
  reported by `config.ParseRepoSettings` and shown in the Web UI health
  checklist.
- `bottown milestone apply` always reads `.bottown.toml` from `main` on the
  forge before resolving `<milestones_dir>/<slug>.md` at that same `main`
  commit.

### 2.4 The milestone plan file format

A milestone plan file `<milestones_dir>/<slug>.md` is standard Markdown with
TOML front matter and one fenced TOML metadata block per `##` issue section.
The file slug `<slug>` (the filename without `.md`) must match
`^[a-z0-9]+(-[a-z0-9]+)*$`.

````markdown
+++
title = "Milestones: plan, check, apply and run"
kickoff = 74
design = "docs/design/milestones.md"
+++

Optional overview prose for the implementation epic body.

## Plan format, repo config and offline check

```issue
key = "plan-format-check"
depends = []
```

Implement the milestone plan parser and `bottown milestone check`.

### Acceptance criteria

- Rejects duplicate keys, unknown fields, and cycles.

## Distinct plan run kind and harness command

```issue
key = "plan-run-kind"
depends = ["plan-format-check", "#64"]
```

Add `policy.Plan` and `/bottown-plan`.
````

#### Rules of the micro-format

1. **Front matter (`+++` TOML fence):**
   - Must start on line 1 with `+++` and close with `+++` on its own line.
   - Decoded with `go-toml/v2` and `DisallowUnknownFields`:
     - `title` (required, non-empty string): title of the Implementation Epic
       issue created by `apply`.
     - `kickoff` (required, positive integer): issue number of the kickoff /
       design issue in the same repository.
     - `design` (optional string): clean repo-relative path to the design
       document on `main` (no leading `/`, no `..`).
2. **Overview prose:**
   - Any text after the front matter and before the first `## ` heading is
     optional overview prose; `apply` includes it in the Implementation Epic's
     body above the generated task list.
3. **Issue sections (`## <Title>`):**
   - The parser tracks fenced code blocks (` ``` ` or `~~~`) and recognises
     `## <Title>` headings only outside fences.
   - Level-1 (`#`) headings after front matter are rejected as ambiguous;
     headings inside an issue body must use `###` or deeper.
   - Every file must contain at least one `## <Title>` section, and each
     heading title must be non-empty after trimming whitespace.
4. **Fenced `issue` TOML block:**
   - The first non-blank content of every `##` section must be a fenced code
     block with info string `issue` (` ```issue `). A second `issue` fence in
     the same section is an error.
   - Decoded as TOML with `DisallowUnknownFields`:
     - `key` (required string): stable identifier for this child issue within
       the plan, matching `^[a-z0-9]+(-[a-z0-9]+)*$`, unique in the file.
     - `depends` (optional string array, default `[]`): dependencies that must
       be closed before this issue is ready. Each entry is either another
       `key` in the same file or `"#<N>"` referencing an existing issue in
       the same repository. Duplicate entries, self-dependencies, and
       `"#<kickoff>"` (depending on the kickoff issue itself) are errors.
5. **Issue body:**
   - Everything after the closing fence of the `issue` block up to the next
     `## ` heading (or EOF), with leading and trailing blank lines trimmed
     and internal newlines preserved verbatim, is the child issue's body.
   - The body must be non-empty.

### 2.5 Offline validation: `bottown milestone check`

```
bottown milestone check <slug|path>
```

`check` is **pure and network-free**: it never contacts the server or Forgejo,
so agents can run it during a run, verifiers can run it in a detached
worktree, and unit tests can run it against golden files.

- If the argument contains `/` or ends in `.md`, `check` reads that file path
  directly; otherwise it reads `<milestones_dir>/<slug>.md` in the current
  checkout (reading `.bottown.toml` locally if present) and validates `<slug>`.
- `check` verifies:
  1. Front matter syntax, required fields (`title`, `kickoff`), and clean
     `design` path (plus that `design` exists in the checkout when run inside
     one).
  2. Every `##` section has a non-empty title, starts with one valid `issue`
     TOML block, has no extra `issue` blocks, and has a non-empty body.
  3. All `key` values are valid kebab-case and unique across the file.
  4. Every entry in `depends` is either a `key` defined in the file or a
     syntactically valid `"#<N>"` (`N > 0`, `N != kickoff`), with no
     self-edges or duplicate entries.
  5. The in-file dependency graph is a DAG (no cycles). If a cycle exists,
     `check` prints the cycle (`a -> b -> c -> a`).
- Diagnostics include `file:line` and the section's `key` (or heading). Exit
  code is `0` when valid, `1` on validation errors.

### 2.6 Applying a plan: `bottown milestone apply`

```
bottown milestone apply <slug> [--dry-run] [--force] [--repo R]
```

`bottown milestone apply` is an **owner-only command**. It reconciles the
merged plan on `main` into the Forgejo tracker. It runs in the owner's main
checkout and talks to Forgejo through the server's API proxy with the
owner's key (`internal/milestone.Apply` behind a small `Forge` interface;
the CLI's implementation is `internal/cli/milestone_forge.go`). It prints
every planned change and flag before writing, then each write.

#### Provenance markers and trust rule

To keep the merged file on `main` identical to what was reviewed, the mapping
from plan keys to Forgejo issue numbers lives in the forge as an HTML comment
line at the bottom of each issue body created by `apply`:

- On the **Implementation Epic issue**:
  ```markdown
  <!-- bottown:milestone v=1 plan="<slug>" role="epic" sha="<hex>" -->
  ```
- On each **child implementation issue**:
  ```markdown
  <!-- bottown:milestone v=1 plan="<slug>" key="<key>" sha="<hex>" -->
  ```

`sha` is the first 16 hex characters of the SHA-256 of the canonical title and
body that `apply` last wrote (`title + "\n\n" + bodyWithoutMarker`), used to
detect whether an issue was edited by hand in Forgejo after `apply`.

- **Universal marker trust rule:** Every component that reads
  `bottown:milestone` markers (`apply` in §2.6, the Web UI in §2.7, base-branch
  resolution in §3.3, and the epic scheduler in §3.5) **accepts a marker only
  on issues authored by the owner** (`issue.Poster.UserName == owner`). Because
  `apply` runs as the owner and base policy (`issue-edit`, §2.2) denies
  `PATCH /repos/*/*/issues/*` to all actors, an actor can neither plant a
  trusted marker by filing an issue nor inject or alter a marker on an
  owner-authored issue.
- Because markers carry `plan="<slug>"`, multiple plan files may safely
  contribute to the same Forgejo milestone if the owner chooses.

#### Preflight (before any forge write)

1. **Read from `main` on the forge:** Resolve `main`'s current commit SHA via
   the API proxy as the owner, read `.bottown.toml` and
   `<milestones_dir>/<slug>.md` at that commit, and run the offline `check`
   rules. If the file does not exist on `main` or fails `check`, stop.
2. **Resolve the kickoff issue and its Forgejo milestone:**
   - Fetch `#<kickoff>` in the repository. Refuse if `#<kickoff>` does not
     exist, is a pull request, or has **no Forgejo milestone** assigned, or if
     its milestone is closed.
   - Record the numeric Forgejo milestone ID `M`. `apply` files the
     Implementation Epic and every child issue into milestone `M`.
   - If `design` is set in front matter, verify that the file exists at
     `main`'s commit SHA.
3. **Validate external `#<N>` dependencies and full graph acyclicity:**
   - Every `"#<N>"` in `depends` must exist as an issue (not a PR) in the same
     repository and must not be the Implementation Epic `#<epic>` or
     `#<kickoff>`.
   - **Closed dependencies are allowed** and already satisfied.
   - Fetch existing Forgejo `blocked_by` dependencies of referenced issues and
     verify that combining the plan's edges with reachable existing edges in
     the repository introduces no cycle.
4. **Discover existing plan issues by marker:**
   - List all issues (open and closed, paginated) in the repository and parse
     `<!-- bottown:milestone ... -->` markers for `plan="<slug>"` on
     owner-authored issues (ignoring or flagging any marker on an
     actor-authored issue).
   - If two owner-authored issues carry the same `(plan, role="epic")` or
     `(plan, key)` marker, refuse and report both issue numbers rather than
     guessing.
   - If an existing managed issue is attached to a different Forgejo milestone
     than `#<kickoff>`'s milestone `M`, refuse and report the mismatch.

With `--dry-run`, `apply` prints the resolved `main` SHA, milestone `M`, and
every create, update, dependency add/remove, and flagged item it would
perform, then exits `0` without writing.

#### Reconciliation order (idempotent and resumable)

Forgejo has no multi-issue transaction, so `apply` orders writes so that an
interrupted run can always be resumed by running `bottown milestone apply <slug>`
again:

1. **Create or update child implementation issues:**
   - For each section in file order:
     - Render the managed body: the section's markdown body, a provenance
       footer linking kickoff `#<kickoff>` (and `design` path if set), and the
       `<!-- bottown:milestone v=1 plan="<slug>" key="<key>" sha="<hex>" -->`
       marker.
     - **New key:** `POST /repos/{org}/{repo}/issues` as the owner with
       `Title`, `Body`, and `Milestone: M`.
     - **Existing key, unchanged `sha`:** no write.
     - **Existing key, plan text changed (`newSHA != marker.sha`):**
       - If the issue is **closed**, do **not** reopen or edit it; flag in
         output (`#<num> (<key>): issue is closed; plan text change skipped`).
       - If the issue is **open** and its current Forgejo title+body still
         matches `marker.sha`, `PATCH` the issue with the new title, body, and
         marker `sha`.
       - If the issue is **open** but its current Forgejo title+body no longer
         matches `marker.sha` (manual edit in Forgejo), **flag conflict/drift**
         in output (`#<num> (<key>): modified in Forgejo since last apply`)
         and skip overwriting unless `--force` is passed.
   - **Removed keys** (an owner-authored issue on the forge has
     `plan="<slug>"` and `key="K"`, but `K` is no longer in `<slug>.md`):
     - Never automatically closed, deleted, or stripped of its history.
     - Excluded from the epic's active child set (removed from the epic's
       checklist and from `#<E>`'s `blocked_by` dependencies) and flagged in
       `apply`'s output and in `#<E>`'s `### Flagged (removed from plan)`
       section so the owner can close or re-triage it.
2. **Create or update the Implementation Epic issue:**
   - The Implementation Epic's body is fully owned by `apply` and contains:
     - Provenance header: kickoff issue `#<kickoff>`, design doc link (if
       set), and plan file `<milestones_dir>/<slug>.md` at the commit on
       `main` that last changed it (not `main`'s head, which moves with every
       merge and would rewrite the epic on each re-apply).
     - Optional overview prose from the plan file.
     - A `## Implementation issues` checklist of active keys in file order:
       `- [ ] #<num> — <Title> (`<key>`, depends on #<d1>, #<d2>)`
       (using `[x]` for child issues that are already closed).
     - If any keys were removed from the plan file, a
       `### Flagged (removed from plan)` section listing their issue numbers
       and state.
     - The epic marker `<!-- bottown:milestone v=1 plan="<slug>" role="epic" sha="<hex>" -->`.
   - Created in milestone `M` on first apply; updated via `PATCH` whenever its
     rendered text changes.
3. **Reconcile Forgejo `blocked_by` dependencies:**
   - For each active child issue `#<C>`:
     - Desired plan-owned dependencies = the resolved issue numbers of its
       `depends` list.
     - Compare with current `GET /repos/{org}/{repo}/issues/{C}/dependencies`:
       add missing `blocked_by` edges (`POST .../dependencies`) and remove
       edges to other issues of `plan="<slug>"` that are no longer in
       `depends` (`DELETE .../dependencies`). Dependencies on issues outside
       `plan="<slug>"` that the owner added manually are preserved unless they
       were previously declared in `depends`. What was previously declared
       comes from the current epic's checklist (`depends on #<d1>, …`), which
       `apply` wrote at the last apply.
   - All removals are made before any addition, so reversing an edge never
     leaves a transient two-issue cycle that Forgejo would refuse.
   - An existing epic's `PATCH` (step 2) is made after the removals and
     before the additions. Its checklist is the only record of the external
     dependencies the plan declared, so it changes only once their stale
     edges are gone: an apply interrupted earlier still removes them on the
     next run, instead of mistaking them for the owner's own. A new epic is
     created before any dependency write, since it has edges of its own.
   - For the **Implementation Epic `#<E>`**:
     - Ensure `#<E>` has a Forgejo `blocked_by` dependency on **every active
       child issue** of the plan (and remove edges to keys removed from the
       plan).
     - Thus in Forgejo, `#<E>` is blocked by all of its active child issues and
       becomes unblocked when all active child issues are closed.

### 2.7 Web UI: Milestones view (`bottown web ui`)

Step 1 adds a **Milestones** view to `bottown web ui` ([web UI design](web-ui.md)
§4).

- **What it displays:**
  - Every Implementation Epic in the repository (discovered from
    owner-authored issues carrying the `role="epic"` marker per §2.6's trust
    rule, grouped under their Forgejo milestone), plus any Forgejo milestone
    without a plan epic.
  - **Summary bar:** Forgejo milestone link, Implementation Epic `#<E>` link,
    kickoff `#<K>` and design doc link, active base branch (`milestone/<E>` if
    present on the forge, otherwise `main`), and status counts (`closed/total`,
    `ready`, `running`, `awaiting merge`, `blocked`, `stopped`).
  - **Interactive dependency DAG diagram (Mermaid-style layered SVG):**
    - Nodes are the active child implementation issues `#<C1>..#<Cn>` (plus any
      external `#<N>` dependencies shown as compact pill nodes; removed keys
      are listed separately under a flagged banner).
    - Directed edges `A ──▶ B` show that `B` depends on `A` (`B` is blocked by
      `A` in Forgejo). Layers are computed topologically from roots to leaves
      and rendered as clean inline SVG (respecting `web-ui.md` §8's strict CSP
      with no external scripts or unsafe inline execution).
    - **Each node card shows:**
      - `#<num>` and issue title (plus `key`).
      - Status badge:
        - `blocked` (waiting on open dependencies),
        - `ready` (all dependencies closed, no active run or open PR),
        - `running` (`implement` or `verify` live run, actor name, elapsed time),
        - `in review` / `approved` (open PR `!<pr>` and its review state),
        - `stopped` (last flow stop reason: `out of rounds`, `run failed`,
          `verifier undecided`, `conflicts`),
        - `closed` (completed).
      - Assigned actor pair (`implement/<A>`, `verify/<B>`) when set.
      - Direct links to `#<num>` and `!<pr>` in Forgejo.
  - **How it is built:** the host UI serves `GET /api/milestones`
    (`internal/webui/milestones.go`). An epic's active children are its
    plan's trusted children that it is blocked by; the plan's other trusted
    children are its removed keys. Kickoff, plan file and design come from
    the epic's header line. A Forgejo milestone without an epic shows its
    issues the same way (open milestones only). A node's status is, first
    match wins: `closed`; `running` (a live run on the issue or its PR, or
    a UI-started flow between runs); `stopped` (the owner's newest
    `**bottown flow: …**` comment, other than ready to merge, with no run
    since; or the last run failed; or the open PR's reviews assess as
    changes requested or undecided); `approved` / `in review` (an open PR);
    `blocked` (an open dependency); else `ready`. The actor pair is the
    `implement/`/`verify/` labels, else the running flow's, else the last
    runs'. Layers are each node's longest distance from a root, external
    dependencies included as layer-0 pills.
  - **Actions in the Milestones view:**
    - In Step 1: start a single-issue flow or run on any `ready` or `stopped`
      child issue directly from its node; clicking any open node that is not
      running opens the flow dialog for it.
    - In Step 2: start or stop `bottown milestone run <E>` with an actor pool
      selector.

---

## 3. Step 2: Running an Implementation Epic (`bottown milestone run`)

### 3.1 Overview and the `milestone/<epic>` branch

In Step 2, the owner runs an entire Implementation Epic `#<E>`:

```
bottown milestone run <E> --agents agy,claude,kimi [--rounds 3] [--timeout 60m] [--repo R]
```

(`<E>` may be the Implementation Epic's issue number `#80`, `80`, or the plan
slug `<slug>`, which resolves to that plan's epic issue.)

```
main ──────────────────────────────────────────────────────────●──────▶ (owner approves & merges final PR)
       \                                                      /
        └──▶ milestone/80 ────●────────────●────────────●────┘  final PR (by manager): milestone/80 → main
               \             /            /            /        (Closes #80)
                ├─▶ issue/81 (agy + claude) ── auto-merge + close #81 (unblocks #83)
                ├─▶ issue/82 (claude + kimi) ─ auto-merge + close #82 (unblocks #83)
                └─────────────────────────────▶ issue/83 (kimi + agy) ── auto-merge + close #83
```

1. **Milestone branch creation:** When `milestone run <E>` starts, if branch
   `milestone/<E>` does not yet exist on the forge, `bottown` (as `manager`)
   creates `milestone/<E>` at `main`'s current head SHA on the forge.
2. **Child issues target `milestone/<E>`:** Every child issue `#<C>` of epic
   `#<E>` branches `issue/<C>` from `milestone/<E>` and opens its pull request
   with base `milestone/<E>`.
3. **Cross-family verification and auto-merge into `milestone/<E>`:** Each
   ready child issue runs through implement → cross-family verify. Once its PR
   into `milestone/<E>` has a non-stale official `APPROVED` review from a
   different model family at its current head SHA and merges cleanly,
   `bottown` (as `manager`) **automatically merges the child PR into
   `milestone/<E>` and closes `#<C>`**.
4. **Forgejo dependencies drive readiness:** Because Forgejo's native issue
   dependencies clear `blocked_by` when an issue is **closed**, closing `#<C>`
   upon merging its PR into `milestone/<E>` immediately unblocks any child
   issue that depends on `#<C>`.
5. **Final pull request to `main`:** When all active child issues of `#<E>`
   are merged into `milestone/<E>` and closed, `bottown` (as `manager`) opens
   (or updates) a pull request from `milestone/<E>` into `main` (`Closes #<E>`)
   and stops for the owner to review, approve, and merge in Forgejo.

### 3.2 Protection on `milestone/*` branches

To preserve the same enforcement guarantees on `milestone/<E>` that `main` has
([problem statement](../problem-statement.md) §5.2):

- **Forgejo branch protection rule `milestone/*`:**
  `internal/provision` reconciles a branch protection rule with pattern
  `milestone/*` on every repository alongside the default branch's rule
  (`reconcile.go:576-592`). Its settings match `WantProtection()` for `main`,
  with `"manager"` added to the push whitelist so the server can create
  `milestone/<E>` as `manager`:
  - `EnablePush: true`, `EnablePushWhitelist: true`,
    `PushWhitelistUsernames: [owner, "manager"]` — actors cannot push directly
    to `milestone/<E>`, force-push it, or create/delete `milestone/*` branches.
  - `EnableMergeWhitelist: true`,
    `MergeWhitelistUsernames: withOwner(Desired.Protection.MergeWhitelist, owner)`
    (`[owner, "manager"]`) — actors cannot merge PRs into `milestone/<E>`.
  - `EnableApprovalsWhitelist: true`,
    `ApprovalsWhitelistUsernames: withOwner(append(Desired.ActorNames(), Desired.Protection.ApprovalsWhitelist...), owner)`
    — all configured actors plus the owner, identical to `main`, so a
    cross-family verifier's `APPROVED` review satisfies Forgejo's approval gate
    on `milestone/<E>`.
  - `RequiredApprovals: 1`, `DismissStaleApprovals: true`,
    `BlockOnRejectedReviews: true`, `ApplyToAdmins: true`.
- **Git push ref check in the proxy:**
  In `internal/policy/run.go` / git proxy, an `implement` or `plan` run for
  issue `#<N>` may only push `issue/<N>`, preventing an implementer from
  pushing to another issue's branch.

### 3.3 Automatic base-branch resolution (`main` vs `milestone/<E>`)

A child issue may be run either by `bottown milestone run <E>` or manually by
the owner (`bottown run implement <C>` or `bottown flow <C>`). Both paths share
one rule on the server (`POST /v1/runs` and `opPRCreate`):

- When resolving the base branch for issue `#<C>`:
  1. Check whether `#<C>` is an **owner-authored** issue carrying a valid
     `bottown:milestone` child marker (`plan="<slug>"`, `key="<key>"`) whose
     owner-authored epic `#<E>` (`plan="<slug>"`, `role="epic"`) is open and
     lists `#<C>` in its active `blocked_by` dependencies (applying §2.6's
     marker trust rule so an actor-authored issue with a copied marker never
     gets a milestone base branch), **and** whether branch `milestone/<E>`
     exists on the forge.
  2. If `milestone/<E>` exists on the forge, `run.Base` is `"milestone/<E>"`.
  3. Otherwise (for standalone issues, or in Step 1 before `milestone run` has
     created `milestone/<E>`), `run.Base` is the repository's default branch
     (`"main"`).
- `secrets.Run` and `runs.Record` store `Base string` (`json:"base,omitempty"`):
  - `ws.AddImplement(run.ID, run.Branch, run.Base)` creates `issue/<C>` from
    `origin/<run.Base>` when `origin/issue/<C>` does not yet exist.
  - The implement prompt renders `run.Base` as the base branch to merge from
    and open PRs against.
  - `opPRCreate` sets `Base: run.Base` on the created pull request.
  - If an open PR from `issue/<C>` already exists, `flow` uses that PR's
    `BaseBranch`.

### 3.4 Actor pool and cross-family pairing

`bottown milestone run <E> --agents a,b,c` requires an explicit comma-separated
actor pool from the owner:

- **Validation before dispatch:**
  - Every actor in `--agents` must be a configured actor on the instance.
  - The pool must contain actors from **at least two distinct model families**;
    otherwise `milestone run` refuses before starting any run.
- **Assignment labels (`implement/<actor>`, `verify/<actor>`):**
  - Provisioning ensures org labels `implement/<actor>` and `verify/<actor>`
    exist for each configured actor ([web UI design](web-ui.md) §3.6).
  - When a child issue `#<C>` already has both `implement/<A>` and
    `verify/<B>` labels from an earlier run of the milestone (and `A`, `B` are
    in `--agents` with `family(A) != family(B)`), the scheduler keeps `(A, B)`
    so resuming a milestone never silently switches an in-progress issue to a
    different actor.
  - When a ready child issue `#<C>` has no assignment yet, the scheduler
    chooses an eligible ordered pair `(A, B)` from `--agents` with
    `family(A) != family(B)`:
    - Candidate ready issues are ordered deterministically by ascending issue
      number `#<C>`.
    - Among valid cross-family pairs `(A, B)`, the scheduler picks a pair
      whose implementer `A` has an available batch slot (`activeBatch(A) < limit(A)`),
      breaking ties by least active assignments in the milestone and then deterministic
      round-robin order over the `--agents` list.
    - Before starting the first run on `#<C>`, `bottown` sets `implement/<A>`
      and `verify/<B>` on `#<C>` via an owner-only label endpoint.

### 3.5 The epic scheduler loop, auto-merge, and conflict resolution

Like single-issue `flow`, `milestone run` is **stateless**: all state lives in
Forgejo (issues, `blocked_by` dependencies, labels, PRs, reviews) and the
server's run records. Killing and re-running `bottown milestone run <E> --agents ...`
resumes from the current forge state.

#### Readiness and dependency semantics

- **The epic's active child set** is the set of owner-authored child issues
  carrying `plan="<slug>"` markers that are currently in `#<E>`'s Forgejo
  `blocked_by` dependencies (excluding the kickoff/design issue `#<K>`, `#<E>`
  itself, and any removed keys whose `blocked_by` edge from `#<E>` was dropped
  by `apply` in §2.6).
- An active child issue `#<C>` is:
  - **Complete** when `#<C>`'s state in Forgejo is `closed` **and** `#<C>` was
    closed either by `manager` (with a merged PR from `issue/<C>` into
    `milestone/<E>`) or by the owner (`closed_by` / latest close event in the
    issue timeline).
  - **Blocked** when `#<C>` is `open` and its Forgejo dependencies list
    (`GET /repos/{org}/{repo}/issues/{C}/dependencies`) contains any issue
    whose state is `open` (or any closed dependency that fails the owner/`manager`
    closure check above).
  - **Ready** when `#<C>` is `open` and all of its Forgejo dependencies are
    `closed` (by `manager` or the owner).
- **Enforcement & consequence of Forgejo closed-state dependencies:**
  Base policy (`issue-edit` in §2.2) denies `PATCH /repos/*/*/issues/*` to all
  actors, so an agent cannot close an issue to unblock a dependent. As
  defense in depth, if the scheduler ever encounters a child issue or
  dependency closed by any account other than `manager` or the owner, it does
  not treat that issue as complete: it keeps dependents blocked, posts a stop
  comment on `#<E>` naming the unauthorized closure, and halts that branch.
  When the **owner** manually closes a child issue in Forgejo without merging a
  PR (cancelling or accepting it out-of-band), Forgejo unblocks its dependents
  and the scheduler treats that owner-closed child issue as satisfied. An
  approved but unmerged PR never unblocks dependents.

#### Dispatch priority and concurrency

- Multiple ready child issues run concurrently in their own goroutines, each
  driving its issue's flow against `milestone/<E>`.
- The server's atomic per-actor `max_batch_runs` limit ([web UI design](web-ui.md)
  §3.4) prevents over-committing any actor across all running flows.
- **Verification before new implementations:** When an actor slot frees up, a
  waiting `verify` step on an already-open child PR takes priority over
  starting a first `implement` run on a newly ready child issue, so open PRs
  merge into `milestone/<E>` and unlock dependents as early as possible.

#### Auto-merging verified child PRs into `milestone/<E>`

When a child issue `#<C>`'s PR `!<P>` reaches `ReadyToMerge` (head `H` has a
non-stale official `APPROVED` review from `#<C>`'s cross-family verifier, and
`Mergeable == true`):

1. The scheduler calls the server's owner-only milestone merge endpoint
   (`POST /v1/repos/{repo}/milestones/{epic}/merge`, passing `issue: C`,
   `pr: P`, `head_sha: H`).
2. The server verifies using its admin/manager client that:
   - `#<C>` is an owner-authored child of epic `#<E>`;
   - `!<P>` is open, has `Base.Ref == "milestone/<E>"`, `Head.Ref == "issue/<C>"`,
     and `Head.Sha == H`;
   - `!<P>` has a non-stale official `APPROVED` review at commit `H` from an
     actor of a different model family than `!<P>`'s author.
3. The server merges `!<P>` into `milestone/<E>` as `manager` (recording an
   audit entry `milestone_merge`), deletes or leaves `issue/<C>`, and
   **explicitly closes issue `#<C>` as `manager`**
   (`PATCH /repos/{org}/{repo}/issues/{C}` with `state: "closed"`, because
   Forgejo only auto-closes `Closes #C` when a PR merges into the repository's
   default branch).
4. The server posts a comment on `#<C>`:
   `**bottown milestone #<E>: merged !P into milestone/<E> at <sha>**`.
5. On the next scheduler tick, `#<C>` is `closed`, so any child issue blocked
   only by `#<C>` becomes **ready** and is dispatched.

#### Automatic sibling conflict resolution on `milestone/<E>`

When child issues `#<C1>` and `#<C2>` run in parallel and `#<C1>` auto-merges
into `milestone/<E>` first, `!<P2>` may no longer merge cleanly into
`milestone/<E>`.

- In a single-issue `bottown flow` targeting `main`, a base conflict stops
  immediately (`conflicts with main`) because only the owner merges into `main`.
- In `bottown milestone run`, because `bottown` itself advanced
  `milestone/<E>`, the scheduler **automatically runs a conflict-resolution
  round** on `#<C2>` if rounds remain (`roundsUsed < opts.Rounds`, where each
  `REQUEST_CHANGES` review and each conflict-resolution implement run counts as
  one round):
  1. Starts an `implement` run on `#<C2>` (whose prompt tells the implementer
     to merge `origin/milestone/<E>` into `issue/<C2>`, resolve conflicts, run
     checks, and push).
  2. Starts a fresh cross-family `verify` run at the new head SHA.
  3. If approved and mergeable, auto-merges `!<P2>` into `milestone/<E>`.
- If no rounds remain, `#<C2>` stops with `out of rounds (conflicts with milestone/<E>)`.

#### Handling stopped child issues

- If a child issue `#<C>` stops with `run failed`, `out of rounds`, or
  `verifier undecided`, the scheduler posts the stop comment on `#<C>`, removes
  `#<C>` from active execution, and **keeps running other independent ready
  issues** in the DAG until no ready or running child issues remain.
- Dependents of a stopped `#<C>` stay `blocked` because `#<C>` remains `open`.

### 3.6 Completing the milestone: final pull request into `main`

When the scheduler loop has no running or ready active child issues left:

- **Case A — All active child issues of `#<E>` are closed:**
  1. `bottown` checks whether an open PR from head `milestone/<E>` into base
     `main` already exists; if not, the server creates one **as `manager`**
     with:
     - `Title`: `#<E>`'s title.
     - `Head`: `"milestone/<E>"`, `Base`: `"main"`.
     - `Body`: summary of the merged child issues and PRs (plus any flagged
       removed keys), and `Closes #<E>`.
  2. Why **`manager` authors the final PR:** `main` has
     `required_approvals = 1` and `apply_to_admins = true`, and Forgejo forbids
     a PR author from approving their own pull request. Having `manager` author
     `!<final>` allows the owner (who is on `ApprovalsWhitelistUsernames` and
     `MergeWhitelistUsernames`) to review, **approve**, and merge `!<final>` in
     Forgejo directly without needing a redundant agent verify run on the
     already-verified milestone branch.
  3. `milestone run` posts a comment on `#<E>` linking the final PR `!<final>`
     and exits `0` (`ready to merge`).
  4. The final PR appears in the Web UI Inbox. The owner reviews the integrated
     diff in Forgejo, approves `!<final>`, and merges it into `main`, which
     closes `#<E>`.
- **Case B — One or more active child issues are stopped (and others remain blocked):**
  1. `milestone run` posts a summary comment on `#<E>` listing the completed,
     stopped, and still-blocked child issues.
  2. Exits non-zero. Once the owner fixes or steers the stopped child issue,
     re-running `bottown milestone run <E> --agents ...` resumes from the
     current forge state.

---

## 4. Server APIs and Policy Summary

| Component | Change |
|---|---|
| `internal/policy` | In `policy.Default`, add `issue-edit` (`PATCH`/`DELETE` `/repos/*/*/issues/*`), `pull-edit` (`PATCH`/`DELETE` `/repos/*/*/pulls/*`), and `milestones` (writes to `/repos/*/*/milestones/**`) so only the owner and `manager` can close/edit issues, change milestones, or close/retarget PRs. Add `Plan = "plan"` role; in `RunScope.DecideAPI`, `Plan` matches `Implement` except `POST /repos/*/*/issues` is denied (`role-plan-no-issues`). In `RunScope.DecideGit`, `Implement` and `Plan` pushes are confined to the run's branch. |
| `internal/server/forge.go` | `opIssueCreate` denies `policy.Plan`; `opPRCreate` allows `policy.Implement` and `policy.Plan` and uses `run.Base`. |
| `internal/provision` | Protect `milestone/*` alongside the default branch (`PushWhitelist: [owner, manager]`, `MergeWhitelist: [owner, manager]`, `ApprovalsWhitelist: [actors..., owner]`, `RequiredApprovals: 1`, `DismissStaleApprovals: true`, `BlockOnRejectedReviews: true`, `ApplyToAdmins: true`). Reconcile `implement/<actor>` and `verify/<actor>` org labels. |
| `internal/config/repo.go` | Add `MilestonesDir string` (`toml:"milestones_dir"`, default `"docs/milestones"`) to `RepoSettings`. |
| `POST /v1/runs` | Accept `kind: "plan"`. Resolve `run.Base` (`milestone/<E>` when the owner-authored issue belongs to an owner-authored epic with an active milestone branch, else default branch). |
| `POST /v1/repos/{repo}/milestones/{epic}/merge` | Owner-only endpoint used by `milestone run`: verifies cross-family approval at head SHA on `milestone/<epic>`, merges the child PR as `manager`, and closes the child issue as `manager`. Also used to create `milestone/<epic>` and open the final PR into `main` as `manager`. |

---

## 5. Phases

Each phase is one implementation issue, small enough for one implement run.
**Step 1** is Phases 1–4; **Step 2** is Phases 5–6.

### Step 1: Planning, Apply, and Web UI Visualization

1. **Phase 1 — Milestone plan format, `.bottown.toml` `milestones_dir`, and `bottown milestone check`:**
   Parser for `<milestones_dir>/<slug>.md` (TOML `+++` front matter, `##`
   sections outside fences, fenced `issue` TOML blocks), `milestones_dir` in
   `config.RepoSettings`, and the offline `bottown milestone check` CLI with
   unit tests covering valid plans, syntax errors, unknown fields, duplicate
   keys, invalid dependencies, and cycle detection.
2. **Phase 2 — Base issue/milestone policy, the `plan` run kind, prompt, CLI, and `/bottown-plan`:**
   Base policy rules (`issue-edit`, `pull-edit`, `milestones`) reserving issue
   state/body/milestone edits for the owner and `manager`; `policy.Plan` role
   enforcement (denying issue creation in both the API proxy and
   `opIssueCreate` while allowing branch push and `opPRCreate`);
   `internal/runs/prompts/plan.md`; `bottown milestone plan` /
   `bottown run start plan`; and `/bottown-plan` harness setup across all
   harnesses.
3. **Phase 3 — `bottown milestone apply`:**
   Owner-only reconciliation reading `.bottown.toml` and `<milestones_dir>/<slug>.md`
   from `main` on the forge; preflight validation (kickoff issue in an open
   Forgejo milestone, external `#N` refs, cross-issue cycle detection,
   owner-authored marker provenance and duplicate detection); `--dry-run`;
   idempotent creation and update of child implementation issues, the
   Implementation Epic issue (with task list), and Forgejo `blocked_by`
   dependencies; drift and removed-key flagging; unit tests against a fake
   client including interrupted-apply recovery.
4. **Phase 4 — Web UI Milestones view and dependency DAG diagram:**
   Host UI endpoint and Svelte view for the **Milestones** tab: epic summary
   cards, progress counts, and the interactive layered SVG dependency graph
   showing issue states, assigned actors, active runs, open PRs, and links into
   Forgejo.

### Step 2: Running an Implementation Epic

5. **Phase 5 — Protected `milestone/*` branches and automatic `milestone/<epic>` base-branch resolution:**
   Provisioning branch protection on `milestone/*` (including `manager` push
   rights and actor approvals whitelist), git push branch confinement,
   `implement/<actor>` and `verify/<actor>` labels, and automatic `run.Base`
   resolution (`milestone/<epic>` when active vs `main`, enforcing the
   owner-authored marker trust rule) across `POST /v1/runs`,
   `runs.Workspace.AddImplement`, prompts, `opPRCreate`, and `flow`.
6. **Phase 6 — `bottown milestone run`:**
   The epic DAG scheduler (`internal/milestone`), cross-family pool pairing with
   persistent label assignment, server endpoints for `manager` branch creation,
   auto-merge into `milestone/<epic>` + child issue closure (with owner/`manager`
   closure verification), automatic conflict-resolution rounds when
   `milestone/<epic>` advances, `manager`-authored final PR creation from
   `milestone/<epic>` into `main`, and Web UI start/stop controls.

---

## 6. Out of scope

- Creating or closing Forgejo milestones automatically (the owner creates the
  Forgejo milestone and files the kickoff issue in it).
- Auto-merging the final `milestone/<epic>` pull request into `main` (the owner
  reviews, approves, and merges it in Forgejo).
- Agents modifying issue state, issue bodies, milestones, dependencies, or
  labels directly.
- Cross-repository milestones.
