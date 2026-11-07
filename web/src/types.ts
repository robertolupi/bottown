export interface UnpushedCommit {
  sha: string;
  short_sha: string;
  subject: string;
  author: string;
  date: string;
  pr_number?: number;
}

export interface UpstreamStatus {
  has_upstream: boolean;
  origin_url?: string;
  fetched_at?: string;
  head_sha?: string;
  ahead_count: number;
  behind_count: number;
  unpushed_commits: UnpushedCommit[];
  error?: string;
}

export interface ForgeStatus {
  remote: string;
  repo: string;
  forge_url: string;
  default_branch: string;
  checkout_sha: string;
  forge_sha: string;
  behind_count: number;
  ahead_count: number;
}

export interface ActorWorktreeInfo {
  actor: string;
  path: string;
  head: string;
  is_dirty: boolean;
  active_run?: string;
  last_run?: string;
  ephemeral?: boolean;
  kind?: string;
  target?: string;
  // The fam guide file in a permanent worktree: current, outdated,
  // missing, foreign, none, or an error.
  guide?: string;
}

export interface HarnessCommandInfo {
  harness: string;
  installed: boolean;
  current: boolean;
  path?: string;
  error?: string;
}

export interface HealthChecklist {
  agent_instructions: {
    found: boolean;
    filename: string;
  };
  protected_main: {
    checked: boolean;
    protected: boolean;
    error?: string;
  };
  worktrees: ActorWorktreeInfo[];
  harness_commands: HarnessCommandInfo[];
  version: {
    cli_version: string;
    server_version: string;
    matches: boolean;
  };
  repo_settings: RepoSettingsCheck;
}

export interface RepoSettingsCheck {
  checked: boolean;
  found: boolean;
  file: string;
  branch: string;
  commit?: string;
  after_pull: string[];
  milestones_dir?: string;
  error?: string;
}

export type InboxItemKind =
  | 'ready_to_merge'
  | 'merge_base'
  | 'conflicts_with_base'
  | 'stopped_flow'
  | 'broken_run'
  | 'discuss_answer'
  | 'actor_filed_issue';

export interface InboxAction {
  label: string;
  command: string;
}

export interface InboxItem {
  id: string;
  kind: InboxItemKind;
  title: string;
  description: string;
  target: string;
  target_url: string;
  issue?: number;
  issue_title?: string;
  head_sha?: string;
  action?: InboxAction;
  created: string;
}

export interface ActorDetail {
  name: string;
  family: string;
  harness: string;
  // The host's default model spec ([actor_models]), when it names one.
  model?: string;
}

export interface FlowInfo {
  issue: number;
  round: number;
  max_rounds: number;
  step: string;
  label: string;
}

export interface ActiveFlowInfo {
  issue: number;
  implement: string;
  verify: string;
  round: number;
  max_rounds: number;
  step: string;
  label: string;
  waiting: boolean;
}

// One rendered line of a run's log (internal/runs.LogLine).
export type LogLineKind = 'plain' | 'text' | 'tool' | 'output' | 'error' | 'status';
export type ToolClass = 'shell' | 'read' | 'edit' | 'search' | 'forge' | 'browser' | 'other';

export interface LogLine {
  kind: LogLineKind;
  tool?: string; // tool name, for kind tool
  tool_class?: ToolClass;
  state?: 'running' | 'completed' | 'failed';
  text: string;
  detail?: string; // full assistant text when text is its first line
  parameters?: string;
  output?: string;
}

// A windowed retro's window (secrets.RetroWindow): what landed on the
// default branch after base, up to the run's head, and the wiki page the
// retro writes. Its run is on no issue; its target is <base>..<head>,
// linking the page.
export interface RetroWindow {
  since: string; // as the owner gave it: git:<ref>, last, a date or an age
  base: string;
  page: string;
}

export interface LiveRunInfo {
  id: string;
  target: string;
  target_url: string;
  window?: RetroWindow;
  issue?: number;
  issue_url?: string;
  issue_title?: string;
  pr?: number;
  pr_url?: string;
  pr_title?: string;
  milestone?: string;
  thread_key?: string;
  kind: string;
  actor: string;
  family: string;
  harness: string;
  spec?: string; // the harness's model, when the run chose one (ACTOR:MODEL)
  interactive: boolean;
  created: string;
  expires: string;
  elapsed: string;
  timeout: string;
  flow?: FlowInfo;
  log_tail: LogLine[];
  denials: Denial[];
}

// A flow's post-implement checks (the repository's e2e commands) while they
// run: not a run, but shown with a live tail like one. Its id,
// flow-<issue>-<sha>-<check>.e2e, names its log for GET /api/runs/{id}/log.
export interface LiveCheckInfo {
  id: string;
  issue?: number;
  issue_url?: string;
  issue_title?: string;
  pr?: number;
  pr_url?: string;
  sha: string;
  commands: string[];
  created: string;
  elapsed: string;
  timeout?: string; // the flow's --check-timeout
  log_tail: LogLine[];
}

export interface Denial {
  time: string;
  run?: string;
  op: string;
  method?: string;
  path?: string;
  rule?: string;
  detail?: string;
}

// An ended run's log as the history table loads it (GET /api/runs/ID/log).
export interface RunLogState {
  loading: boolean;
  lines?: LogLine[];
  error?: string;
}

export interface UsageInfo {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  cost_usd?: number;
  turns?: number;
  duration_ms?: number;
}

// An observed cause of a run's failure (secrets.Failure).
export type FailureCause =
  | 'startup'
  | 'harness_exit'
  | 'timeout'
  | 'stopped'
  | 'interrupted'
  | 'expired'
  | 'reported'
  | 'no_output';

export interface RunFailure {
  cause: FailureCause | string;
  detail?: string;
  exit_code?: number;
}

// How a history row explains a failed or revoked run (describeRunFailure).
export interface FailureDescription {
  // What bottown observed, e.g. "Startup failed": undefined when nothing
  // was recorded.
  observed?: string;
  observedDetail?: string;
  // The agent's own account: its summary.
  reported?: string;
  // Neither was recorded: the row says so rather than guessing.
  unavailable: boolean;
  // Whether a log may exist: false when the run failed before its harness
  // ran, so details say there is none.
  hasLog: boolean;
}

export interface HistoryRunInfo {
  id: string;
  target: string;
  target_url: string;
  window?: RetroWindow;
  issue?: number;
  issue_url?: string;
  issue_title?: string;
  pr?: number;
  pr_url?: string;
  pr_title?: string;
  milestone?: string;
  thread_key?: string;
  active?: boolean;
  kind: string;
  actor: string;
  family: string;
  harness: string;
  spec?: string; // the harness's model, when the run chose one (ACTOR:MODEL)
  mode: string;
  status: string; // execution: succeeded | failed | revoked | running
  // The work result the server recorded from the run's tools: approved,
  // changes_requested, commented, pr_opened. Absent when none was recorded.
  outcome?: string;
  created: string;
  ended?: string;
  duration: string;
  summary: string;
  usage?: UsageInfo;
  // What the run's ender observed of a failed or revoked run, apart from
  // summary, the agent's own account. Absent when none was recorded.
  failure?: RunFailure;
  purged: boolean;
  denials: Denial[];
  pr_head?: string;
  pr_merged?: boolean;
  pr_mergeable?: boolean;
  review_decision?: string;
  review_at_head?: boolean;
  flow_state?: FlowStateKind;
}

export type FlowStateKind =
  | 'ready_to_merge'
  | 'needs_changes'
  | 'in_progress'
  | 'needs_intervention'
  | 'needs_review'
  | 'merged'
  | 'unknown';

export interface FlowStateInfo {
  state: FlowStateKind;
  label: string;
  tone: 'success' | 'warning' | 'error' | 'accent' | 'neutral';
  icon: string;
  title: string;
}

export interface TimelineEvent {
  type: string;
  time: string;
  ended?: string;
  duration?: string;
  kind?: string;
  title: string;
  detail?: string;
  actor?: string;
  status?: string;
  outcome?: string; // run events: see HistoryRunInfo.outcome
  run_id?: string;
  url?: string;
}

export interface IssueTimeline {
  issue: number;
  pr?: number;
  pr_url?: string;
  pr_title?: string;
  milestone?: string;
  thread_key?: string;
  title: string;
  state: string;
  url: string;
  events: TimelineEvent[];
}

export interface HistoryThread {
  key: string;
  kind: 'flow' | 'discuss';
  issue?: number;
  issue_url?: string;
  issue_title?: string;
  pr?: number;
  pr_url?: string;
  pr_title?: string;
  milestone?: string;
  active: boolean;
  active_label?: string;
  status: string;
  flow_state?: FlowStateInfo;
  run_count: number;
  last_activity: string;
  actors: string[];
  duration: string;
  usage?: UsageInfo;
  summary: string;
  total_denials: number;
  runs: HistoryRunInfo[]; // constituent runs in chronological order
}

export interface FlowTimelineRunBoundary {
  entry_type: 'run_boundary';
  run: HistoryRunInfo;
  time: string;
  ended?: string;
  duration: string;
  status: string;
  events: TimelineEvent[];
}

export interface FlowTimelineStandaloneEvent {
  entry_type: 'event';
  time: string;
  event: TimelineEvent;
}

export type FlowTimelineEntry = FlowTimelineRunBoundary | FlowTimelineStandaloneEvent;

export interface UIStatusResponse {
  instance: string;
  repo: string;
  upstream: UpstreamStatus;
  forge: ForgeStatus;
  health: HealthChecklist;
  links: {
    repo: string;
    issues: string;
    pulls: string;
    commits: string;
  };
  inbox: InboxItem[];
  inbox_error?: string;
  live_runs?: LiveRunInfo[];
  live_checks?: LiveCheckInfo[];
  active_flows?: ActiveFlowInfo[];
  actors?: ActorDetail[];
}

export interface ActionResult {
  success: boolean;
  output: string;
  error?: string;
}

// GET /api/milestones (docs/design/milestones.md §2.7)
export type MilestoneNodeStatus =
  | 'blocked'
  | 'ready'
  | 'running'
  | 'in_review'
  | 'approved'
  | 'stopped'
  | 'closed'
  | 'open';

export interface MilestoneRef {
  id: number;
  title: string;
  state: string;
  url: string;
}

export interface MilestoneIssueRef {
  number: number;
  title: string;
  state: string;
  url: string;
  key?: string;
}

export interface MilestoneCounts {
  total: number;
  closed: number;
  ready: number;
  running: number;
  in_review: number;
  awaiting_merge: number;
  blocked: number;
  stopped: number;
}

export interface MilestoneNode {
  number: number;
  title: string;
  url: string;
  state: string;
  key?: string;
  external?: boolean;
  status: MilestoneNodeStatus;
  layer: number;
  depends_on: number[];
  implement?: string;
  verify?: string;
  run?: { id: string; kind: string; actor: string; created: string; elapsed: string };
  pr?: { number: number; title: string; url: string; state: string; review?: string };
  stop_reason?: string;
  flow?: { implement: string; verify: string };
}

export interface MilestoneEdge {
  from: number;
  to: number;
}

export interface MilestoneGroup {
  id: string;
  milestone?: MilestoneRef;
  epic?: MilestoneIssueRef;
  plan?: string;
  plan_file?: string;
  kickoff?: MilestoneIssueRef;
  design?: string;
  design_url?: string;
  base: string;
  removed: MilestoneIssueRef[];
  counts: MilestoneCounts;
  nodes: MilestoneNode[];
  edges: MilestoneEdge[];
  layers: number;
  error?: string;
  // The plan's `bottown milestone run` the UI started, while it runs.
  scheduler?: MilestoneScheduler;
}

// A running milestone run (internal/webui/milestone_run.go). id is what
// POST /api/actions/stop takes: milestone-<slug>. Its child flows are the
// group's nodes with a flow.
export interface MilestoneScheduler {
  id: string;
  agents: string;
  pool: string[];
  parallel: number;
  started_at: string;
  elapsed: string;
  log: string;
}

// GET /api/timeline (internal/webui/timeline.go): the window's runs laid
// out in lanes by actor and by flow thread. Every interval is clipped to
// the window; times are RFC 3339.
export interface TimelineRun {
  id: string;
  kind: string;
  actor: string;
  family?: string;
  state: string;
  status?: string;
  outcome?: string;
  created: string;
  ended?: string;
  purged: boolean;
  issue?: number;
  pr?: number;
  thread_key: string;
  interactive?: boolean;
}

export interface TimelineActorLane {
  actor: string;
  rows: string[][]; // run IDs, packed so no two in a row overlap
  runs: number;
  busy: number; // seconds
}

export type TimelineSegmentKind = 'run' | 'e2e' | 'owner_wait' | 'overhead';

export interface TimelineSegment {
  kind: TimelineSegmentKind;
  start: string;
  end: string;
  live?: boolean;
  run_id?: string;
  state?: string;
  sha?: string;
  description?: string;
  derived?: boolean;
  reason?: string;
  ended_by?: string;
}

export interface TimelineFlowLane {
  key: string;
  issue?: number;
  pr?: number;
  title?: string;
  segments: TimelineSegment[];
  runs: number;
  agent: number; // seconds
  e2e: number;
  owner_wait: number;
  overhead: number;
}

export interface TimelineData {
  window: { since: string; until: string };
  now: string;
  open: boolean;
  runs: TimelineRun[];
  actors: TimelineActorLane[];
  flows: TimelineFlowLane[];
  concurrency: { steps: { t: string; n: number }[]; peak: number; mean: number };
  incomplete?: string[];
}

// GET /api/stats (internal/stats): the per-issue/PR metric vector, the same
// vector grouped into breakdowns, the size labels' estimate against the
// measured diff, and each milestone's burndown and burnup. Every number
// carries its coverage; a measure with n 0 has no median.
export interface StatsMeasure {
  n: number;
  median?: number;
  min?: number;
  max?: number;
}

export interface StatsCell {
  key: string;
  n: number;
  lead_seconds: StatsMeasure;
  cycle_seconds: StatsMeasure;
  agent_seconds: StatsMeasure;
  implement_rounds: StatsMeasure;
  check_rounds: StatsMeasure;
  verifier_rejections: StatsMeasure;
}

export interface StatsBreakdown {
  by: string;
  cells: StatsCell[];
}

export interface StatsUsage {
  family?: string;
  runs: number;
  reported: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  cost_usd?: number;
}

export interface StatsMeasured {
  added: number;
  deleted: number;
  files: number;
}

export interface StatsIssue {
  issue?: number;
  pr?: number;
  title?: string;
  size?: string;
  milestone?: string;
  implementer?: string;
  implementer_family?: string;
  verifier?: string;
  verifier_family?: string;
  created?: string;
  merged?: string;
  lead_seconds?: number;
  cycle_seconds?: number;
  runs: number;
  agent_seconds: number;
  agent_by_kind?: Record<string, number>;
  implement_rounds: number;
  check_rounds: number;
  verifier_rejections: number;
  rejections_by_family?: Record<string, number>;
  failed_runs: number;
  failures_by_cause?: Record<string, number>;
  interactive_runs: number;
  stops: number;
  usage?: StatsUsage[];
  measured?: StatsMeasured;
}

export interface StatsSizeBucket {
  bucket: string;
  n: number;
}

export interface StatsSizeRow {
  size: string;
  n: number;
  measured: number;
  buckets: StatsSizeBucket[];
}

export interface StatsBurnPoint {
  t: string;
  total: number;
  closed: number;
  open: number;
  total_weight?: number;
  open_weight?: number;
}

export interface StatsMilestone {
  title: string;
  total: number;
  closed: number;
  weighted: boolean;
  points: StatsBurnPoint[];
}

export interface StatsData {
  window: { since: string; until: string };
  now: string;
  issues: StatsIssue[];
  breakdowns: StatsBreakdown[];
  sizes: StatsSizeRow[];
  milestones: StatsMilestone[];
  incomplete?: string[];
}
