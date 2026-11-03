import type { ActiveFlowInfo, HistoryRunInfo, HistoryThread, IssueTimeline, LiveRunInfo, RunLogState } from './types';
import { groupHistoryThreads, reconcileExpandedThread, toggleExpandedThread } from './state';

type Live = { liveRuns: LiveRunInfo[] | undefined; activeFlows: ActiveFlowInfo[] | undefined };

// RunHistoryState is the run history's state and loading: ended runs,
// filters, the one expanded flow and the timelines loaded for it, and the
// run logs expanded in the table. App owns one, so it lasts across views;
// the RunHistory component renders it.
export class RunHistoryState {
  runs = $state<HistoryRunInfo[]>([]);
  loading = $state<boolean>(false);
  filters = $state({ actor: '', kind: '', status: '', date: '', search: '' });
  // At most one flow expanded at a time.
  expandedThreadKey = $state<string | null>(null);
  timelines = $state<Record<string, IssueTimeline>>({});
  timelineLoading = $state<Record<string, boolean>>({});
  timelineError = $state<Record<string, string>>({});
  logs = $state<Record<string, RunLogState>>({});

  // The history's threads, with the live runs and flows merged in.
  threads(liveRuns: LiveRunInfo[] | undefined, activeFlows: ActiveFlowInfo[] | undefined): HistoryThread[] {
    return groupHistoryThreads(this.runs, liveRuns, activeFlows);
  }

  // Expand a run's log, loading it, or collapse it.
  async toggleLog(id: string) {
    if (this.logs[id]) {
      delete this.logs[id];
      this.logs = { ...this.logs };
      return;
    }
    this.logs[id] = { loading: true };
    this.logs = { ...this.logs };
    try {
      const res = await fetch(`/api/runs/${id}/log`);
      if (!res.ok) throw new Error('Failed to load log');
      const data = await res.json();
      this.logs[id] = { loading: false, lines: data.lines || [] };
    } catch (e: any) {
      this.logs[id] = { loading: false, error: e.message };
    }
    this.logs = { ...this.logs };
  }

  // Load a flow thread's timeline; in the background, without the loading
  // note and keeping the last timeline on errors.
  async loadTimeline(thread: HistoryThread, background = false) {
    if (!thread.issue) return;
    const key = thread.key;
    if (!background && !this.timelines[key]) {
      this.timelineLoading = { ...this.timelineLoading, [key]: true };
    }
    try {
      const prParam = thread.pr !== undefined ? `?pr=${thread.pr}` : '?pr=0';
      const res = await fetch(`/api/issues/${thread.issue}/timeline${prParam}`);
      if (!res.ok) {
        throw new Error(`Failed to load timeline for #${thread.issue}`);
      }
      const data: IssueTimeline = await res.json();
      this.timelines = { ...this.timelines, [key]: data };
      if (this.timelineError[key]) {
        const nextErr = { ...this.timelineError };
        delete nextErr[key];
        this.timelineError = nextErr;
      }
    } catch (e: any) {
      if (!background) {
        this.timelineError = {
          ...this.timelineError,
          [key]: e.message || 'Failed to load timeline',
        };
      }
    } finally {
      if (this.timelineLoading[key]) {
        this.timelineLoading = { ...this.timelineLoading, [key]: false };
      }
    }
  }

  // Expand a flow thread, collapsing any other, or collapse it.
  selectFlow(thread: HistoryThread) {
    if (thread.kind !== 'flow') return;
    const nextKey = toggleExpandedThread(this.expandedThreadKey, thread.key);
    this.expandedThreadKey = nextKey;
    if (nextKey === thread.key) {
      this.loadTimeline(thread, Boolean(this.timelines[thread.key]));
    }
  }

  // Show one run of a thread: expand its flow, if it is one, and open the
  // run's log, leaving both as they are when already open.
  showRun(thread: HistoryThread, runId: string) {
    if (thread.kind === 'flow' && this.expandedThreadKey !== thread.key) {
      this.selectFlow(thread);
    }
    if (!this.logs[runId]) {
      this.toggleLog(runId);
    }
  }

  // Keep the expanded flow, and its loaded timeline, across a change of
  // threads (a run starting or ending can change a thread's key).
  reconcile(prevThreads: HistoryThread[], nextThreads: HistoryThread[]) {
    const prevKey = this.expandedThreadKey;
    const reconciledKey = reconcileExpandedThread(prevKey, prevThreads, nextThreads);
    if (reconciledKey && prevKey && reconciledKey !== prevKey && this.timelines[prevKey]) {
      this.timelines = { ...this.timelines, [reconciledKey]: this.timelines[prevKey] };
    }
    this.expandedThreadKey = reconciledKey;
  }

  // Load the ended runs; live reads the current live runs and flows. The
  // loading note shows only on a first, foreground load.
  async load(live: () => Live, background = false) {
    if (!background && this.runs.length === 0) {
      this.loading = true;
    }
    try {
      const before = live();
      const prevThreads = this.threads(before.liveRuns, before.activeFlows);
      const res = await fetch('/api/runs?state=ended');
      if (!res.ok) {
        throw new Error(`Failed to load history: ${res.statusText}`);
      }
      const nextRuns: HistoryRunInfo[] = await res.json();
      const after = live();
      const nextThreads = groupHistoryThreads(nextRuns, after.liveRuns, after.activeFlows);
      this.reconcile(prevThreads, nextThreads);
      this.runs = nextRuns;
      if (this.expandedThreadKey) {
        const selected = nextThreads.find((t) => t.key === this.expandedThreadKey);
        if (selected && selected.kind === 'flow') {
          await this.loadTimeline(selected, true);
        }
      }
    } catch (e: any) {
      console.error('Failed to load history:', e);
    } finally {
      this.loading = false;
    }
  }
}
