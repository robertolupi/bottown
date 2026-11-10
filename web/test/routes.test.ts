// The views' addresses (routes.ts) and the App following them: each route
// renders its view, a tab adds a history entry that Back returns from, and
// a hashchange switches the view. App.svelte runs on the default fixture,
// with fetch stubbed.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { render, flushSync } from '../testing/dom.ts';
import { parseRoute, routeHash, VIEWS } from '../src/routes.ts';
import App from '../src/App.svelte';

// jsdom has no ResizeObserver, which the Timeline's bind:clientWidth uses.
(globalThis as any).ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

const fx = JSON.parse(readFileSync(new URL('../fixtures/default.json', import.meta.url), 'utf8'));

test('parseRoute names the view of a fragment', () => {
  const cases: [string, string][] = [
    ['', 'now'],
    ['#', 'now'],
    ['#/', 'now'],
    ['#/now', 'now'],
    ['#/history', 'history'],
    ['#/milestones', 'milestones'],
    ['#/timeline', 'timeline'],
    ['#/stats', 'stats'],
    ['#/repo', 'repo'],
    ['/repo', 'repo'],
    ['#milestones', 'milestones'],
    ['#timeline', 'timeline'],
    ['#stats', 'stats'],
    ['#history', 'now'],
    ['#/nope', 'now'],
    ['#nope', 'now'],
  ];
  for (const [hash, view] of cases) assert.equal(parseRoute(hash), view, hash);
  for (const { view } of VIEWS) assert.equal(parseRoute(routeHash(view)), view);
});

function stubFetch() {
  const status = { ...fx.status, inbox: fx.inbox, live_runs: fx.live_runs, live_checks: fx.live_checks ?? [], active_flows: fx.active_flows };
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost/');
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
    switch (url.pathname) {
      case '/api/status':
        return json(status);
      case '/api/runs':
        if (url.searchParams.get('state') === 'live')
          return json({ live_runs: fx.live_runs, live_checks: fx.live_checks ?? [], active_flows: fx.active_flows });
        return json(fx.history);
      case '/api/milestones':
        return json(fx.milestones);
      case '/api/timeline':
        return json(fx.timeline);
      case '/api/stats':
        return json(fx.stats);
    }
    return new Response('not found', { status: 404 });
  }) as typeof fetch;
}

const settle = async () => {
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, 5));
    flushSync();
  }
};

function headings(root: HTMLElement): string[] {
  return [...root.querySelectorAll('section.card h2')].map((h) => h.textContent!.trim());
}

function activeTab(root: HTMLElement): string {
  return (root.querySelector('.tab-active') as HTMLElement).dataset.view!;
}

async function mountAt(hash: string) {
  history.replaceState(null, '', `/${hash}`);
  stubFetch();
  const view = render(App, {});
  await settle();
  return view;
}

const nowPanels = ['Live Runs', 'Owner Inbox'];
const elsewhere = ['Run History', 'Upstream & Forge', 'Health Checklist'];

test('each route renders its view', async () => {
  const expect: Record<string, (h: string[]) => void> = {
    '': (h) => assert.deepEqual(h, nowPanels),
    '#/now': (h) => assert.deepEqual(h, nowPanels),
    '#/history': (h) => assert.deepEqual(h, ['Run History']),
    '#/repo': (h) => assert.deepEqual(h, ['Upstream & Forge', 'Health Checklist']),
    '#/milestones': (h) => assert.ok(h.length > 0 && !h.some((x) => [...nowPanels, ...elsewhere].includes(x)), h.join()),
    '#/timeline': (h) => assert.deepEqual(h, ['Timeline']),
    '#/stats': (h) => assert.deepEqual(h, ['Stats']),
  };
  for (const [hash, check] of Object.entries(expect)) {
    const view = await mountAt(hash);
    try {
      check(headings(view.target));
      assert.equal(activeTab(view.target), parseRoute(hash), hash);
    } finally {
      view.unmount();
    }
  }
});

test('the tabs list the views in order', async () => {
  const view = await mountAt('');
  try {
    const tabs = [...view.target.querySelectorAll('.tab')].map((t) => t.textContent!.trim());
    assert.deepEqual(tabs, ['Now', 'History', 'Milestones', 'Timeline', 'Stats', 'Repository']);
  } finally {
    view.unmount();
  }
});

test('a tab click adds a history entry, and Back returns to the previous view', async () => {
  const view = await mountAt('#/repo');
  try {
    const before = history.length;
    (view.target.querySelector('[data-view="history"]') as HTMLElement).click();
    await settle();
    assert.equal(location.hash, '#/history');
    assert.equal(history.length, before + 1);
    assert.deepEqual(headings(view.target), ['Run History']);

    history.back();
    await settle();
    assert.equal(location.hash, '#/repo');
    assert.deepEqual(headings(view.target), ['Upstream & Forge', 'Health Checklist']);
  } finally {
    view.unmount();
  }
});

test('a hashchange switches the view; old and unknown fragments resolve', async () => {
  const view = await mountAt('');
  try {
    location.hash = '#/stats';
    await settle();
    assert.deepEqual(headings(view.target), ['Stats']);

    location.hash = '#milestones';
    await settle();
    assert.equal(activeTab(view.target), 'milestones');
    assert.ok(!headings(view.target).includes('Stats'));

    location.hash = '#/nowhere';
    await settle();
    assert.equal(activeTab(view.target), 'now');
    assert.deepEqual(headings(view.target), nowPanels);
  } finally {
    view.unmount();
  }
});
