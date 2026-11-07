// The Timeline view: its geometry and labels (src/timeline.ts), and
// Timeline.svelte mounted on a fixture answer with fetch stubbed.
import test from 'node:test';
import assert from 'node:assert/strict';
import { render, flushSync } from '../testing/dom.ts';
import { reactiveProps } from '../testing/props.svelte.js';
import Timeline from '../src/Timeline.svelte';
import {
  concurrencyPath,
  formatSpan,
  percent,
  runEnd,
  segmentEnd,
  segmentLabel,
  timeScale,
  timeTicks,
  timelineQuery,
  viewEnd,
} from '../src/timeline.ts';
import type { TimelineData, TimelineRun } from '../src/types.ts';

const H = 3600_000;
const t0 = Date.parse('2026-11-02T00:00:00Z');
const iso = (h: number) => new Date(t0 + h * H).toISOString();

function run(over: Partial<TimelineRun>): TimelineRun {
  return {
    id: 'r-1', kind: 'implement', actor: 'claude', state: 'ended', status: 'succeeded',
    created: iso(1), ended: iso(2), purged: false, thread_key: 'flow:issue:7:pr:9', issue: 7, pr: 9,
    ...over,
  };
}

function data(over: Partial<TimelineData> = {}): TimelineData {
  return {
    window: { since: iso(0), until: iso(6) },
    now: iso(5),
    open: false,
    runs: [
      run({}),
      run({ id: 'r-2', actor: 'codex', kind: 'verify', created: iso(2.5), ended: undefined, state: 'active', status: 'running' }),
      run({ id: 'r-3', created: iso(1.5), ended: iso(3), status: 'failed', interactive: true }),
    ],
    actors: [
      { actor: 'claude', rows: [['r-1'], ['r-3']], runs: 2, busy: 2 * 3600 },
      { actor: 'codex', rows: [['r-2']], runs: 1, busy: 2.5 * 3600 },
    ],
    flows: [{
      key: 'flow:issue:7:pr:9', issue: 7, pr: 9, title: 'A thing',
      segments: [
        { kind: 'run', start: iso(1), end: iso(2), run_id: 'r-1' },
        { kind: 'e2e', start: iso(2), end: iso(2.25), state: 'success', sha: 'abcdef0123', derived: true },
        { kind: 'overhead', start: iso(2.25), end: iso(2.5) },
        { kind: 'run', start: iso(2.5), end: iso(5), run_id: 'r-2', live: true },
      ],
      runs: 2, agent: 3.5 * 3600, e2e: 900, owner_wait: 0, overhead: 900,
    }],
    concurrency: { steps: [{ t: iso(0), n: 0 }, { t: iso(1), n: 1 }, { t: iso(2), n: 0 }, { t: iso(2.5), n: 1 }], peak: 1, mean: 0.58 },
    ...over,
  };
}

test('timelineQuery: presets end now, a custom range needs a start before its end', () => {
  const now = new Date(t0 + 12 * H);
  assert.equal(new URLSearchParams(timelineQuery('6h', now)!).get('since'), iso(6));
  assert.equal(new URLSearchParams(timelineQuery('7d', now)!).get('until'), null, 'a preset leaves the window open');
  const today = new URLSearchParams(timelineQuery('today', now)!).get('since')!;
  assert.equal(new Date(today).getHours(), 0, 'today starts at local midnight');
  const custom = new URLSearchParams(timelineQuery('custom', now, '2026-11-01T08:00', '2026-11-01T10:00')!);
  assert.ok(custom.get('since') && custom.get('until'));
  assert.equal(timelineQuery('custom', now, '', ''), null);
  assert.equal(timelineQuery('custom', now, '2026-11-01T10:00', '2026-11-01T08:00'), null);
});

test('live intervals grow with the clock only in an open window', () => {
  const now = t0 + 7 * H;
  const closed = data();
  const open = data({ open: true });
  assert.equal(viewEnd(closed, now), t0 + 6 * H);
  assert.equal(viewEnd(open, now), now);
  const live = closed.runs[1];
  assert.equal(runEnd(live, closed, now), t0 + 5 * H, 'at the server\'s now in a closed window');
  assert.equal(runEnd(live, open, now), now);
  assert.equal(runEnd(closed.runs[0], open, now), t0 + 2 * H);
  assert.equal(segmentEnd(iso(5), true, open, now), now);
  assert.equal(segmentEnd(iso(5), true, closed, now), t0 + 5 * H);
});

test('timeScale clamps to the window, and ticks fall on round times', () => {
  const s = timeScale(t0, t0 + 6 * H, 100, 700);
  assert.equal(s.x(t0), 100);
  assert.equal(s.x(t0 + 3 * H), 400);
  assert.equal(s.x(t0 - H), 100);
  assert.equal(s.x(t0 + 9 * H), 700);
  const ticks = timeTicks(t0 + 0.2 * H, t0 + 6 * H, 8);
  assert.ok(ticks.length >= 3 && ticks.length <= 8, `${ticks.length} ticks`);
  for (const t of ticks) assert.equal(new Date(t.t).getMinutes(), 0);
  assert.deepEqual(timeTicks(t0, t0, 5), []);
});

test('labels and numbers', () => {
  assert.equal(percent(0, 100), '0%');
  assert.equal(percent(0.5, 100), '<1%');
  assert.equal(percent(42.4, 100), '42%');
  assert.equal(formatSpan(45), '45s');
  assert.equal(formatSpan(3900), '1h 05m');
  assert.equal(formatSpan(3 * 86400), '3d 0h');
  const d = data();
  assert.match(segmentLabel(d.flows[0].segments[1]), /Checks passed at abcdef01, 15m \(start derived/);
  assert.match(segmentLabel({ kind: 'e2e', start: iso(1), end: iso(1), state: 'failure' }), /no duration recorded/);
  assert.match(
    segmentLabel({ kind: 'owner_wait', start: iso(1), end: iso(3), reason: 'ready to merge', ended_by: 'merge' }),
    /^Waiting on the owner: ready to merge, 2h 00m, until the merge$/,
  );
  assert.match(segmentLabel({ kind: 'owner_wait', start: iso(1), end: iso(3), live: true }), /still waiting/);
});

test('concurrencyPath steps up to the peak and closes on the baseline', () => {
  const s = timeScale(t0, t0 + 4 * H, 0, 400);
  const d = concurrencyPath([{ t: iso(0), n: 0 }, { t: iso(1), n: 2 }, { t: iso(3), n: 1 }], t0 + 4 * H, 2, s, 0, 40);
  assert.equal(d, 'M 0.0 40 L 0.0 40.0 L 100.0 40.0 L 100.0 0.0 L 300.0 0.0 L 300.0 20.0 L 400.0 20.0 L 400.0 40 Z');
  assert.equal(concurrencyPath([{ t: iso(0), n: 0 }], t0 + H, 0, s, 0, 40), '');
});

// jsdom has no ResizeObserver, which bind:clientWidth uses; without it
// the plot keeps its default width.
(globalThis as any).ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// mount renders Timeline with fetch answering /api/timeline from body.
async function mount(body: TimelineData) {
  const fetched: string[] = [];
  globalThis.fetch = (async (path: string) => {
    fetched.push(path);
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
  }) as typeof fetch;
  const picked: { runs: string[]; flows: string[] } = { runs: [], flows: [] };
  const props = reactiveProps({
    refreshKey: 0,
    now: t0 + 5 * H,
    onRun: (r: TimelineRun) => picked.runs.push(r.id),
    onFlow: (f: { key: string }) => picked.flows.push(f.key),
  });
  const view = render(Timeline, props);
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, 5));
    flushSync();
  }
  return { view, props, fetched, picked };
}

test('Timeline draws actor lanes with sub-rows, and a bar opens its run', async () => {
  const { view, fetched, picked } = await mount(data());
  try {
    assert.equal(fetched.length, 1);
    assert.match(fetched[0], /^\/api\/timeline\?since=/);
    const bars = view.target.querySelectorAll('.tl-bar');
    assert.equal(bars.length, 3);
    const rects = [...view.target.querySelectorAll('rect.tl-run')];
    const ys = new Set(rects.map((r) => r.getAttribute('y')));
    assert.equal(ys.size, 3, 'claude\'s overlapping runs take two rows, codex its own');
    assert.ok(view.target.querySelector('.tl-run-failed'), 'a failed run is outlined');
    assert.ok(view.target.querySelector('.tl-run-hatch'), 'an interactive run is hatched');
    assert.match(view.target.textContent!, /busy 33% · 2 runs/);
    assert.match(view.target.textContent!, /peak 1 · mean 0\.6/);
    (bars[0] as SVGElement).dispatchEvent(new Event('click', { bubbles: true }));
    assert.deepEqual(picked.runs, ['r-1']);
  } finally {
    view.unmount();
  }
});

test('Timeline by flow draws checks, gaps and shares; a lane label opens the flow', async () => {
  const { view, picked } = await mount(data());
  try {
    const byFlow = [...view.target.querySelectorAll('.tl-toggle button')].find((b) => b.textContent === 'By flow')!;
    (byFlow as HTMLElement).click();
    flushSync();
    assert.equal(view.target.querySelectorAll('.tl-bar').length, 2);
    assert.equal(view.target.querySelectorAll('rect.tl-e2e').length, 1);
    assert.equal(view.target.querySelectorAll('line.tl-overhead').length, 1);
    assert.match(view.target.textContent!, /agents 58% · e2e 4% · owner 0%/);
    const label = view.target.querySelector('.tl-link') as SVGElement;
    assert.match(label.textContent!, /#7 !9 A thing/);
    label.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    assert.deepEqual(picked.flows, ['flow:issue:7:pr:9']);
  } finally {
    view.unmount();
  }
});

test('Timeline says when the window is empty', async () => {
  const { view } = await mount(data({ runs: [], actors: [], flows: [], concurrency: { steps: [{ t: iso(0), n: 0 }], peak: 0, mean: 0 } }));
  try {
    assert.match(view.target.textContent!, /No runs in this window\./);
  } finally {
    view.unmount();
  }
});
