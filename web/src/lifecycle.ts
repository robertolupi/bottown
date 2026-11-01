import type { ActiveFlowInfo, LiveRunInfo } from './types';

// What a live poll changed in the run and flow lifecycle, and so which views
// need refreshing. Runs are identified by id and flows by issue, so a run
// replaced by another between polls counts as a change even when the counts
// match, while an unchanged snapshot refreshes nothing.
export interface LifecycleChange {
  // A run or flow started or ended: status (actors, attention) may differ.
  status: boolean;
  // A run or flow ended: the ended-runs history has new entries.
  history: boolean;
}

type List<T> = T[] | null | undefined;

function keys<T>(items: List<T>, key: (item: T) => string): Set<string> {
  return new Set((items || []).map(key));
}

// Whether a has a key that b lacks.
function hasMissing(a: Set<string>, b: Set<string>): boolean {
  for (const x of a) if (!b.has(x)) return true;
  return false;
}

export function lifecycleChange(
  prevRuns: List<LiveRunInfo>,
  prevFlows: List<ActiveFlowInfo>,
  nextRuns: List<LiveRunInfo>,
  nextFlows: List<ActiveFlowInfo>,
): LifecycleChange {
  const runKey = (r: LiveRunInfo) => r.id;
  const flowKey = (f: ActiveFlowInfo) => String(f.issue);
  const pr = keys(prevRuns, runKey);
  const nr = keys(nextRuns, runKey);
  const pf = keys(prevFlows, flowKey);
  const nf = keys(nextFlows, flowKey);

  const ended = hasMissing(pr, nr) || hasMissing(pf, nf);
  const started = hasMissing(nr, pr) || hasMissing(nf, pf);
  return { status: ended || started, history: ended };
}
