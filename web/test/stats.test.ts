// The Stats view: its labels and geometry (src/stats.ts), and Stats.svelte
// mounted on a fixture answer with fetch stubbed.
import test from 'node:test';
import assert from 'node:assert/strict';
import { render, flushSync } from '../testing/dom.ts';
import { reactiveProps } from '../testing/props.svelte.js';
import Stats from '../src/Stats.svelte';
import {
  agentByKindText,
  breakdownKey,
  burnMax,
  burnPath,
  burnValue,
  countMeasure,
  durationMeasure,
  measureText,
  statsQuery,
  target,
  tokens,
  usageText,
} from '../src/stats.ts';
import type { StatsData, StatsIssue } from '../src/types.ts';

const H = 3600_000;
const t0 = Date.parse('2026-11-02T00:00:00Z');
const iso = (h: number) => new Date(t0 + h * H).toISOString();

function issue(over: Partial<StatsIssue> = {}): StatsIssue {
  return {
    issue: 12, pr: 30, title: 'Add the widget', size: 'M', milestone: 'M2',
    implementer: 'claude', implementer_family: 'anthropic', verifier: 'agy', verifier_family: 'google',
    created: iso(0), merged: iso(24), lead_seconds: 24 * 3600, cycle_seconds: 22 * 3600,
    runs: 4, agent_seconds: 3.5 * 3600, agent_by_kind: { implement: 3 * 3600, verify: 1800 },
    implement_rounds: 1, check_rounds: 1, verifier_rejections: 1, rejections_by_family: { openai: 1 },
    failed_runs: 0, interactive_runs: 1, stops: 1,
    usage: [{ family: 'anthropic', runs: 2, reported: 1, input_tokens: 182000, output_tokens: 24000, cost_usd: 1.85 }],
    measured: { added: 120, deleted: 30, files: 4 },
    ...over,
  };
}

function data(over: Partial<StatsData> = {}): StatsData {
  return {
    window: { since: iso(0), until: iso(48) },
    now: iso(48),
    issues: [issue({})],
    breakdowns: [
      {
        by: 'actor',
        cells: [{
          key: 'claude', n: 1,
          lead_seconds: { n: 1, median: 24 * 3600, min: 24 * 3600, max: 24 * 3600 },
          cycle_seconds: { n: 1, median: 22 * 3600, min: 22 * 3600, max: 22 * 3600 },
          agent_seconds: { n: 1, median: 3.5 * 3600, min: 3.5 * 3600, max: 3.5 * 3600 },
          implement_rounds: { n: 1, median: 1, min: 1, max: 1 },
          check_rounds: { n: 1, median: 1, min: 1, max: 1 },
          verifier_rejections: { n: 1, median: 1, min: 1, max: 1 },
        }],
      },
      { by: 'size', cells: [{ key: '', n: 0, lead_seconds: { n: 0 }, cycle_seconds: { n: 0 }, agent_seconds: { n: 0 }, implement_rounds: { n: 0 }, check_rounds: { n: 0 }, verifier_rejections: { n: 0 } }] },
    ],
    sizes: [
      { size: 'S', n: 0, measured: 0, buckets: [{ bucket: 'S', n: 0 }, { bucket: 'M', n: 0 }, { bucket: 'L', n: 0 }, { bucket: 'XL', n: 0 }] },
      { size: 'M', n: 1, measured: 1, buckets: [{ bucket: 'S', n: 0 }, { bucket: 'M', n: 1 }, { bucket: 'L', n: 0 }, { bucket: 'XL', n: 0 }] },
      { size: 'L', n: 0, measured: 0, buckets: [{ bucket: 'S', n: 0 }, { bucket: 'M', n: 0 }, { bucket: 'L', n: 0 }, { bucket: 'XL', n: 0 }] },
      { size: 'XL', n: 0, measured: 0, buckets: [{ bucket: 'S', n: 0 }, { bucket: 'M', n: 0 }, { bucket: 'L', n: 0 }, { bucket: 'XL', n: 0 }] },
      { size: '', n: 0, measured: 0, buckets: [{ bucket: 'S', n: 0 }, { bucket: 'M', n: 0 }, { bucket: 'L', n: 0 }, { bucket: 'XL', n: 0 }] },
    ],
    milestones: [{
      title: 'M2', total: 2, closed: 1, weighted: false,
      points: [
        { t: iso(0), total: 1, closed: 0, open: 1 },
        { t: iso(24), total: 2, closed: 1, open: 1 },
      ],
    }],
    ...over,
  };
}

test('statsQuery: presets end now, a custom range needs a start before its end', () => {
  const now = new Date(t0 + 12 * H);
  assert.equal(new URLSearchParams(statsQuery('7d', now)!).get('since'), iso(-7 * 24 + 12));
  assert.equal(new URLSearchParams(statsQuery('30d', now)!).get('until'), null, 'a preset leaves the window open');
  assert.ok(statsQuery('90d', now));
  assert.ok(statsQuery('custom', now, '2026-11-01T08:00', '2026-11-01T10:00'));
  assert.equal(statsQuery('custom', now, '', ''), null);
  assert.equal(statsQuery('custom', now, '2026-11-01T10:00', '2026-11-01T08:00'), null);
});

test('measures and formatting', () => {
  assert.equal(measureText({ n: 0 }, String), 'no data');
  assert.equal(measureText({ n: 3, median: 2, min: 1, max: 4 }, String), '2 (1–4), n 3');
  assert.equal(measureText({ n: 1, median: 5, min: 5, max: 5 }, String), '5, n 1');
  assert.equal(durationMeasure({ n: 1, median: 3600, min: 3600, max: 3600 }), '1h 00m, n 1');
  assert.equal(countMeasure({ n: 0 }), 'no data');
  assert.equal(target({ issue: 7, pr: 9 }), '#7 !9');
  assert.equal(target({ pr: 9 }), '!9');
  assert.equal(tokens(950), '950');
  assert.equal(tokens(12345), '12.3k');
  assert.equal(tokens(1_400_000), '1.4M');
  assert.equal(
    usageText({ runs: 2, reported: 1, input_tokens: 182000, output_tokens: 24000, cost_usd: 1.85 }),
    '182.0k in, 24.0k out, $1.85, 1 of 2 runs reported',
  );
  assert.equal(usageText({ runs: 1, reported: 0 }), '0 of 1 runs reported');
  assert.equal(agentByKindText(issue()), 'implement 3h 00m · verify 30m');
});

test('breakdownKey names the dimensions a key does not cover', () => {
  assert.equal(breakdownKey('actor', 'claude'), 'claude');
  assert.equal(breakdownKey('size', ''), 'unlabelled');
  assert.equal(breakdownKey('milestone', ''), 'no milestone');
  assert.equal(breakdownKey('verifier_family', ''), 'no verifier');
  assert.equal(breakdownKey('pair', ''), 'incomplete pair');
});

test('burn geometry: the top is the largest scope, weighted or counted', () => {
  const points = [
    { t: iso(0), total: 1, closed: 0, open: 1 },
    { t: iso(24), total: 3, closed: 1, open: 2 },
  ];
  assert.equal(burnMax(points, false), 3);
  assert.deepEqual(burnValue(points[1], false), { total: 3, closed: 1 });
  const weighted = [
    { t: iso(0), total: 1, closed: 0, open: 1, total_weight: 4, open_weight: 4 },
    { t: iso(24), total: 2, closed: 1, open: 1, total_weight: 8, open_weight: 2 },
  ];
  assert.equal(burnMax(weighted, true), 8);
  assert.deepEqual(burnValue(weighted[1], true), { total: 8, closed: 6 });
  const path = burnPath(points, (p) => p.total, t0, t0 + 24 * H, 0, 240, 0, 100, 3);
  assert.equal(path, 'M 0.0 66.7 L 240.0 0.0');
  assert.equal(burnPath([], (p) => p.total, t0, t0 + H, 0, 10, 0, 10, 1), '');
});

test('Stats draws the vector, the breakdown and the burn chart', async () => {
  globalThis.fetch = (async () => new Response(JSON.stringify(data()), { status: 200, headers: { 'Content-Type': 'application/json' } })) as typeof fetch;
  const props = reactiveProps({ refreshKey: 0, issuesLink: 'http://forge/bottown/demo/issues', pullsLink: 'http://forge/bottown/demo/pulls' });
  const view = render(Stats, props);
  try {
    for (let i = 0; i < 4; i++) {
      await new Promise((r) => setTimeout(r, 5));
      flushSync();
    }
    const text = view.target.textContent!;
    assert.match(text, /1 issue, 1 merged/);
    assert.match(text, /#12 !30/);
    assert.match(text, /1 \/ 1/, 'implement / check rounds');
    assert.match(text, /182\.0k in/, 'usage with coverage');
    assert.match(text, /Implementer/, 'the actor breakdown is selected');
    const link = view.target.querySelector('.stats-target a') as HTMLAnchorElement;
    assert.equal(link.getAttribute('href'), 'http://forge/bottown/demo/issues/12');
    // The size confusion table and the burn chart render.
    assert.match(text, /Size label vs measured/);
    assert.equal(view.target.querySelectorAll('svg .stats-total').length, 1);
    assert.equal(view.target.querySelectorAll('svg .stats-closed').length, 1);
  } finally {
    view.unmount();
  }
});

test('Stats says when nothing happened', async () => {
  globalThis.fetch = (async () => new Response(JSON.stringify(data({ issues: [], breakdowns: [], milestones: [] })), { status: 200 })) as typeof fetch;
  const props = reactiveProps({ refreshKey: 0, issuesLink: '', pullsLink: '' });
  const view = render(Stats, props);
  try {
    for (let i = 0; i < 4; i++) {
      await new Promise((r) => setTimeout(r, 5));
      flushSync();
    }
    assert.match(view.target.textContent!, /No issue or PR had activity in this window\./);
  } finally {
    view.unmount();
  }
});

test('Stats shows a spinner while it loads, and while it refreshes', async () => {
  // Each fetch waits until the test answers it, as a slow forge would.
  const pending: Array<() => void> = [];
  globalThis.fetch = (() => new Promise<Response>((resolve) => {
    pending.push(() => resolve(new Response(JSON.stringify(data()), { status: 200 })));
  })) as typeof fetch;
  const settle = async () => {
    for (let i = 0; i < 4; i++) {
      await new Promise((r) => setTimeout(r, 5));
      flushSync();
    }
  };
  const props = reactiveProps({ refreshKey: 0, issuesLink: '', pullsLink: '' });
  const view = render(Stats, props);
  try {
    await settle();
    const first = view.target.querySelector('.stats-loading');
    assert.ok(first, 'the first load shows the loading state');
    assert.ok(first!.querySelector('.spinner'), 'with a spinner');
    assert.equal(first!.getAttribute('role'), 'status');
    assert.equal(view.target.querySelector('.stats-updating'), null);

    pending.shift()!();
    await settle();
    assert.equal(view.target.querySelector('.stats-loading'), null, 'the loading state goes once the data is in');
    assert.match(view.target.textContent!, /1 issue, 1 merged/);
    assert.equal(view.target.querySelector('.stats-updating'), null);

    // A refresh keeps the data on screen and says it is updating.
    props.refreshKey = 1;
    await settle();
    assert.equal(pending.length, 1, 'the refresh fetched again');
    assert.ok(view.target.querySelector('.stats-updating .spinner'), 'the header shows the refresh');
    assert.equal(view.target.querySelector('.stats-loading'), null);
    assert.match(view.target.textContent!, /1 issue, 1 merged/);

    pending.shift()!();
    await settle();
    assert.equal(view.target.querySelector('.stats-updating'), null);
  } finally {
    view.unmount();
  }
});
