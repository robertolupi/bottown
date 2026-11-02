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
  | 'conflicts_with_main'
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
  action?: InboxAction;
  created: string;
}

export interface ActorDetail {
  name: string;
  family: string;
  harness: string;
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

export interface LiveRunInfo {
  id: string;
  target: string;
  target_url: string;
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
  interactive: boolean;
  created: string;
  expires: string;
  elapsed: string;
  timeout: string;
  flow?: FlowInfo;
  log_tail: string[];
  denials: Denial[];
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

export interface UsageInfo {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  cost_usd?: number;
  turns?: number;
  duration_ms?: number;
}

export interface HistoryRunInfo {
  id: string;
  target: string;
  target_url: string;
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
  purged: boolean;
  denials: Denial[];
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
  active_flows?: ActiveFlowInfo[];
  actors?: ActorDetail[];
}

export interface ActionResult {
  success: boolean;
  output: string;
  error?: string;
}
