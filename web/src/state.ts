import type {
  ActiveFlowInfo,
  ActorDetail,
  FlowTimelineEntry,
  FlowTimelineRunBoundary,
  HistoryRunInfo,
  HistoryThread,
  InboxItem,
  IssueTimeline,
  LiveRunInfo,
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
  verify: string;
  rounds: number | string;
  timeout: string;
}): FlowActionPayload {
  return {
    issue: Number(input.issue),
    implement: input.implement,
    verify: input.verify,
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
  question: string;
  allowSameFamily: boolean;
}): RunActionPayload {
  return {
    kind: input.kind,
    target: Number(input.target),
    actor: input.actor,
    question: input.kind === 'discuss' ? input.question : undefined,
    allow_same_family: input.kind === 'verify' ? input.allowSameFamily : undefined,
  };
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
  const sum: UsageInfo = {
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cost_usd: 0,
    turns: 0,
    duration_ms: 0,
  };
  for (const r of runs) {
    if (!r.usage) continue;
    hasUsage = true;
    sum.input_tokens = (sum.input_tokens || 0) + (r.usage.input_tokens || 0);
    sum.output_tokens = (sum.output_tokens || 0) + (r.usage.output_tokens || 0);
    sum.cache_read_tokens = (sum.cache_read_tokens || 0) + (r.usage.cache_read_tokens || 0);
    sum.cache_write_tokens = (sum.cache_write_tokens || 0) + (r.usage.cache_write_tokens || 0);
    sum.cost_usd = (sum.cost_usd || 0) + (r.usage.cost_usd || 0);
    sum.turns = (sum.turns || 0) + (r.usage.turns || 0);
    sum.duration_ms = (sum.duration_ms || 0) + (r.usage.duration_ms || 0);
  }
  return hasUsage ? sum : undefined;
}

function resolveRunIssueAndPR(run: HistoryRunInfo): { issue?: number; pr?: number } {
  let issue = run.issue && run.issue > 0 ? run.issue : undefined;
  let pr = run.pr && run.pr > 0 ? run.pr : undefined;
  if (!issue && run.target?.startsWith('#')) {
    issue = parseIssueNumber(run.target) ?? undefined;
  }
  if (!pr && run.target?.startsWith('!')) {
    pr = parseIssueNumber(run.target) ?? undefined;
  }
  return { issue, pr };
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
  if (!historyRuns || historyRuns.length === 0) return [];

  const live = liveRuns || [];
  const flows = activeFlows || [];

  // Normalize runs in chronological order (oldest first) so we can link pre-PR
  // implement runs to the PR created for that flow window.
  const enriched = historyRuns.map((r) => {
    const { issue, pr } = resolveRunIssueAndPR(r);
    return {
      run: r,
      issue,
      pr,
      createdMs: new Date(r.created).getTime() || 0,
      endedMs: new Date(r.ended || r.created).getTime() || 0,
    };
  });

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
        undefined,
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
          if (item.createdMs > prevLastMs && item.createdMs <= w.lastMs) {
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
  if (nextThreads.some((t) => t.key === currentKey)) {
    return currentKey;
  }
  const prev = prevThreads.find((t) => t.key === currentKey);
  if (prev) {
    const prevRunIds = new Set(prev.runs.map((r) => r.id));
    const byRunId = nextThreads.find((t) => t.runs.some((r) => prevRunIds.has(r.id)));
    if (byRunId) return byRunId.key;
  }
  const prePRMatch = currentKey.match(/^flow:issue:(\d+)$/);
  if (prePRMatch) {
    const issueNum = parseInt(prePRMatch[1], 10);
    const upgraded = nextThreads.find((t) => t.kind === 'flow' && t.issue === issueNum && Boolean(t.pr));
    if (upgraded) return upgraded.key;
  }
  return currentKey;
}

// Combine all runs in the expanded flow and forge timeline events into a single
// chronological flow timeline with explicit run boundaries.
export function buildFlowTimeline(
  thread: HistoryThread,
  issueTimeline?: IssueTimeline | null,
): FlowTimelineEntry[] {
  const runs = [...thread.runs].sort((a, b) => {
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
      detail: r.summary,
      actor: r.actor,
      status: r.status,
      run_id: r.id,
    };
    const innerEvents: TimelineEvent[] = [startEvt];
    if (r.ended) {
      innerEvents.push({
        type: 'run_ended',
        time: r.ended,
        duration: r.duration,
        kind: r.kind,
        title: `${r.kind} run ${r.status} (${r.actor})`,
        detail: r.summary,
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
  const rawEvents = issueTimeline?.events || [];

  for (const ev of rawEvents) {
    // Skip raw run_started / run_ended events since we construct explicit run_boundary
    // blocks for all constituent runs in the flow (and exclude foreign/discuss runs).
    if (ev.type === 'run_started' || ev.type === 'run_ended') {
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

