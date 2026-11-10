// RunHistory.svelte and its rows in jsdom: the five columns (§4.2), a
// failed thread's observed reason, the single state badge, and the filters.
import test from 'node:test';
import assert from 'node:assert/strict';
import { flushSync, render } from '../testing/dom.ts';
import { reactiveProps } from '../testing/props.svelte.js';
import RunHistory from '../src/RunHistory.svelte';
import { RunHistoryState } from '../src/history.svelte.ts';
import { deriveFlowState } from '../src/state.ts';
import type { HistoryRunInfo, HistoryThread } from '../src/types.ts';

const NOW = Date.parse('2026-11-10T12:00:00Z');

function runInfo(over: Partial<HistoryRunInfo> = {}): HistoryRunInfo {
  return {
    id: 'r-1', target: '#42', target_url: 'http://forge/issues/42', issue: 42,
    kind: 'implement', actor: 'claude', family: 'anthropic', harness: 'claude', mode: 'batch',
    status: 'succeeded', created: '2026-11-10T10:00:00Z', ended: '2026-11-10T10:30:00Z',
    duration: '30m', summary: 'Did the thing', purged: false, denials: [],
    ...over,
  };
}

function thread(over: Partial<HistoryThread> = {}): HistoryThread {
  const runs = over.runs ?? [runInfo()];
  return {
    key: 'flow:issue:42:pr:57', kind: 'flow', issue: 42, pr: 57,
    active: false, status: 'succeeded', run_count: runs.length,
    last_activity: '2026-11-10T11:30:00Z', actors: ['claude'],
    duration: '30m', summary: 'Did the thing', total_denials: 0,
    runs,
    ...over,
  };
}

function withFlowState(t: HistoryThread): HistoryThread {
  const latestRun = t.runs[t.runs.length - 1];
  return {
    ...t,
    flow_state: deriveFlowState({
      active: t.active,
      active_label: t.active_label,
      status: t.status,
      runs: t.runs,
      latestRun,
    }),
  };
}

function mount(threads: HistoryThread[]) {
  const history = new RunHistoryState();
  const view = render(
    RunHistory,
    reactiveProps({ history, threads, actors: undefined, now: NOW }),
  );
  return { history, view };
}

test('the history table has five columns', () => {
  const { view } = mount([thread()]);
  const heads = [...view.target.querySelectorAll('thead th')].map((th) => th.textContent?.trim());
  assert.deepEqual(heads, ['Target', 'Actors', 'State', 'Duration · cost', 'Last activity']);
  assert.equal(view.target.querySelectorAll('tbody tr').length, 1);
  assert.equal(view.target.querySelector('tbody tr')!.querySelectorAll('td').length, 5);
  view.unmount();
});

test('a discuss thread also has five cells', () => {
  const disc = thread({
    key: 'discuss:r-9', kind: 'discuss', issue: 44, pr: undefined, active: false,
    actors: ['kimi'], summary: 'Recommended streaming',
    runs: [runInfo({ id: 'r-9', kind: 'discuss', actor: 'kimi', issue: 44, summary: 'Recommended streaming' })],
  });
  const { view } = mount([disc]);
  assert.equal(view.target.querySelector('tbody tr')!.querySelectorAll('td').length, 5);
  view.unmount();
});

test('a failed thread shows its observed reason on the row', () => {
  const failed = runInfo({
    id: 'r-f', status: 'failed', summary: '',
    failure: { cause: 'startup', detail: 'worktree already used' },
  });
  const t = withFlowState(thread({
    key: 'flow:issue:7', issue: 7, pr: undefined, status: 'failed', summary: '', runs: [failed],
  }));
  const { view } = mount([t]);
  const text = view.target.textContent!.replace(/\s+/g, ' ');
  assert.match(text, /Observed ?Startup failed: worktree already used/);
  view.unmount();
});

test('the state column shows one badge for the thread', () => {
  const flows: Array<[string, HistoryThread, string]> = [
    ['Merged', withFlowState(thread({ key: 'm', runs: [runInfo({ flow_state: 'merged' })] })), 'flow-state-merged'],
    ['Ready to merge', withFlowState(thread({ key: 'r', runs: [runInfo({ flow_state: 'ready_to_merge' })] })), 'flow-state-ready_to_merge'],
    ['Needs changes', withFlowState(thread({ key: 'c', runs: [runInfo({ flow_state: 'needs_changes' })] })), 'flow-state-needs_changes'],
    ['In progress', withFlowState(thread({ key: 'p', active: true, active_label: 'implementing', runs: [runInfo({ status: 'running', ended: undefined, flow_state: 'in_progress' })] })), 'flow-state-in_progress'],
    ['Needs intervention', withFlowState(thread({ key: 'f', status: 'failed', runs: [runInfo({ status: 'failed', summary: '' })] })), 'flow-state-needs_intervention'],
  ];
  const { view } = mount(flows.map(([, t]) => t));
  const stateCells = [...view.target.querySelectorAll('tbody tr')].map((row) => row.querySelectorAll('td')[2]!);
  flows.forEach(([label, , cls], i) => {
    assert.ok(stateCells[i].querySelector(`.${cls}`), `${label}: shows .${cls}`);
    assert.match(stateCells[i].textContent || '', new RegExp(label), `${label}: label`);
  });
  view.unmount();
});

test('filters still narrow the rows', () => {
  const a = thread({ key: 'a', issue: 42, summary: 'Token bucket limiter', actors: ['claude'] });
  const b = thread({ key: 'b', issue: 43, pr: 58, summary: 'Audit streaming', actors: ['kimi'], runs: [runInfo({ id: 'r-2', issue: 43, actor: 'kimi', summary: 'Audit streaming' })] });
  const { history, view } = mount([a, b]);
  assert.equal(view.target.querySelectorAll('tbody tr').length, 2);
  history.filters.search = 'bucket';
  flushSync();
  assert.equal(view.target.querySelectorAll('tbody tr').length, 1);
  view.unmount();
});
