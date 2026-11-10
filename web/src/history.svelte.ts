import type { ActiveFlowInfo, HistoryRunInfo, HistoryThread, LiveRunInfo } from './types';
import { groupHistoryThreads } from './state';

type Live = { liveRuns: LiveRunInfo[] | undefined; activeFlows: ActiveFlowInfo[] | undefined };

// RunHistoryState is the run history's state and loading: ended runs
// and filters. App owns one, so it lasts across views;
// the RunHistory component renders it.
export class RunHistoryState {
  runs = $state<HistoryRunInfo[]>([]);
  loading = $state<boolean>(false);
  filters = $state({ actor: '', kind: '', status: '', date: '', search: '' });

  // The history's threads, with the live runs and flows merged in.
  threads(liveRuns: LiveRunInfo[] | undefined, activeFlows: ActiveFlowInfo[] | undefined): HistoryThread[] {
    return groupHistoryThreads(this.runs, liveRuns, activeFlows);
  }

  // Load the ended runs; live reads the current live runs and flows. The
  // loading note shows only on a first, foreground load.
  async load(live: () => Live, background = false) {
    if (!background && this.runs.length === 0) {
      this.loading = true;
    }
    try {
      const res = await fetch('/api/runs?state=ended');
      if (!res.ok) {
        throw new Error(`Failed to load history: ${res.statusText}`);
      }
      this.runs = await res.json();
    } catch (e: any) {
      console.error('Failed to load history:', e);
    } finally {
      this.loading = false;
    }
  }
}
