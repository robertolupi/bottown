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
}

export interface ActionResult {
  success: boolean;
  output: string;
  error?: string;
}
