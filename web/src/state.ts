import type {
  ActiveFlowInfo,
  ActorDetail,
  FailureDescription,
  FlowStateInfo,
  FlowStateKind,
  FlowTimelineEntry,
  FlowTimelineRunBoundary,
  HistoryRunInfo,
  HistoryThread,
  InboxItem,
  IssueTimeline,
  LiveRunInfo,
  LogLine,
  RunFailure,
  TimelineEvent,
  UsageInfo,
} from './types';

export interface ActorSelection {
  flowImplement: string;
  flowVerify: string;
  runActor: string;
}

// Pick default actors for the Flow and Run forms when status arrives, without
// overwriting choices the user already made. The verifier defaults to the
// first actor from another model family when one exists.
export function selectDefaultActors(
  actors: ActorDetail[] | undefined | null,
  current: ActorSelection,
): ActorSelection {
  let { flowImplement, flowVerify, runActor } = current;
  if (actors && actors.length > 0) {
    if (!flowImplement) flowImplement = actors[0].name;
    if (!flowVerify && actors.length > 1) {
      const diffFamily = actors.find((a) => a.family !== actors[0].family);
      flowVerify = diffFamily ? diffFamily.name : actors[1].name;
    }
    if (!runActor) runActor = actors[0].name;
  }
  return { flowImplement, flowVerify, runActor };
}

// Whether two chosen actors share a model family, which blocks starting a flow.
export function isSameFamilyPair(
  actors: ActorDetail[] | undefined | null,
  implement: string,
  verify: string,
): boolean {
  if (!implement || !verify || !actors) return false;
  const implActor = actors.find((a) => a.name === implement);
  const verActor = actors.find((a) => a.name === verify);
  return implActor?.family !== undefined && implActor.family === verActor?.family;
}

export interface FlowGroup {
  flow: ActiveFlowInfo;
  runs: LiveRunInfo[];
}

// Group live runs under their active flows, or synthesize flow groups from run
// flow metadata when active_flows is absent.
export function groupLiveFlows(
  activeFlows: ActiveFlowInfo[] | undefined | null,
  liveRuns: LiveRunInfo[] | undefined | null,
): FlowGroup[] {
  const flows = activeFlows || [];
  const runs = liveRuns || [];
  if (flows.length > 0) {
    return flows.map((f) => ({
      flow: f,
      runs: runs.filter((r) => r.flow && r.flow.issue === f.issue),
    }));
  }
  const groups: FlowGroup[] = [];
  const map = new Map<number, FlowGroup>();
  for (const r of runs) {
    if (r.flow) {
      let g = map.get(r.flow.issue);
      if (!g) {
        g = {
          flow: {
            issue: r.flow.issue,
            implement: '',
            verify: '',
            round: r.flow.round,
            max_rounds: r.flow.max_rounds,
            step: r.flow.step,
            label: r.flow.label,
            waiting: false,
          },
          runs: [],
        };
        map.set(r.flow.issue, g);
        groups.push(g);
      }
      g.runs.push(r);
    }
  }
  return groups;
}

export function standaloneLiveRuns(liveRuns: LiveRunInfo[] | undefined | null): LiveRunInfo[] {
  if (!liveRuns) return [];
  return liveRuns.filter((r) => !r.flow);
}

export interface HistoryFilters {
  actor?: string;
  kind?: string;
  status?: string;
  date?: string;
  search?: string;
}

export function filterHistoryRuns(
  historyRuns: HistoryRunInfo[],
  filters: HistoryFilters,
  nowTime: number,
): HistoryRunInfo[] {
  const { actor, kind, status, date, search } = filters;
  const searchLower = search ? search.toLowerCase() : '';
  return historyRuns.filter((h) => {
    if (actor && h.actor !== actor) return false;
    if (kind && h.kind !== kind) return false;
    if (status && h.status !== status) return false;
    if (date) {
      const timeMs = new Date(h.ended || h.created).getTime();
      let maxAgeMs = 0;
      switch (date) {
        case '1h': maxAgeMs = 3600 * 1000; break;
        case '24h': maxAgeMs = 24 * 3600 * 1000; break;
        case '7d': maxAgeMs = 7 * 24 * 3600 * 1000; break;
        case '30d': maxAgeMs = 30 * 24 * 3600 * 1000; break;
      }
      if (maxAgeMs > 0 && nowTime - timeMs > maxAgeMs) return false;
    }
    if (searchLower) {
      return (
        h.target.toLowerCase().includes(searchLower) ||
        h.summary.toLowerCase().includes(searchLower) ||
        h.id.toLowerCase().includes(searchLower) ||
        h.actor.toLowerCase().includes(searchLower)
      );
    }
    return true;
  });
}

export function parseLivePollSnapshot(data: unknown): {
  liveRuns: LiveRunInfo[];
  activeFlows: ActiveFlowInfo[];
} {
  if (Array.isArray(data)) {
    return { liveRuns: data as LiveRunInfo[], activeFlows: [] };
  }
  const obj = (data && typeof data === 'object' ? data : {}) as {
    live_runs?: LiveRunInfo[];
    active_flows?: ActiveFlowInfo[];
  };
  return {
    liveRuns: obj.live_runs || [],
    activeFlows: obj.active_flows || [],
  };
}

export function nextPollDelayMs(
  liveRuns: LiveRunInfo[] | undefined | null,
  activeFlows: ActiveFlowInfo[] | undefined | null,
): number {
  const hasActive =
    Boolean(liveRuns && liveRuns.length > 0) ||
    Boolean(activeFlows && activeFlows.length > 0);
  return hasActive ? 2500 : 10000;
}

// actorSpec joins an actor and an optional model as ACTOR[:SPEC]; an empty
// model leaves the host's default ([actor_models]) to the CLI.
export function actorSpec(actor: string, model: string | undefined): string {
  const m = (model ?? '').trim();
  return m ? `${actor}:${m}` : actor;
}

// actorModelPlaceholder is a model field's hint: the host's default for the
// actor, or the harness's own when the host names none.
export function actorModelPlaceholder(actors: ActorDetail[] | undefined | null, name: string): string {
  const model = actors?.find((a) => a.name === name)?.model;
  return model ? `default: ${model}` : 'harness default';
}

export interface FlowActionPayload {
  issue: number;
  implement: string;
  verify: string;
  rounds: number;
  timeout: string;
}

export function buildFlowActionPayload(input: {
  issue: number | string;
  implement: string;
  implementModel?: string;
  verify: string;
  verifyModel?: string;
  rounds: number | string;
  timeout: string;
}): FlowActionPayload {
  return {
    issue: Number(input.issue),
    implement: actorSpec(input.implement, input.implementModel),
    verify: actorSpec(input.verify, input.verifyModel),
    rounds: Number(input.rounds) || 3,
    timeout: input.timeout || '60m',
  };
}

export interface RunActionPayload {
  kind: 'implement' | 'verify' | 'discuss';
  target: number;
  actor: string;
  question?: string;
  allow_same_family?: boolean;
}

export function buildRunActionPayload(input: {
  kind: 'implement' | 'verify' | 'discuss';
  target: number | string;
  actor: string;
  model?: string;
  question: string;
  allowSameFamily: boolean;
}): RunActionPayload {
  return {
    kind: input.kind,
    target: Number(input.target),
    actor: actorSpec(input.actor, input.model),
    question: input.kind === 'discuss' ? input.question : undefined,
    allow_same_family: input.kind === 'verify' ? input.allowSameFamily : undefined,
  };
}

export function filterInboxItems(
  inbox: InboxItem[] | undefined | null,
  showActorFiled: boolean,
): InboxItem[] {
  const items = inbox || [];
  return showActorFiled ? items : items.filter((item) => item.kind !== 'actor_filed_issue');
}

export type ResolvedInboxAction =
  | { modal: 'gc'; includeFailed: boolean }
  | { modal: 'stop'; stopRunId: string }
  | null;

export function resolveInboxAction(item: Pick<InboxItem, 'target' | 'action'>): ResolvedInboxAction {
  const cmd = item.action?.command;
  if (!cmd) return null;
  if (cmd.includes('gc')) {
    return { modal: 'gc', includeFailed: cmd.includes('--failed') };
  }
  if (cmd.includes('stop')) {
    return { modal: 'stop', stopRunId: item.target };
  }
  return null;
}

export function parseIssueNumber(target: string): number | null {
  const num = parseInt(target.replace(/^[#!]/, ''), 10);
  return !isNaN(num) && num > 0 ? num : null;
}

function parseDurationStringMs(dur: string | undefined): number {
  if (!dur || dur === '-') return 0;
  let total = 0;
  const h = dur.match(/(\d+)h/);
  const m = dur.match(/(\d+)m/);
  const s = dur.match(/(\d+)s/);
  if (h) total += parseInt(h[1], 10) * 3600 * 1000;
  if (m) total += parseInt(m[1], 10) * 60 * 1000;
  if (s) total += parseInt(s[1], 10) * 1000;
  return total;
}

export interface OutcomeBadge {
  label: string;
  // A glyph shown before the label, so tones are not told apart by color alone.
  icon: string;
  tone: 'success' | 'warning' | 'error' | 'accent' | 'neutral';
  title: string;
}

const outcomeBadges: Record<string, Omit<OutcomeBadge, 'title'>> = {
  approved: { label: 'Approved', icon: '✓', tone: 'success' },
  changes_requested: { label: 'Changes requested', icon: '✗', tone: 'warning' },
  commented: { label: 'Commented', icon: '…', tone: 'neutral' },
  pr_opened: { label: 'PR opened', icon: '↗', tone: 'accent' },
};

// runOutcome describes a run's work outcome, apart from its execution status:
// only what the server recorded (a review verdict, an opened PR), never one
// guessed from the summary. A run that recorded none says so, in a neutral
// tone; a run still going has no outcome yet (null).
export function runOutcome(run: { status?: string; outcome?: string; ended?: string }): OutcomeBadge | null {
  if (run.outcome) {
    const known = outcomeBadges[run.outcome];
    const badge = known || { label: run.outcome.replace(/_/g, ' '), icon: '•', tone: 'neutral' as const };
    return { ...badge, title: `Outcome: ${badge.label} (recorded by the server)` };
  }
  if (!run.ended || run.status === 'running') return null;
  return { label: 'None recorded', icon: '–', tone: 'neutral', title: 'Outcome: none recorded for this run' };
}

const failureLabels: Record<string, string> = {
  startup: 'Startup failed',
  timeout: 'Timed out',
  stopped: 'Stopped by the owner',
  interrupted: 'Interrupted',
  expired: 'Key expired while live',
  reported: 'Agent ended the run as failed',
};

// Causes whose detail adds to their label; the others' repeats it.
const failureDetailed = new Set(['startup', 'timeout', 'interrupted', 'expired']);

// describeRunFailure explains a failed or revoked run for the history: what
// bottown observed (the recorded failure) apart from what the agent
// reported (its summary), or that neither was recorded. It never derives a
// cause from the summary. Other runs, and runs still going, get null.
export function describeRunFailure(run: {
  status?: string;
  ended?: string;
  summary?: string;
  failure?: RunFailure;
}): FailureDescription | null {
  if (run.status !== 'failed' && run.status !== 'revoked') return null;
  const f = run.failure;
  let observed: string | undefined;
  let observedDetail: string | undefined;
  if (f?.cause) {
    if (f.cause === 'harness_exit') {
      observed = f.exit_code !== undefined && f.exit_code !== null
        ? `Harness exited with code ${f.exit_code}`
        : 'Harness exited with an error';
    } else {
      observed = failureLabels[f.cause] || f.cause.replace(/_/g, ' ');
    }
    if (f.detail && (failureDetailed.has(f.cause) || !failureLabels[f.cause] && f.cause !== 'harness_exit')) {
      observedDetail = f.detail;
    }
  }
  const reported = run.summary?.trim() || undefined;
  return {
    observed,
    observedDetail,
    reported,
    unavailable: !observed && !reported,
    hasLog: f?.cause !== 'startup',
  };
}

// failureText is a failure description on one line, for titles and search.
export function failureText(d: FailureDescription): string {
  const parts: string[] = [];
  if (d.observed) parts.push(`Observed: ${d.observed}${d.observedDetail ? ` (${d.observedDetail})` : ''}`);
  if (d.reported) parts.push(`Agent reported: ${d.reported}`);
  if (d.unavailable) parts.push('Reason unavailable');
  return parts.join(' · ');
}

// deriveFlowState derives a concise flow-level state (Ready to merge, Needs changes,
// In progress, Needs intervention, Needs review, Merged, Unknown) from authoritative
// flow, PR, and review data. Stale approvals at an older head do not imply readiness.
// Missing or read-failed data remains explicitly neutral/unknown, never inferred from summaries.
export function deriveFlowState(input: {
  active: boolean;
  active_label?: string;
  status: string;
  runs: HistoryRunInfo[];
  latestRun?: HistoryRunInfo;
}): FlowStateInfo {
  const { active, active_label, status, latestRun } = input;

  if (active) {
    return {
      state: 'in_progress',
      label: 'In progress',
      tone: 'accent',
      icon: '●',
      title: active_label ? `Next action: In progress — ${active_label}` : 'Next action: In progress — flow is currently active',
    };
  }

  // 1. Authoritative server-provided flow_state if present on latestRun
  if (latestRun?.flow_state) {
    switch (latestRun.flow_state) {
      case 'ready_to_merge':
        return {
          state: 'ready_to_merge',
          label: 'Ready to merge',
          tone: 'success',
          icon: '✓',
          title: latestRun.pr ? `Next action: Ready to merge — PR !${latestRun.pr} is approved at current head` : 'Next action: Ready to merge — approved at current head',
        };
      case 'needs_changes':
        return {
          state: 'needs_changes',
          label: 'Needs changes',
          tone: 'warning',
          icon: '⟲',
          title: latestRun.pr ? `Next action: Needs changes — changes requested on PR !${latestRun.pr}` : 'Next action: Needs changes',
        };
      case 'needs_review':
        return {
          state: 'needs_review',
          label: 'Needs review',
          tone: 'warning',
          icon: '👁',
          title: latestRun.pr ? `Next action: Needs review — PR !${latestRun.pr} current head is not approved` : 'Next action: Needs review — current head is not approved',
        };
      case 'needs_intervention':
        return {
          state: 'needs_intervention',
          label: 'Needs intervention',
          tone: 'error',
          icon: '!',
          title: 'Next action: Needs intervention — flow stopped and requires owner intervention',
        };
      case 'merged':
        return {
          state: 'merged',
          label: 'Merged',
          tone: 'neutral',
          icon: '⎇',
          title: latestRun.pr ? `Next action: Merged — PR !${latestRun.pr} has merged into main` : 'Next action: Merged — PR merged into main',
        };
      case 'unknown':
        return {
          state: 'unknown',
          label: 'Unknown',
          tone: 'neutral',
          icon: '?',
          title: 'Next action: Unknown — current PR and review data unavailable',
        };
    }
  }

  // 2. Authoritative PR and review fields
  if (latestRun?.pr_merged) {
    return {
      state: 'merged',
      label: 'Merged',
      tone: 'neutral',
      icon: '⎇',
      title: latestRun.pr ? `Next action: Merged — PR !${latestRun.pr} has merged into main` : 'Next action: Merged — PR merged into main',
    };
  }

  if (latestRun?.pr_mergeable === false) {
    return {
      state: 'needs_intervention',
      label: 'Needs intervention',
      tone: 'error',
      icon: '!',
      title: latestRun.pr ? `Next action: Needs intervention — PR !${latestRun.pr} conflicts with main` : 'Next action: Needs intervention — conflicts with main',
    };
  }

  if (latestRun?.review_decision) {
    switch (latestRun.review_decision) {
      case 'approved':
        if (latestRun.review_at_head === true) {
          return {
            state: 'ready_to_merge',
            label: 'Ready to merge',
            tone: 'success',
            icon: '✓',
            title: latestRun.pr ? `Next action: Ready to merge — PR !${latestRun.pr} is approved at current head` : 'Next action: Ready to merge',
          };
        }
        // Stale approval at an older head: must NOT imply readiness!
        return {
          state: 'needs_review',
          label: 'Needs review',
          tone: 'warning',
          icon: '👁',
          title: latestRun.pr ? `Next action: Needs review — PR !${latestRun.pr} approval is stale; current head is not approved` : 'Next action: Needs review — current head is not approved',
        };
      case 'changes_requested':
        return {
          state: 'needs_changes',
          label: 'Needs changes',
          tone: 'warning',
          icon: '⟲',
          title: latestRun.pr ? `Next action: Needs changes — changes requested on PR !${latestRun.pr}` : 'Next action: Needs changes',
        };
      case 'commented':
        return {
          state: 'needs_intervention',
          label: 'Needs intervention',
          tone: 'error',
          icon: '!',
          title: 'Next action: Needs intervention — verifier ended review with undecided COMMENT',
        };
      case 'needs_review':
        return {
          state: 'needs_review',
          label: 'Needs review',
          tone: 'warning',
          icon: '👁',
          title: latestRun.pr ? `Next action: Needs review — PR !${latestRun.pr} needs review` : 'Next action: Needs review',
        };
    }
  }

  // 3. Execution failure on latestRun or thread status
  if (status === 'failed' || status === 'revoked' || latestRun?.status === 'failed' || latestRun?.status === 'revoked') {
    return {
      state: 'needs_intervention',
      label: 'Needs intervention',
      tone: 'error',
      icon: '!',
      title: 'Next action: Needs intervention — run failed and requires owner intervention',
    };
  }

  // 4. Missing / read-failed data: explicitly neutral/unknown, never inferred from summaries
  return {
    state: 'unknown',
    label: 'Unknown',
    tone: 'neutral',
    icon: '?',
    title: 'Next action: Unknown — current PR and review data unavailable',
  };
}

export function formatDurationMs(ms: number): string {
  if (!ms || ms <= 0) return '-';
  const totalSec = Math.round(ms / 1000);
  if (totalSec <= 0) return '0s';
  const h = Math.floor(totalSec / 3600);
  const m = Math.floor((totalSec % 3600) / 60);
  const s = totalSec % 60;
  if (h > 0) return `${h}h${m}m${s}s`;
  if (m > 0) return `${m}m${s}s`;
  return `${s}s`;
}

export function aggregateThreadUsage(runs: HistoryRunInfo[]): UsageInfo | undefined {
  let hasUsage = false;
  const sum: UsageInfo = {};
  for (const r of runs) {
    if (!r.usage) continue;
    const u = r.usage;
    if (u.input_tokens !== undefined && u.input_tokens !== null) {
      hasUsage = true;
      sum.input_tokens = (sum.input_tokens ?? 0) + u.input_tokens;
    }
    if (u.output_tokens !== undefined && u.output_tokens !== null) {
      hasUsage = true;
      sum.output_tokens = (sum.output_tokens ?? 0) + u.output_tokens;
    }
    if (u.cache_read_tokens !== undefined && u.cache_read_tokens !== null) {
      hasUsage = true;
      sum.cache_read_tokens = (sum.cache_read_tokens ?? 0) + u.cache_read_tokens;
    }
    if (u.cache_write_tokens !== undefined && u.cache_write_tokens !== null) {
      hasUsage = true;
      sum.cache_write_tokens = (sum.cache_write_tokens ?? 0) + u.cache_write_tokens;
    }
    if (u.cost_usd !== undefined && u.cost_usd !== null) {
      hasUsage = true;
      sum.cost_usd = (sum.cost_usd ?? 0) + u.cost_usd;
    }
    if (u.turns !== undefined && u.turns !== null) {
      hasUsage = true;
      sum.turns = (sum.turns ?? 0) + u.turns;
    }
    if (u.duration_ms !== undefined && u.duration_ms !== null) {
      hasUsage = true;
      sum.duration_ms = (sum.duration_ms ?? 0) + u.duration_ms;
    }
  }
  return hasUsage ? sum : undefined;
}

function parseThreadKeyIssueAndPR(threadKey?: string): { issue?: number; pr?: number } {
  if (!threadKey) return {};
  const issueMatch = threadKey.match(/(?:^|:)issue:(\d+)(?:$|:)/);
  const prMatch = threadKey.match(/(?:^|:)pr:(\d+)(?:$|:)/);
  const issue = issueMatch ? parseInt(issueMatch[1], 10) : undefined;
  const pr = prMatch ? parseInt(prMatch[1], 10) : undefined;
  return {
    issue: issue && issue > 0 ? issue : undefined,
    pr: pr && pr > 0 ? pr : undefined,
  };
}

function resolveRunIssueAndPR(run: HistoryRunInfo): { issue?: number; pr?: number } {
  const fromKey = parseThreadKeyIssueAndPR(run.thread_key);
  let issue = run.issue && run.issue > 0 ? run.issue : undefined;
  let pr = run.pr && run.pr > 0 ? run.pr : undefined;
  if (!issue && run.target?.startsWith('#')) {
    issue = parseIssueNumber(run.target) ?? undefined;
  }
  if (!pr && run.target?.startsWith('!')) {
    pr = parseIssueNumber(run.target) ?? undefined;
  }
  if (!issue && fromKey.issue) {
    issue = fromKey.issue;
  }
  if (!pr && fromKey.pr) {
    pr = fromKey.pr;
  }
  return { issue, pr };
}

function liveRunToHistoryRun(lr: LiveRunInfo): HistoryRunInfo {
  const fromKey = parseThreadKeyIssueAndPR(lr.thread_key);
  const issue =
    lr.issue ||
    lr.flow?.issue ||
    (lr.target?.startsWith('#') ? (parseIssueNumber(lr.target) ?? undefined) : undefined) ||
    fromKey.issue;
  const pr =
    lr.pr ||
    (lr.target?.startsWith('!') ? (parseIssueNumber(lr.target) ?? undefined) : undefined) ||
    fromKey.pr;
  return {
    id: lr.id,
    target: lr.target || (pr ? `!${pr}` : issue ? `#${issue}` : ''),
    target_url: lr.target_url || lr.pr_url || lr.issue_url || '',
    issue,
    issue_url: lr.issue_url || (lr.target?.startsWith('#') ? lr.target_url : undefined),
    issue_title: lr.issue_title,
    pr,
    pr_url: lr.pr_url || (lr.target?.startsWith('!') ? lr.target_url : undefined),
    pr_title: lr.pr_title,
    milestone: lr.milestone,
    thread_key: lr.thread_key,
    active: true,
    kind: lr.kind || 'implement',
    actor: lr.actor || '',
    family: lr.family || '',
    harness: lr.harness || '',
    spec: lr.spec,
    mode: lr.interactive ? 'interactive' : 'batch',
    status: 'running',
    created: lr.created,
    ended: undefined,
    duration: lr.elapsed || '',
    summary: '',
    purged: false,
    denials: lr.denials || [],
  };
}

// Group history runs into flow-centric threads (grouping implement & verify runs
// for the same issue and PR, preserving pre-PR implementation runs when their PR
// is created, separating distinct PRs into separate threads, and keeping discuss
// runs as standalone entries).
export function groupHistoryThreads(
  historyRuns: HistoryRunInfo[],
  liveRuns?: LiveRunInfo[] | null,
  activeFlows?: ActiveFlowInfo[] | null,
): HistoryThread[] {
  const live = liveRuns || [];
  const flows = activeFlows || [];

  const liveById = new Map<string, LiveRunInfo>();
  for (const lr of live) {
    if (lr && lr.id) liveById.set(lr.id, lr);
  }
  const seenRunIds = new Set<string>();
  const combinedRuns: HistoryRunInfo[] = [];
  for (const r of historyRuns || []) {
    if (!r || !r.id) continue;
    seenRunIds.add(r.id);
    const matchingLive = liveById.get(r.id);
    if (matchingLive) {
      const converted = liveRunToHistoryRun(matchingLive);
      combinedRuns.push({
        ...r,
        issue: r.issue || converted.issue,
        issue_url: r.issue_url || converted.issue_url,
        issue_title: r.issue_title || converted.issue_title,
        pr: r.pr || converted.pr,
        pr_url: r.pr_url || converted.pr_url,
        pr_title: r.pr_title || converted.pr_title,
        milestone: r.milestone || converted.milestone,
        thread_key: converted.thread_key || r.thread_key,
        active: true,
      });
    } else {
      combinedRuns.push(r);
    }
  }
  for (const lr of live) {
    if (!lr || !lr.id || seenRunIds.has(lr.id)) continue;
    seenRunIds.add(lr.id);
    combinedRuns.push(liveRunToHistoryRun(lr));
  }

  if (combinedRuns.length === 0) return [];

  let maxKnownMs = 0;
  for (const r of combinedRuns) {
    const cMs = r.created ? new Date(r.created).getTime() : 0;
    const eMs = r.ended ? new Date(r.ended).getTime() : 0;
    if (!isNaN(cMs) && cMs > maxKnownMs) maxKnownMs = cMs;
    if (!isNaN(eMs) && eMs > maxKnownMs) maxKnownMs = eMs;
  }

  // Normalize runs in chronological order (oldest first) so we can link pre-PR
  // implement runs to the PR created for that flow window.
  const enriched = combinedRuns.map((r) => {
    const { issue, pr } = resolveRunIssueAndPR(r);
    const isLiveRun = !r.ended && (Boolean(r.active) || r.status === 'running');
    const rawCreatedMs = r.created ? new Date(r.created).getTime() : NaN;
    const createdMs =
      !isNaN(rawCreatedMs) && rawCreatedMs > 0
        ? rawCreatedMs
        : isLiveRun
          ? maxKnownMs + 1
          : 0;
    const rawEndedMs = r.ended ? new Date(r.ended).getTime() : NaN;
    const endedMs =
      !isNaN(rawEndedMs) && rawEndedMs > 0
        ? rawEndedMs
        : isLiveRun
          ? Math.max(createdMs, maxKnownMs + 1)
          : createdMs;
    return {
      run: r,
      issue,
      pr,
      createdMs,
      endedMs,
    };
  });

  // Resolve issue from PR when a verify/live run only specifies PR.
  const issueByPR = new Map<number, number>();
  for (const item of enriched) {
    if (item.issue && item.pr && !issueByPR.has(item.pr)) {
      issueByPR.set(item.pr, item.issue);
    }
  }
  for (const item of enriched) {
    if (!item.issue && item.pr && issueByPR.has(item.pr)) {
      item.issue = issueByPR.get(item.pr);
    }
  }

  const chronological = [...enriched].sort((a, b) => {
    if (a.createdMs !== b.createdMs) return a.createdMs - b.createdMs;
    return a.run.id.localeCompare(b.run.id);
  });

  // Collect PR windows per issue from both history runs and live runs.
  interface PRWindow {
    pr: number;
    pr_url?: string;
    pr_title?: string;
    milestone?: string;
    firstMs: number;
    lastMs: number;
  }
  const prWindowsByIssue = new Map<number, PRWindow[]>();
  const recordPRWindow = (
    issue: number,
    pr: number,
    timeMs: number,
    endMs: number,
    pr_url?: string,
    pr_title?: string,
    milestone?: string,
  ) => {
    let list = prWindowsByIssue.get(issue);
    if (!list) {
      list = [];
      prWindowsByIssue.set(issue, list);
    }
    let win = list.find((w) => w.pr === pr);
    if (!win) {
      win = { pr, pr_url, pr_title, milestone, firstMs: timeMs, lastMs: endMs };
      list.push(win);
    } else {
      if (timeMs < win.firstMs) win.firstMs = timeMs;
      if (endMs > win.lastMs) win.lastMs = endMs;
      if (!win.pr_url && pr_url) win.pr_url = pr_url;
      if (!win.pr_title && pr_title) win.pr_title = pr_title;
      if (!win.milestone && milestone) win.milestone = milestone;
    }
  };

  for (const item of chronological) {
    if (item.run.kind !== 'discuss' && item.issue && item.pr) {
      recordPRWindow(
        item.issue,
        item.pr,
        item.createdMs,
        item.endedMs,
        item.run.pr_url || (item.run.target.startsWith('!') ? item.run.target_url : undefined),
        item.run.pr_title,
        item.run.milestone,
      );
    }
  }
  for (const lr of live) {
    if (lr.kind === 'discuss') continue;
    const issue = lr.issue || lr.flow?.issue || (lr.target?.startsWith('#') ? parseIssueNumber(lr.target) ?? undefined : undefined);
    const pr = lr.pr || (lr.target?.startsWith('!') ? parseIssueNumber(lr.target) ?? undefined : undefined);
    if (issue && pr) {
      const tMs = new Date(lr.created).getTime() || Date.now();
      recordPRWindow(
        issue,
        pr,
        tMs,
        tMs,
        lr.pr_url || (lr.target?.startsWith('!') ? lr.target_url : undefined),
        lr.pr_title,
        lr.milestone,
      );
    }
  }

  for (const list of prWindowsByIssue.values()) {
    list.sort((a, b) => a.firstMs - b.firstMs || a.pr - b.pr);
  }

  interface ThreadBuilder {
    key: string;
    kind: 'flow' | 'discuss';
    issue?: number;
    issue_url?: string;
    issue_title?: string;
    pr?: number;
    pr_url?: string;
    pr_title?: string;
    milestone?: string;
    runs: HistoryRunInfo[];
  }

  const threadMap = new Map<string, ThreadBuilder>();
  const threads: ThreadBuilder[] = [];

  for (const item of chronological) {
    const r = item.run;
    let { issue, pr } = item;
    let pr_url = r.pr_url || (r.target?.startsWith('!') ? r.target_url : undefined);
    let pr_title = r.pr_title;
    let issue_url = r.issue_url || (r.target?.startsWith('#') ? r.target_url : undefined);
    let issue_title = r.issue_title;
    let milestone = r.milestone || undefined;

    if (r.kind === 'discuss') {
      const key = r.thread_key || `discuss:${r.id}`;
      const normalizedRun: HistoryRunInfo = {
        ...r,
        issue,
        issue_url,
        issue_title,
        milestone,
        thread_key: key,
      };
      const tb: ThreadBuilder = {
        key,
        kind: 'discuss',
        issue,
        issue_url,
        issue_title,
        milestone,
        runs: [normalizedRun],
      };
      threadMap.set(key, tb);
      threads.push(tb);
      continue;
    }

    // Pre-PR implement run: associate with the PR created for its flow window if one exists.
    if (issue && !pr) {
      const windows = prWindowsByIssue.get(issue);
      if (windows && windows.length > 0) {
        for (let i = 0; i < windows.length; i++) {
          const w = windows[i];
          const prevLastMs = i > 0 ? windows[i - 1].lastMs : -Infinity;
          const isLastWindow = i === windows.length - 1;
          const isLiveRun = !r.ended && (Boolean(r.active) || r.status === 'running');
          const withinWindow =
            item.createdMs > prevLastMs &&
            (item.createdMs <= w.lastMs || (isLastWindow && isLiveRun && !r.thread_key));
          if (withinWindow) {
            pr = w.pr;
            pr_url = pr_url || w.pr_url;
            pr_title = pr_title || w.pr_title;
            milestone = milestone || w.milestone;
            break;
          }
        }
      }
    }

    let key: string;
    if (issue && pr) {
      key = `flow:issue:${issue}:pr:${pr}`;
    } else if (issue) {
      key = `flow:issue:${issue}`;
    } else if (pr) {
      key = `flow:pr:${pr}`;
    } else {
      key = r.thread_key || `flow:run:${r.id}`;
    }

    const normalizedRun: HistoryRunInfo = {
      ...r,
      issue,
      issue_url,
      issue_title,
      pr,
      pr_url,
      pr_title,
      milestone,
      thread_key: key,
    };

    let tb = threadMap.get(key);
    if (!tb) {
      tb = {
        key,
        kind: 'flow',
        issue,
        issue_url,
        issue_title,
        pr,
        pr_url,
        pr_title,
        milestone,
        runs: [],
      };
      threadMap.set(key, tb);
      threads.push(tb);
    } else {
      if (!tb.issue && issue) tb.issue = issue;
      if (!tb.issue_url && issue_url) tb.issue_url = issue_url;
      if (!tb.issue_title && issue_title) tb.issue_title = issue_title;
      if (!tb.pr && pr) tb.pr = pr;
      if (!tb.pr_url && pr_url) tb.pr_url = pr_url;
      if (!tb.pr_title && pr_title) tb.pr_title = pr_title;
      if (!tb.milestone && milestone) tb.milestone = milestone;
    }
    tb.runs.push(normalizedRun);
  }

  // Determine the latest flow thread for each issue so activeFlows / issue-level liveRuns
  // highlight the active thread for that issue without highlighting closed earlier PR threads.
  const latestFlowKeyByIssue = new Map<number, string>();
  for (const tb of threads) {
    if (tb.kind === 'flow' && tb.issue) {
      latestFlowKeyByIssue.set(tb.issue, tb.key);
    }
  }

  const built: HistoryThread[] = threads.map((tb) => {
    // Runs within tb.runs are already in chronological order (oldest -> newest).
    const runs = tb.runs.map((r) => ({
      ...r,
      issue: r.issue ?? tb.issue,
      issue_url: r.issue_url ?? tb.issue_url,
      issue_title: r.issue_title ?? tb.issue_title,
      pr: r.pr ?? tb.pr,
      pr_url: r.pr_url ?? tb.pr_url,
      pr_title: r.pr_title ?? tb.pr_title,
      milestone: r.milestone ?? tb.milestone,
      thread_key: tb.key,
    }));
    const latestRun = runs[runs.length - 1];

    // Check active state from constituent runs, liveRuns, and activeFlows.
    let active = runs.some((r) => Boolean(r.active) || r.status === 'running');
    let activeLabel: string | undefined;

    if (tb.kind === 'flow') {
      const matchingLiveRun = live.find((lr) => {
        if (lr.kind === 'discuss') return false;
        if (lr.thread_key && lr.thread_key === tb.key) return true;
        const lrIssue = lr.issue || lr.flow?.issue || (lr.target?.startsWith('#') ? parseIssueNumber(lr.target) ?? undefined : undefined);
        const lrPR = lr.pr || (lr.target?.startsWith('!') ? parseIssueNumber(lr.target) ?? undefined : undefined);
        if (tb.pr && lrPR === tb.pr) return true;
        if (tb.issue && lrIssue === tb.issue && !lrPR && latestFlowKeyByIssue.get(tb.issue) === tb.key) {
          return true;
        }
        return false;
      });
      if (matchingLiveRun) {
        active = true;
        if (matchingLiveRun.flow?.label) {
          activeLabel = matchingLiveRun.flow.label;
        } else {
          activeLabel = `${matchingLiveRun.kind} (${matchingLiveRun.actor})`;
        }
      }

      if (tb.issue && latestFlowKeyByIssue.get(tb.issue) === tb.key) {
        const matchingFlow = flows.find((f) => f.issue === tb.issue);
        if (matchingFlow) {
          active = true;
          activeLabel = matchingFlow.label || matchingFlow.step || activeLabel;
        }
      }
    } else {
      const matchingDiscussLive = live.find((lr) => lr.id === latestRun.id);
      if (matchingDiscussLive) {
        active = true;
      }
    }

    const actors: string[] = [];
    let totalDurationMs = 0;
    let totalDenials = 0;
    for (const r of runs) {
      if (r.actor && !actors.includes(r.actor)) {
        actors.push(r.actor);
      }
      totalDurationMs += r.usage?.duration_ms || parseDurationStringMs(r.duration);
      totalDenials += r.denials ? r.denials.length : 0;
    }

    const lastActivity = latestRun.ended || latestRun.created;
    const status = active && latestRun.status !== 'running' ? 'running' : latestRun.status;
    let summary = latestRun.summary || '';
    if (!summary) {
      for (let i = runs.length - 1; i >= 0; i--) {
        if (runs[i].summary) {
          summary = runs[i].summary;
          break;
        }
      }
    }

    let flowState: FlowStateInfo | undefined;
    if (tb.kind === 'flow') {
      flowState = deriveFlowState({
        active,
        active_label: activeLabel,
        status,
        runs,
        latestRun,
      });
    }

    return {
      key: tb.key,
      kind: tb.kind,
      issue: tb.issue,
      issue_url: tb.issue_url,
      issue_title: tb.issue_title,
      pr: tb.pr,
      pr_url: tb.pr_url,
      pr_title: tb.pr_title,
      milestone: tb.milestone || undefined,
      active,
      active_label: activeLabel,
      status,
      flow_state: flowState,
      run_count: runs.length,
      last_activity: lastActivity,
      actors,
      duration: runs.length === 1 ? latestRun.duration : formatDurationMs(totalDurationMs),
      usage: aggregateThreadUsage(runs),
      summary,
      total_denials: totalDenials,
      runs,
    };
  });

  // Sort threads newest last_activity first (matching Run History ordering).
  built.sort((a, b) => {
    const timeA = new Date(a.last_activity).getTime() || 0;
    const timeB = new Date(b.last_activity).getTime() || 0;
    if (timeA !== timeB) return timeB - timeA;
    return b.key.localeCompare(a.key);
  });

  return built;
}

export function filterHistoryThreads(
  threads: HistoryThread[],
  filters: HistoryFilters,
  nowTime: number,
): HistoryThread[] {
  const { actor, kind, status, date, search } = filters;
  const searchLower = search ? search.toLowerCase() : '';
  return threads.filter((t) => {
    if (actor && !t.actors.includes(actor) && !t.runs.some((r) => r.actor === actor)) {
      return false;
    }
    if (kind) {
      if (kind === 'discuss' && t.kind !== 'discuss') return false;
      if (kind !== 'discuss' && (t.kind !== 'flow' || !t.runs.some((r) => r.kind === kind))) {
        return false;
      }
    }
    if (status && t.status !== status && !t.runs.some((r) => r.status === status)) {
      return false;
    }
    if (date) {
      const timeMs = new Date(t.last_activity).getTime();
      let maxAgeMs = 0;
      switch (date) {
        case '1h': maxAgeMs = 3600 * 1000; break;
        case '24h': maxAgeMs = 24 * 3600 * 1000; break;
        case '7d': maxAgeMs = 7 * 24 * 3600 * 1000; break;
        case '30d': maxAgeMs = 30 * 24 * 3600 * 1000; break;
      }
      if (maxAgeMs > 0 && nowTime - timeMs > maxAgeMs) return false;
    }
    if (searchLower) {
      const mainLine = formatThreadMainLine(t).text.toLowerCase();
      if (mainLine.includes(searchLower)) return true;
      if (t.summary.toLowerCase().includes(searchLower)) return true;
      if (t.milestone && t.milestone.toLowerCase().includes(searchLower)) return true;
      return t.runs.some(
        (r) =>
          r.target.toLowerCase().includes(searchLower) ||
          r.summary.toLowerCase().includes(searchLower) ||
          (r.failure?.detail || '').toLowerCase().includes(searchLower) ||
          r.id.toLowerCase().includes(searchLower) ||
          r.actor.toLowerCase().includes(searchLower),
      );
    }
    return true;
  });
}

export interface ThreadMainLine {
  issueLabel?: string;
  issueTitle?: string;
  issueUrl?: string;
  prLabel?: string;
  prTitle?: string;
  prUrl?: string;
  milestone?: string;
  text: string;
}

// Build the main line metadata for a thread row: shows issue, PR, and milestone
// together, and omits milestone when none is assigned.
export function formatThreadMainLine(thread: HistoryThread): ThreadMainLine {
  const issueLabel = thread.issue ? `#${thread.issue}` : undefined;
  const prLabel = thread.pr ? `!${thread.pr}` : undefined;
  const milestone = thread.milestone && thread.milestone.trim() !== '' ? thread.milestone.trim() : undefined;

  const parts: string[] = [];
  if (issueLabel) {
    parts.push(thread.issue_title ? `${issueLabel} ${thread.issue_title}` : issueLabel);
  }
  if (prLabel) {
    parts.push(thread.pr_title && thread.pr_title !== thread.issue_title ? `${prLabel} ${thread.pr_title}` : prLabel);
  }
  if (parts.length === 0 && thread.runs.length > 0) {
    parts.push(thread.runs[thread.runs.length - 1].target);
  }
  if (milestone) {
    parts.push(milestone);
  }

  return {
    issueLabel,
    issueTitle: thread.issue_title,
    issueUrl: thread.issue_url,
    prLabel,
    prTitle: thread.pr_title,
    prUrl: thread.pr_url,
    milestone,
    text: parts.join(' · '),
  };
}

// Enforce at-most-one expanded flow at a time. Clicking the open flow closes it;
// clicking another flow opens it and closes the previously open flow.
export function toggleExpandedThread(
  currentKey: string | null,
  clickedKey: string,
): string | null {
  return currentKey === clickedKey ? null : clickedKey;
}

// Preserve the selected/expanded flow across live updates without changing user
// selection, including when a pre-PR thread (`flow:issue:N`) transitions to a
// PR thread (`flow:issue:N:pr:M`) once its PR is created.
export function reconcileExpandedThread(
  currentKey: string | null,
  prevThreads: HistoryThread[],
  nextThreads: HistoryThread[],
): string | null {
  if (!currentKey) return null;
  const prev = prevThreads.find((t) => t.key === currentKey);
  const prePRMatch = currentKey.match(/^flow:issue:(\d+)$/);
  if (prePRMatch) {
    const issueNum = parseInt(prePRMatch[1], 10);
    if (prev) {
      const prevRunIds = new Set(prev.runs.map((r) => r.id));
      const upgradedByRun = nextThreads.find(
        (t) =>
          t.kind === 'flow' &&
          t.issue === issueNum &&
          Boolean(t.pr) &&
          t.runs.some((r) => prevRunIds.has(r.id)),
      );
      if (upgradedByRun) return upgradedByRun.key;
    }
    const prevKeys = new Set(prevThreads.map((t) => t.key));
    const newlyOpenedPRThread = nextThreads.find(
      (t) =>
        t.kind === 'flow' &&
        t.issue === issueNum &&
        Boolean(t.pr) &&
        (!prevKeys.has(t.key) || t.active),
    );
    if (newlyOpenedPRThread) return newlyOpenedPRThread.key;
  }
  if (nextThreads.some((t) => t.key === currentKey)) {
    return currentKey;
  }
  if (prev) {
    const prevRunIds = new Set(prev.runs.map((r) => r.id));
    const byRunId = nextThreads.find((t) => t.runs.some((r) => prevRunIds.has(r.id)));
    if (byRunId) return byRunId.key;
  }
  if (prePRMatch) {
    const issueNum = parseInt(prePRMatch[1], 10);
    const upgraded = nextThreads.find((t) => t.kind === 'flow' && t.issue === issueNum && Boolean(t.pr));
    if (upgraded) return upgraded.key;
  }
  return currentKey;
}

function parseTimelineRunEventSummary(runId: string, detail?: string): string {
  if (!detail) return '';
  const trimmed = detail.trim();
  if (!trimmed || trimmed.startsWith(`Run ${runId}`)) {
    return '';
  }
  return trimmed;
}

// Combine all runs in the expanded flow and forge timeline events into a single
// chronological flow timeline with explicit run boundaries.
// Consumes backend `type: "run"` events by `run_id` so ended runs are deduplicated
// and live runs receive explicit run boundaries and constituent-run entries.
export function buildFlowTimeline(
  thread: HistoryThread,
  issueTimeline?: IssueTimeline | null,
): FlowTimelineEntry[] {
  const runsById = new Map<string, HistoryRunInfo>();
  const actorMetaByActor = new Map<string, { family: string; harness: string }>();

  for (const r of thread.runs) {
    runsById.set(r.id, { ...r });
    if (r.actor && (r.family || r.harness) && !actorMetaByActor.has(r.actor)) {
      actorMetaByActor.set(r.actor, { family: r.family || '', harness: r.harness || '' });
    }
  }

  const rawEvents = issueTimeline?.events || [];

  // Pass 1: consume `type: "run"` events by `run_id` (deduplicating ended runs
  // already in `thread.runs` and incorporating live or newly started runs into
  // the run/boundary model).
  for (const ev of rawEvents) {
    if (ev.type !== 'run') continue;
    const runId = ev.run_id || `run:${ev.time}`;
    const isLive = !ev.ended && (!ev.status || ev.status === 'live' || ev.status === 'running');
    const normalizedStatus = isLive
      ? 'running'
      : ev.status === 'live'
        ? 'running'
        : ev.status || 'succeeded';
    const summaryFromEv = parseTimelineRunEventSummary(runId, ev.detail);

    const existing = runsById.get(runId);
    if (existing) {
      runsById.set(runId, {
        ...existing,
        ended: existing.ended || ev.ended,
        duration: existing.duration || ev.duration || '',
        kind: existing.kind || ev.kind || 'implement',
        actor: existing.actor || ev.actor || '',
        status:
          existing.status === 'running' && ev.ended && normalizedStatus !== 'running'
            ? normalizedStatus
            : existing.status || normalizedStatus,
        outcome: existing.outcome || ev.outcome,
        active: ev.ended ? false : Boolean(existing.active) || isLive,
        summary: existing.summary || summaryFromEv,
        target_url: existing.target_url || ev.url || '',
      });
      continue;
    }

    const kind =
      ev.kind ||
      (ev.title?.includes(': verify ')
        ? 'verify'
        : ev.title?.includes(': discuss ')
          ? 'discuss'
          : 'implement');
    const actor = ev.actor || '';
    const actorMeta = actorMetaByActor.get(actor);
    const target =
      kind === 'verify' && thread.pr
        ? `!${thread.pr}`
        : thread.issue
          ? `#${thread.issue}`
          : thread.pr
            ? `!${thread.pr}`
            : '';
    const targetUrl =
      ev.url ||
      (kind === 'verify' && thread.pr_url
        ? thread.pr_url
        : thread.issue_url || thread.pr_url || '');

    runsById.set(runId, {
      id: runId,
      target,
      target_url: targetUrl,
      issue: thread.issue,
      issue_url: thread.issue_url,
      issue_title: thread.issue_title,
      pr: thread.pr,
      pr_url: thread.pr_url,
      pr_title: thread.pr_title,
      milestone: thread.milestone,
      thread_key: thread.key,
      active: isLive,
      kind,
      actor,
      family: actorMeta?.family || '',
      harness: actorMeta?.harness || '',
      mode: 'batch',
      status: normalizedStatus,
      outcome: ev.outcome,
      created: ev.time,
      ended: ev.ended,
      duration: ev.duration || '',
      summary: summaryFromEv,
      purged: false,
      denials: [],
    });
  }

  const runs = Array.from(runsById.values()).sort((a, b) => {
    const tA = new Date(a.created).getTime() || 0;
    const tB = new Date(b.created).getTime() || 0;
    return tA - tB || a.id.localeCompare(b.id);
  });

  const boundaries: FlowTimelineRunBoundary[] = runs.map((r) => {
    const startEvt: TimelineEvent = {
      type: 'run_started',
      time: r.created,
      ended: r.ended,
      duration: r.duration,
      kind: r.kind,
      title: `${r.kind} run started (${r.actor})`,
      actor: r.actor,
      status: r.status,
      run_id: r.id,
    };
    const innerEvents: TimelineEvent[] = [startEvt];
    if (r.ended) {
      const failure = describeRunFailure(r);
      innerEvents.push({
        type: 'run_ended',
        time: r.ended,
        duration: r.duration,
        kind: r.kind,
        title: `${r.kind} run ${r.status} (${r.actor})`,
        detail: failure ? failureText(failure) : r.summary,
        actor: r.actor,
        status: r.status,
        run_id: r.id,
      });
    }
    return {
      entry_type: 'run_boundary',
      run: r,
      time: r.created,
      ended: r.ended,
      duration: r.duration,
      status: r.status,
      events: innerEvents,
    };
  });

  const standaloneEvents: TimelineEvent[] = [];

  for (const ev of rawEvents) {
    // Skip raw `run`, `run_started`, and `run_ended` events since all flow runs
    // have been consumed by `run_id` into explicit `run_boundary` entries.
    if (ev.type === 'run' || ev.type === 'run_started' || ev.type === 'run_ended') {
      continue;
    }

    // Issue creation and PR merge are milestone flow events shown at top level.
    if (ev.type === 'issue_created' || ev.type === 'pr_merged') {
      standaloneEvents.push(ev);
      continue;
    }

    // Place forge events (pr_opened, review, comment) inside the constituent run
    // that was active when the event occurred, or as standalone events between runs.
    const evMs = new Date(ev.time).getTime() || 0;
    let matchedBoundary: FlowTimelineRunBoundary | undefined;
    for (const b of boundaries) {
      const startMs = new Date(b.run.created).getTime() || 0;
      const endMs = b.run.ended ? new Date(b.run.ended).getTime() : Infinity;
      // Allow a 2-second grace window around run start/end for forge webhooks/API timestamps.
      if (evMs >= startMs - 2000 && evMs <= endMs + 2000) {
        if (!matchedBoundary) {
          matchedBoundary = b;
        } else if (ev.actor && b.run.actor === ev.actor) {
          matchedBoundary = b;
        }
      }
    }

    if (matchedBoundary) {
      matchedBoundary.events.push(ev);
    } else {
      standaloneEvents.push(ev);
    }
  }

  for (const b of boundaries) {
    b.events.sort((a, c) => {
      const tA = new Date(a.time).getTime() || 0;
      const tB = new Date(c.time).getTime() || 0;
      if (tA !== tB) return tA - tB;
      if (a.type === 'run_started') return -1;
      if (c.type === 'run_started') return 1;
      if (a.type === 'run_ended') return 1;
      if (c.type === 'run_ended') return -1;
      return 0;
    });
  }

  const entries: FlowTimelineEntry[] = [
    ...boundaries,
    ...standaloneEvents.map((event) => ({
      entry_type: 'event' as const,
      time: event.time,
      event,
    })),
  ];

  entries.sort((a, b) => {
    const tA = new Date(a.time).getTime() || 0;
    const tB = new Date(b.time).getTime() || 0;
    if (tA !== tB) return tA - tB;
    if (a.entry_type === 'event' && a.event.type === 'issue_created') return -1;
    if (b.entry_type === 'event' && b.event.type === 'issue_created') return 1;
    return 0;
  });

  return entries;
}

// Return all constituent runs for an expanded flow, incorporating both `thread.runs`
// and any live/timeline runs consumed by `buildFlowTimeline`.
export function buildFlowConstituentRuns(
  thread: HistoryThread,
  entries?: FlowTimelineEntry[] | null,
): HistoryRunInfo[] {
  if (entries && entries.length > 0) {
    const boundaryRuns = entries
      .filter((e): e is FlowTimelineRunBoundary => e.entry_type === 'run_boundary')
      .map((e) => e.run);
    if (boundaryRuns.length > 0) {
      return boundaryRuns;
    }
  }
  return thread.runs;
}

export interface KeyedLogLine<T = LogLine> {
  id: number;
  line: T;
}

// Build keyed lines with monotonic IDs so Svelte preserves existing line DOM elements
// as lines slide or trim.
export function buildKeyedLogLines<T = LogLine>(
  lines: T[] | undefined | null,
  baseIndex: number,
): KeyedLogLine<T>[] {
  if (!lines || lines.length === 0) return [];
  return lines.map((line, i) => ({ id: baseIndex + i, line }));
}

// The texts of log lines, which follow and trim detection compare.
export function logLineTexts(lines: LogLine[] | undefined | null): string[] {
  return (lines || []).map((l) => l.text);
}

// A tool line's text split into the tool name and its concise argument,
// without the renderer's separating colon; null for other lines.
export function splitToolLine(line: LogLine): { tool: string; arg: string } | null {
  if (line.kind !== 'tool' || !line.tool || !line.text.startsWith(line.tool)) return null;
  return { tool: line.tool, arg: line.text.slice(line.tool.length).replace(/^:\s*/, '') };
}

// Detect how many lines were trimmed from the beginning of prevLines when nextLines arrived.
// Returns 0 if lines were only appended or unchanged.
// Returns k > 0 if k lines were trimmed from the front.
// If nextLines is empty and prevLines had lines, returns prevLines.length.
// If there is no overlap at all, returns prevLines.length.
export function detectTrimmedLineCount(prevLines: string[], nextLines: string[]): number {
  if (!prevLines || prevLines.length === 0) return 0;
  if (!nextLines || nextLines.length === 0) return prevLines.length;

  // Check if prevLines is a prefix of nextLines (0 lines trimmed, lines only appended or identical)
  let isPrefix = true;
  if (nextLines.length >= prevLines.length) {
    for (let i = 0; i < prevLines.length; i++) {
      if (prevLines[i] !== nextLines[i]) {
        isPrefix = false;
        break;
      }
    }
    if (isPrefix) return 0;
  }

  // Look for the shift k in [1, prevLines.length) where prevLines[k:] is a prefix of nextLines
  for (let k = 1; k < prevLines.length; k++) {
    const remainingCount = prevLines.length - k;
    if (nextLines.length >= remainingCount) {
      let matches = true;
      for (let i = 0; i < remainingCount; i++) {
        if (prevLines[k + i] !== nextLines[i]) {
          matches = false;
          break;
        }
      }
      if (matches) return k;
    }
  }

  // No overlap found: all previous lines have been shifted out
  return prevLines.length;
}

// Compute the new scrollTop when trimmedHeight pixels were removed from the top of the scroll container,
// preserving the visual position of the lines below.
export function computePreservedScrollTop(
  currentScrollTop: number,
  trimmedHeight: number,
): number {
  if (trimmedHeight <= 0) return currentScrollTop;
  return Math.max(0, currentScrollTop - trimmedHeight);
}

// Check whether a scroll container is scrolled to the bottom (within threshold pixels).
// If the content fits within the viewport, it is always considered at the bottom.
export function isScrolledToBottom(
  scrollTop: number,
  scrollHeight: number,
  clientHeight: number,
  threshold = 16,
): boolean {
  if (scrollHeight <= clientHeight) return true;
  return scrollHeight - scrollTop - clientHeight <= threshold;
}

// Determine the next follow state given whether the container is at the bottom,
// or if an explicit action was taken (such as clicking the follow control).
export function updateLogTailFollow(
  wasFollowing: boolean,
  atBottom: boolean,
  action?: 'scroll' | 'follow_click',
): boolean {
  if (action === 'follow_click') return true;
  return atBottom;
}

