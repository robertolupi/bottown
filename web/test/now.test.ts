// Now (monitor-ui design §3): Live laid out from the server's live view (the
// fixture's `live` section, as GET /api/live answers it), each tile's last
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
import * as nowModule from '../src/now.ts';
import {
  emptyLive,
  endedHref,
  formatSpan,
  freshness,
  healthProblems,
  inboxCounts,
  lastOutputText,
  rowHref,
  rowText,
  tileMeta,
  upstreamText,
} from '../src/now.ts';
import type { Live, LiveItem, UIStatusResponse } from '../src/types.ts';

const fixture = JSON.parse(readFileSync(new URL('../fixtures/default.json', import.meta.url), 'utf8'));
const live = fixture.live as Live;

const t0 = Date.parse('2026-11-02T10:43:40Z');
const min = 60 * 1000;

const liveProps = (p: Record<string, unknown>) => ({
  live, now: t0, onStartFlow: () => {}, onStartRun: () => {}, onGC: () => {}, ...p,
});

const byId = (root: Element, id: string) => root.querySelector<HTMLElement>(`.now-tile[data-id="${id}"]`)!;

test('formatSpan shows hours past an hour', () => {
  assert.equal(formatSpan(4000), '4s');
  assert.equal(formatSpan(6 * min + 30000), '6m');
  assert.equal(formatSpan(192 * min), '3h 12m');
  assert.equal(formatSpan(-5), '0s');
});

test('Now groups nothing: the live view comes from the server', () => {
  for (const gone of ['nowTiles', 'nowRows', 'justEnded', 'quietAfterMs', 'lastOutputStatus']) {
    assert.equal(gone in nowModule, false, `now.ts still exports ${gone}`);
  }
});

test('one tile per item, in the order the server gives', () => {
  const view = render(LiveRuns, liveProps({}));
  try {
    const tiles = [...view.target.querySelectorAll<HTMLElement>('.now-tile')];
    assert.deepEqual(tiles.map((t) => t.dataset.id), live.items.map((i) => i.id));
    assert.deepEqual(tiles.map((t) => t.dataset.id), ['r-1a2b3c4d', 'flow-52-abcdef0123.check', 'r-9c8d7e6f', 'flow-56-7c1d2e3f4a.e2e', 'flow-64-9e8d7c6b5a.check']);
    assert.match(byId(view.target, 'r-1a2b3c4d').textContent!, /#42\s*Retry webhook deliveries with backoff/);
    assert.match(byId(view.target, 'r-1a2b3c4d').textContent!, /implement 1\/3 · claude · 12m\/1h 0m/);
    assert.match(byId(view.target, 'r-9c8d7e6f').textContent!, /verify 2\/3 · kimi · 4m\/1h 0m/);
    assert.match(byId(view.target, 'r-9c8d7e6f').textContent!, /1 denied/);
    assert.match(byId(view.target, 'flow-52-abcdef0123.check').textContent!, /checks · !59 · abcdef0/);
    assert.match(byId(view.target, 'flow-56-7c1d2e3f4a.e2e').textContent!, /checks e2e · !62 · 7c1d2e3/);
    assert.match(byId(view.target, 'flow-64-9e8d7c6b5a.check').textContent!, /checks · !65 · 9e8d7c6/);
    // No filter bar: the tail is LogLineView lines only.
    assert.equal(view.target.querySelector('.log-follow-btn'), null);
    tiles[1].click();
    assert.equal(location.hash, '#/run/flow-52-abcdef0123.check');
  } finally { view.unmount(); location.hash = ''; }
});

test('a tile shows the last lines of its tail', () => {
  const tail = Array.from({ length: 12 }, (_, i) => ({ kind: 'text', text: `line ${i + 1}` }));
  const items = live.items.map((i) => (i.id === 'r-1a2b3c4d' ? { ...i, tail } : i));
  const view = render(LiveRuns, liveProps({ live: { ...live, items } }));
  try {
    const lines = [...byId(view.target, 'r-1a2b3c4d').querySelectorAll('.tile-tail .log-line')].map((l) => l.textContent!.trim());
    assert.equal(lines.length, 8);
    assert.equal(lines.at(-1), 'line 12');
    assert.equal(byId(view.target, 'flow-52-abcdef0123.check').querySelector('.tile-empty')?.textContent, 'No output yet.');
  } finally { view.unmount(); }
});

test('quiet is the server\'s: the warning border, "no output for", and the summary', () => {
  const props = reactiveProps(liveProps({}));
  const view = render(LiveRuns, props);
  try {
    const quiet = byId(view.target, 'r-9c8d7e6f');
    assert.ok(quiet.classList.contains('quiet'));
    assert.equal(quiet.querySelector('.tile-last')!.textContent, 'no output for 2m');
    // Old output is not quiet unless the server says so.
    const old = byId(view.target, 'r-1a2b3c4d');
    assert.ok(!old.classList.contains('quiet'));
    assert.equal(old.querySelector('.tile-last')!.textContent, '44s');
    assert.equal(view.target.querySelector('.live-summary')!.textContent!.replace(/\s+/g, ' ').trim(), '5 running · 1 quiet · 1 waiting · 1 interactive');

    // The next poll says #42's run went quiet: it turns in place.
    const next: Live = {
      ...live,
      items: live.items.map((i) => (i.id === 'r-1a2b3c4d' ? { ...i, quiet: true } : i)),
      summary: { ...live.summary, quiet: 2 },
    };
    props.live = next;
    props.now = t0 + 5 * min;
    flushSync();
    assert.ok(byId(view.target, 'r-1a2b3c4d').classList.contains('quiet'));
    assert.equal(byId(view.target, 'r-1a2b3c4d').querySelector('.tile-last')!.textContent, 'no output for 5m');
    assert.deepEqual([...view.target.querySelectorAll<HTMLElement>('.now-tile')].map((t) => t.dataset.id), live.items.map((i) => i.id));
    assert.match(view.target.querySelector('.live-summary')!.textContent!, /5 running · 2 quiet/);
  } finally { view.unmount(); }
});

test('last output counts from the start for an item that wrote nothing', () => {
  const logless: LiveItem = { id: 'flow-1-b.check', kind: 'check', target: '#1', started: new Date(t0 - 2 * min).toISOString(), quiet: false, tail: [], denials: 0 };
  assert.equal(lastOutputText(logless, t0), '2m');
  assert.equal(lastOutputText({ ...logless, quiet: true }, t0 + 4 * min), 'no output for 6m');
  assert.equal(tileMeta(logless, t0), 'checks · 2m');
  assert.equal(tileMeta({ ...logless, kind: 'verify', actor: 'kimi', limit: 3600, flow: { issue: 1 } }, t0), 'verify · kimi · 2m/1h 0m');
});

test('a waiting flow and an interactive run are rows, linking to their pages', () => {
  assert.deepEqual(live.rows.map((r) => [r.key, rowHref(r)]), [['flow-53', '#/flow/53'], ['r-0f1e2d3c', '#/run/r-0f1e2d3c']]);
  assert.equal(rowText({ key: 'flow-9', kind: 'flow', target: '#9', issue: 9, what: '' }, t0), 'flow');
  const view = render(LiveRuns, liveProps({}));
  try {
    const lines = [...view.target.querySelectorAll('.now-row')].map((r) => r.textContent!.replace(/\s+/g, ' ').trim());
    assert.deepEqual(lines, [
      '#53 Usage dashboard endpoint · flow 1/3 · waiting for actor · agy has another live batch run (r-2b3c4d5e) · 6m',
      '#44 Should exports stream CSV or build it in memory? · discuss · codex · interactive · 23m',
    ]);
    assert.deepEqual([...view.target.querySelectorAll<HTMLAnchorElement>('.now-row a')].map((a) => a.getAttribute('href')), ['#/flow/53', '#/run/r-0f1e2d3c']);
  } finally { view.unmount(); }
});

test('just ended links each thread the server names to its page', () => {
  assert.deepEqual(live.ended.map(endedHref), ['#/flow/62', '#/flow/52?pr=58', '#/run/r-3e4f5a6b']);
  const view = render(LiveRuns, liveProps({}));
  try {
    const links = [...view.target.querySelectorAll<HTMLAnchorElement>('.just-ended a')];
    assert.deepEqual(links.map((a) => a.getAttribute('href')), ['#/flow/62', '#/flow/52?pr=58', '#/run/r-3e4f5a6b']);
    assert.equal(links[0].textContent, '#62 Retry webhook deliveries with jitter');
    assert.equal(links[2].textContent, 'retro');
  } finally { view.unmount(); }
});

test('an empty live view is one idle line', () => {
  const view = render(LiveRuns, liveProps({ live: emptyLive() }));
  try {
    assert.equal(view.target.querySelector('.live-idle')?.textContent, 'Nothing running in this repository.');
    assert.equal(view.target.querySelector('.live-summary')!.textContent!.trim(), '0 running');
    assert.equal(view.target.querySelector('.just-ended'), null);
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
