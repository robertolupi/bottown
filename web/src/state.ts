import type {
  ActiveFlowInfo,
  ActorDetail,
  HistoryRunInfo,
  InboxItem,
  LiveRunInfo,
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
