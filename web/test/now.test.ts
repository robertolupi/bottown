// Now (monitor-ui design §3): the tiles and rows of Live, each tile's last
// output and quiet, Needs you's counts and its remembered expansion, and the
// header's health, upstream and freshness chips.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { flushSync, render } from '../testing/dom.ts';
import { reactiveProps } from '../testing/props.svelte.js';
import AppHeader from '../src/AppHeader.svelte';
import LiveRuns from '../src/LiveRuns.svelte';
import OwnerInbox from '../src/OwnerInbox.svelte';
import {
  formatSpan,
  freshness,
  healthProblems,
  inboxCounts,
  justEnded,
  lastOutputStatus,
  nowRows,
  nowTiles,
  quietAfterMs,
  upstreamText,
} from '../src/now.ts';
import type { HistoryThread, LiveCheckInfo, LiveRunInfo, UIStatusResponse } from '../src/types.ts';

const fixture = JSON.parse(readFileSync(new URL('../fixtures/default.json', import.meta.url), 'utf8'));

const t0 = Date.parse('2026-11-10T12:00:00Z');
const iso = (offsetMs: number) => new Date(t0 + offsetMs).toISOString();
const min = 60 * 1000;

function run(id: string, created: number, extra: Partial<LiveRunInfo> = {}): LiveRunInfo {
  return {
    id, target: `#${id.length}`, target_url: '', kind: 'implement', actor: 'claude', family: 'anthropic', harness: 'claude',
    interactive: false, created: iso(created), expires: iso(created + 60 * min), elapsed: '', timeout: '60m',
    log_tail: [{ kind: 'text', text: `line of ${id}` }], denials: [], ...extra,
  };
}

function check(id: string, created: number, extra: Partial<LiveCheckInfo> = {}): LiveCheckInfo {
  return { id, group: 'e2e', issue: 56, sha: '7c1d2e3f4a', commands: ['make e2e'], created: iso(created), elapsed: '', log_tail: [], ...extra };
}

const liveProps = (p: Record<string, unknown>) => ({
  liveRuns: [], liveChecks: [], activeFlows: [], historyRuns: [], threads: [], now: t0,
  onStartFlow: () => {}, onStartRun: () => {}, onGC: () => {}, ...p,
});

test('formatSpan shows hours past an hour', () => {
  assert.equal(formatSpan(4000), '4s');
  assert.equal(formatSpan(6 * min + 30000), '6m');
  assert.equal(formatSpan(192 * min), '3h 12m');
  assert.equal(formatSpan(-5), '0s');
});

test('one tile per batch run and check group, in start order', () => {
  const runs = [run('r-late', -1 * min), run('r-early', -30 * min), run('r-inter', -20 * min, { interactive: true })];
  const checks = [check('flow-56-a.e2e', -10 * min)];
  assert.deepEqual(nowTiles(runs, checks).map((t) => t.id), ['r-early', 'flow-56-a.e2e', 'r-late']);

  const view = render(LiveRuns, liveProps({ liveRuns: runs, liveChecks: checks }));
  try {
    const tiles = [...view.target.querySelectorAll<HTMLElement>('.now-tile')];
    assert.deepEqual(tiles.map((t) => t.dataset.id), ['r-early', 'flow-56-a.e2e', 'r-late']);
    assert.match(tiles[0].textContent!, /implement · claude · 30m\/1h 0m/);
    assert.match(tiles[1].textContent!, /checks e2e · 7c1d2e3/);
    // No filter bar: the tail is LogLineView lines only.
    assert.equal(view.target.querySelector('.log-follow-btn'), null);
    tiles[2].click();
    assert.equal(location.hash, '#/run/r-late');
  } finally { view.unmount(); location.hash = ''; }
});

test('last output for a fresh, a quiet and a log-less item, and the quiet class at the threshold', () => {
  const fresh = run('r-fresh', -20 * min, { last_output: iso(-4000) });
  const quiet = run('r-quiet', -20 * min, { last_output: iso(-6 * min) });
  const logless = check('flow-1-b.check', -2 * min);
  assert.deepEqual(lastOutputStatus(fresh, t0), { text: '4s', quiet: false });
  assert.deepEqual(lastOutputStatus(quiet, t0), { text: 'no output for 6m', quiet: true });
  assert.deepEqual(lastOutputStatus(logless, t0), { text: '2m', quiet: false }, 'counts from the start');
  const edge = run('r-edge', -20 * min, { last_output: iso(-quietAfterMs) });
  assert.equal(lastOutputStatus(edge, t0).quiet, true, 'quiet at the threshold');
  assert.equal(lastOutputStatus(edge, t0 - 1000).quiet, false, 'not a second before');

  const props = reactiveProps(liveProps({ liveRuns: [fresh, quiet, edge], liveChecks: [logless] }));
  const view = render(LiveRuns, props);
  try {
    const byId = (id: string) => view.target.querySelector<HTMLElement>(`.now-tile[data-id="${id}"]`)!;
    assert.ok(!byId('r-fresh').classList.contains('quiet'));
    assert.match(byId('r-fresh').querySelector('.tile-last')!.textContent!, /^4s$/);
    assert.ok(byId('r-quiet').classList.contains('quiet'));
    assert.equal(byId('r-quiet').querySelector('.tile-last')!.textContent, 'no output for 6m');
    assert.ok(byId('r-edge').classList.contains('quiet'));
    assert.ok(!byId('flow-1-b.check').classList.contains('quiet'));
    assert.match(view.target.querySelector('.live-summary')!.textContent!, /4 running · 2 quiet/);
    // As time passes the fresh tile turns quiet, in place.
    props.now = t0 + quietAfterMs;
    flushSync();
    assert.ok(byId('r-fresh').classList.contains('quiet'));
    assert.deepEqual([...view.target.querySelectorAll<HTMLElement>('.now-tile')].map((t) => t.dataset.id), ['r-edge', 'r-fresh', 'r-quiet', 'flow-1-b.check']);
  } finally { view.unmount(); }
});

test('a waiting flow and an interactive run are rows, not tiles', () => {
  const runs = [run('r-flow', -4 * min, { flow: { issue: 42, round: 1, max_rounds: 3, step: 'implementing with claude', label: '' } }),
    run('r-inter', -9 * min, { interactive: true, target: '#61', issue_title: 'Rename the export flag' })];
  const flows = [
    { issue: 42, implement: 'claude', verify: 'kimi', round: 1, max_rounds: 3, step: 'implementing with claude', label: '', waiting: false },
    { issue: 53, implement: 'agy', verify: 'claude', round: 1, max_rounds: 3, step: 'waiting for a run slot', label: '', waiting: true },
  ];
  const history = [{ id: 'r-old', issue: 53, issue_title: 'Usage dashboard endpoint', ended: iso(-6 * min) } as any];
  const rows = nowRows(flows, runs, [], history);
  assert.deepEqual(rows.map((r) => [r.key, r.href]), [['flow-53', '#/flow/53'], ['r-inter', '#/run/r-inter']]);

  const view = render(LiveRuns, liveProps({ liveRuns: runs, activeFlows: flows, historyRuns: history }));
  try {
    assert.deepEqual([...view.target.querySelectorAll<HTMLElement>('.now-tile')].map((t) => t.dataset.id), ['r-flow']);
    const lines = [...view.target.querySelectorAll('.now-row')].map((r) => r.textContent!.replace(/\s+/g, ' ').trim());
    assert.deepEqual(lines, [
      '#53 Usage dashboard endpoint · flow 1/3 · waiting for a run slot · 6m',
      '#61 Rename the export flag · implement · claude · interactive · 9m',
    ]);
    assert.match(view.target.querySelector('.live-summary')!.textContent!, /1 running · 1 waiting · 1 interactive/);
  } finally { view.unmount(); }
});

test('just ended names up to five threads that ended in the past hour, each a link', () => {
  const thread = (key: string, issue: number, endedAgo: number, active = false): HistoryThread => ({
    key, kind: 'flow', issue, issue_title: `Issue ${issue}`, active, status: 'succeeded', run_count: 1, last_activity: '',
    actors: [], duration: '', summary: '', total_denials: 0, runs: [{ id: `r-${issue}`, ended: iso(-endedAgo) } as any],
  });
  const threads = [1, 2, 3, 4, 5, 6].map((n) => thread(`t${n}`, n, n * min));
  threads.push(thread('old', 7, 2 * 60 * min), thread('live', 8, min, true));
  const ended = justEnded(threads, t0);
  assert.deepEqual(ended.map((e) => e.label), ['#1', '#2', '#3', '#4', '#5']);
  const view = render(LiveRuns, liveProps({ threads }));
  try {
    const links = [...view.target.querySelectorAll<HTMLAnchorElement>('.just-ended a')];
    assert.deepEqual(links.map((a) => a.getAttribute('href')), ['#/flow/1', '#/flow/2', '#/flow/3', '#/flow/4', '#/flow/5']);
  } finally { view.unmount(); }
});

test('Needs you counts the inbox by kind, expands, and remembers it', () => {
  assert.deepEqual(inboxCounts(fixture.inbox).map((c) => c.label), [
    '2 ready to land', '1 stopped flow', '1 conflict', '1 broken run', '1 discuss answer', '1 actor-filed issue',
  ]);
  localStorage.clear();
  const props = { inbox: fixture.inbox, inboxError: undefined, onGC: () => {}, onStop: () => {}, onLand: () => {} };
  let view = render(OwnerInbox, props);
  try {
    // Collapsed: one line of counts; actor-filed issues are hidden by default.
    assert.equal(view.target.querySelector('.inbox-list'), null);
    const counts = [...view.target.querySelectorAll('.needs-you-counts .badge')].map((b) => b.textContent);
    assert.deepEqual(counts, ['2 ready to land', '1 stopped flow', '1 conflict', '1 broken run', '1 discuss answer']);
    view.target.querySelector<HTMLButtonElement>('.needs-you-toggle')!.click();
    flushSync();
    assert.equal(view.target.querySelectorAll('.inbox-item').length, 6);
    // The actor-filed checkbox is inside the expanded list.
    const box = view.target.querySelector<HTMLInputElement>('.actor-filed-toggle input')!;
    box.click();
    flushSync();
    assert.equal(view.target.querySelectorAll('.inbox-item').length, 7);
  } finally { view.unmount(); }

  // A new mount (a reload) remembers it was expanded.
  view = render(OwnerInbox, props);
  try {
    assert.equal(view.target.querySelectorAll('.inbox-item').length, 6);
    view.target.querySelector<HTMLButtonElement>('.needs-you-toggle')!.click();
    flushSync();
  } finally { view.unmount(); }
  view = render(OwnerInbox, props);
  try { assert.equal(view.target.querySelector('.inbox-list'), null); } finally { view.unmount(); }

  // Storage that throws leaves it collapsed and still toggles.
  const real = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')!;
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, get() { throw new Error('denied'); } });
  try {
    view = render(OwnerInbox, props);
    try {
      view.target.querySelector<HTMLButtonElement>('.needs-you-toggle')!.click();
      flushSync();
      assert.equal(view.target.querySelectorAll('.inbox-item').length, 6);
    } finally { view.unmount(); }
  } finally { Object.defineProperty(globalThis, 'localStorage', real); }
});

test('inbox zero is one muted line', () => {
  const view = render(OwnerInbox, { inbox: [], inboxError: undefined, onGC: () => {}, onStop: () => {}, onLand: () => {} });
  try {
    assert.equal(view.target.querySelector('.inbox-zero-line')?.textContent, 'Nothing waiting on you.');
    assert.equal(view.target.querySelector('.needs-you-toggle'), null);
  } finally { view.unmount(); }
});

test('the freshness chip turns stale when polls fail', () => {
  assert.deepEqual(freshness(t0 - 2000, t0), { text: 'live · 2s ago', stale: false });
  assert.deepEqual(freshness(t0 - 40000, t0), { text: 'stale · 40s', stale: true });
  const props = reactiveProps({ status: fixture.status, refreshing: false, onRefresh: () => {}, lastLiveOk: t0, liveError: null as string | null, now: t0 + 2000 });
  const view = render(AppHeader, props);
  try {
    const chip = () => view.target.querySelector<HTMLElement>('.freshness-chip')!;
    assert.equal(chip().textContent, 'live · 2s ago');
    // Polls fail: lastLiveOk stays, time passes.
    props.liveError = 'live poll: 502 Bad Gateway';
    props.now = t0 + 40000;
    flushSync();
    assert.equal(chip().textContent, 'stale · 40s');
    assert.ok(chip().classList.contains('stale'));
    assert.match(chip().title, /502 Bad Gateway/);
  } finally { view.unmount(); }
});

test('the health and upstream chips for a healthy and an unhealthy fixture', () => {
  const healthy = fixture.status as UIStatusResponse;
  const unhealthy: UIStatusResponse = {
    ...healthy,
    upstream: { ...healthy.upstream, ahead_count: 0, behind_count: 3 },
    health: {
      ...healthy.health,
      protected_main: { checked: true, protected: false },
      version: { ...healthy.health.version, matches: false },
      harness_commands: healthy.health.harness_commands.map((h) => ({ ...h, current: true })),
    },
  };
  // The default fixture has one outdated harness command.
  assert.deepEqual(healthProblems(healthy.health), ['agy commands outdated']);
  assert.equal(healthProblems({ ...healthy.health, harness_commands: [] }).length, 0);
  assert.deepEqual(upstreamText(healthy), { text: 'main · 1 ahead', tone: 'accent' });
  assert.deepEqual(upstreamText(unhealthy), { text: 'main · 3 behind', tone: 'warning' });
  assert.equal(upstreamText({ ...healthy, upstream: { ...healthy.upstream, ahead_count: 0 } }).text, 'main · up to date');

  const chips = (status: UIStatusResponse) => {
    const view = render(AppHeader, { status, refreshing: false, onRefresh: () => {}, lastLiveOk: t0, now: t0 });
    try {
      const health = view.target.querySelector<HTMLAnchorElement>('.health-chip')!;
      const upstream = view.target.querySelector<HTMLAnchorElement>('.upstream-chip')!;
      // Refresh sits next to the upstream chip.
      assert.equal(upstream.nextElementSibling?.textContent?.trim(), 'Refresh');
      return [health.textContent, health.getAttribute('href'), upstream.textContent, upstream.getAttribute('href')];
    } finally { view.unmount(); }
  };
  const allGood = { ...healthy, health: { ...healthy.health, harness_commands: [] } };
  assert.deepEqual(chips(allGood), ['✓ healthy', '#/repo', 'main · 1 ahead', '#/repo']);
  assert.deepEqual(chips(unhealthy), ['2 problems', '#/repo', 'main · 3 behind', '#/repo']);
});
