// The keyboard (monitor-ui design §5): keys.ts maps a key to an intent,
// and App handles them in one place — switching views, moving a visible
// selection among Now's tiles and rows and History's rows, opening it,
// going back, the run page's log keys, History's search and the `?`
// panel. App runs on the default fixture, with fetch stubbed, as
// routes.test.ts does.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { render, flushSync } from '../testing/dom.ts';
import App from '../src/App.svelte';
import { keyIntent, isEditableTarget } from '../src/keys.ts';

// jsdom has no ResizeObserver, which the Timeline's bind:clientWidth uses.
(globalThis as any).ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// jsdom does no layout and has no window.scrollTo; the run page's g/G and
// its follow both scroll the window, so record the calls.
let scrollCalls: ScrollToOptions[] = [];
Object.defineProperty(window, 'scrollTo', {
  configurable: true,
  writable: true,
  value: (x: any, y?: number) => {
    scrollCalls.push(typeof x === 'object' ? x : { top: y });
  },
});
Object.defineProperty(document.documentElement, 'scrollHeight', {
  configurable: true,
  get: () => 4000,
});

const fx = JSON.parse(readFileSync(new URL('../fixtures/default.json', import.meta.url), 'utf8'));

function stubFetch() {
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost/');
    const json = (v: unknown, s = 200) => new Response(JSON.stringify(v), { status: s });
    if (url.pathname.startsWith('/api/runs/') && url.pathname.endsWith('/log')) {
      const id = decodeURIComponent(url.pathname.slice('/api/runs/'.length, -'/log'.length));
      if (id === 'r-unknown') return json({ error: 'invalid run ID' }, 400);
      return json({ id, lines: (fx.logs || {})[id] || [] });
    }
    const timelineMatch = url.pathname.match(/^\/api\/issues\/(\d+)\/timeline$/);
    if (timelineMatch) {
      const issue = timelineMatch[1];
      return json(fx.timelines?.[issue] ?? { issue: Number(issue), title: `Issue ${issue}`, events: [] });
    }
    if (/^\/api\/issues\/\d+$/.test(url.pathname)) return json({ number: 1, title: '', body: '' });
    switch (url.pathname) {
      case '/api/status':
        return json({ ...fx.status, inbox: fx.inbox, live_runs: fx.live_runs, live_checks: fx.live_checks ?? [], active_flows: fx.active_flows });
      case '/api/runs':
        return json(
          url.searchParams.get('state') === 'live'
            ? { live_runs: fx.live_runs, live_checks: fx.live_checks ?? [], active_flows: fx.active_flows }
            : fx.history,
        );
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

const settle = async (times = 6) => {
  for (let i = 0; i < times; i++) {
    await new Promise((r) => setTimeout(r, 5));
    flushSync();
  }
};

async function mountAt(hash: string) {
  history.replaceState(null, '', `/${hash}`);
  location.hash = hash;
  stubFetch();
  const view = render(App, { pollInterval: 100000 });
  await settle();
  return view;
}

// press dispatches a keydown at target (window by default) and flushes, as
// the browser would deliver it to App's one listener.
function press(key: string, target: EventTarget = window, init: KeyboardEventInit = {}) {
  target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }));
  flushSync();
}

const keyEvent = (key: string, init: KeyboardEventInit = {}) => new KeyboardEvent('keydown', { key, ...init });

test('keyIntent maps a key to what the UI does', () => {
  assert.deepEqual(keyIntent(keyEvent('2'), false), { kind: 'view', view: 'history' });
  assert.deepEqual(keyIntent(keyEvent('6'), false), { kind: 'view', view: 'repo' });
  assert.deepEqual(keyIntent(keyEvent('j'), false), { kind: 'move', delta: 1 });
  assert.deepEqual(keyIntent(keyEvent('ArrowDown'), false), { kind: 'move', delta: 1 });
  assert.deepEqual(keyIntent(keyEvent('k'), false), { kind: 'move', delta: -1 });
  assert.deepEqual(keyIntent(keyEvent('ArrowUp'), false), { kind: 'move', delta: -1 });
  assert.deepEqual(keyIntent(keyEvent('Enter'), false), { kind: 'open' });
  assert.deepEqual(keyIntent(keyEvent('Escape'), false), { kind: 'back' });
  assert.deepEqual(keyIntent(keyEvent('/'), false), { kind: 'search' });
  assert.deepEqual(keyIntent(keyEvent('?'), false), { kind: 'help' });
  // The log keys exist only where a log does.
  assert.equal(keyIntent(keyEvent('f'), false), null);
  assert.deepEqual(keyIntent(keyEvent('f'), true), { kind: 'follow' });
  assert.deepEqual(keyIntent(keyEvent('g'), true), { kind: 'log-top' });
  assert.deepEqual(keyIntent(keyEvent('G'), true), { kind: 'log-end' });
  // A held command modifier belongs to the browser.
  assert.equal(keyIntent(keyEvent('2', { ctrlKey: true }), false), null);
  assert.equal(keyIntent(keyEvent('2', { metaKey: true }), false), null);
  assert.equal(keyIntent(keyEvent('2', { altKey: true }), false), null);
  // Shift does not, because G and ? need it.
  assert.deepEqual(keyIntent(keyEvent('G', { shiftKey: true }), true), { kind: 'log-end' });
  assert.equal(keyIntent(keyEvent('x'), false), null);
});

test('isEditableTarget tells a field from the page', () => {
  const input = document.createElement('input');
  const textarea = document.createElement('textarea');
  const select = document.createElement('select');
  const div = document.createElement('div');
  assert.equal(isEditableTarget(input), true);
  assert.equal(isEditableTarget(textarea), true);
  assert.equal(isEditableTarget(select), true);
  assert.equal(isEditableTarget(div), false);
  assert.equal(isEditableTarget(null), false);
});

test('1-6 switch views, and a modifier leaves the key alone', async () => {
  const view = await mountAt('#/now');
  try {
    const digits: [string, string][] = [
      ['2', 'history'],
      ['3', 'milestones'],
      ['4', 'timeline'],
      ['5', 'stats'],
      ['6', 'repo'],
      ['1', 'now'],
    ];
    for (const [key, viewName] of digits) {
      press(key);
      await settle();
      assert.equal(location.hash, `#/${viewName}`, `key ${key}`);
    }

    press('2', window, { ctrlKey: true });
    await settle();
    assert.equal(location.hash, '#/now', 'a command modifier leaves the key alone');
  } finally {
    view.unmount();
  }
});

test('j/k and the arrows move a visible selection among Now tiles and rows, and Enter opens it', async () => {
  const view = await mountAt('#/now');
  try {
    const items = document.querySelectorAll('[data-nav]');
    assert.ok(items.length > 1, 'Now has focusable tiles and rows');

    press('j');
    let selected = document.querySelector('.nav-selected');
    assert.equal(selected, items[0], 'j selects the first item');
    assert.equal(selected!.getAttribute('data-nav'), items[0].getAttribute('data-nav'));

    press('j');
    assert.equal(document.querySelector('.nav-selected'), items[1], 'j advances');
    press('ArrowDown');
    assert.equal(document.querySelector('.nav-selected'), items[2], 'ArrowDown advances');
    press('k');
    assert.equal(document.querySelector('.nav-selected'), items[1], 'k goes back');
    press('ArrowUp');
    assert.equal(document.querySelector('.nav-selected'), items[0], 'ArrowUp goes back');

    // The item announces itself as a link and is reachable by Tab.
    assert.equal(items[0].getAttribute('role'), 'link');
    assert.equal(items[0].getAttribute('tabindex'), '0');

    press('Enter');
    await settle();
    assert.match(location.hash, /^#\/(run|flow)\//, 'Enter opens the selected tile');
  } finally {
    view.unmount();
  }
});

test('j moves a visible selection among History rows, and Enter opens the row', async () => {
  const view = await mountAt('#/history');
  try {
    const rows = document.querySelectorAll('[data-nav]');
    assert.ok(rows.length > 1, 'History has focusable rows');

    press('j');
    assert.equal(document.querySelector('.nav-selected'), rows[0]);
    press('j');
    assert.equal(document.querySelector('.nav-selected'), rows[1]);

    press('Enter');
    await settle();
    assert.match(location.hash, /^#\/(flow|run)\//, 'Enter opens the selected row');
  } finally {
    view.unmount();
  }
});

test('keys typed in History search do nothing else', async () => {
  const view = await mountAt('#/history');
  try {
    const search = document.querySelector('[data-history-search]') as HTMLInputElement;
    assert.ok(search, 'History has a search field');
    search.focus();

    press('2', search);
    await settle();
    assert.equal(location.hash, '#/history', 'a digit in the field does not switch view');

    press('j', search);
    assert.equal(document.querySelector('.nav-selected'), null, 'j in the field does not select');

    press('/', search);
    assert.equal(document.activeElement, search, '/ in the field keeps the field');

    press('Escape', search);
    await settle();
    assert.equal(location.hash, '#/history', 'Esc in the field does not go back');
  } finally {
    view.unmount();
  }
});

test('Esc on a page opened by a direct link goes to Now', async () => {
  const view = await mountAt('#/run/r-1a2b3c4d');
  try {
    // A direct link is a history of one: nothing to go back to.
    Object.defineProperty(window.history, 'length', { configurable: true, get: () => 1 });
    try {
      press('Escape');
      await settle();
      assert.equal(location.hash, '#/now', 'Esc falls back to Now');
    } finally {
      delete (window.history as any).length;
    }
  } finally {
    view.unmount();
  }
});

test('? shows the keys and Esc closes the panel', async () => {
  const view = await mountAt('#/now');
  try {
    assert.equal(document.querySelector('.keys-panel'), null);
    press('?');
    assert.ok(document.querySelector('.keys-panel'), '? opens the keys panel');
    assert.match(document.querySelector('.keys-panel')!.textContent || '', /Switch view/);

    press('Escape');
    assert.equal(document.querySelector('.keys-panel'), null, 'Esc closes the panel');
    await settle();
    assert.equal(location.hash, '#/now', 'Esc closed the panel, not the view');
  } finally {
    view.unmount();
  }
});

test('/ focuses History search', async () => {
  const view = await mountAt('#/history');
  try {
    const search = document.querySelector('[data-history-search]') as HTMLInputElement;
    assert.notEqual(document.activeElement, search);
    press('/');
    assert.equal(document.activeElement, search, '/ focuses the search field');
  } finally {
    view.unmount();
  }
});

test('f toggles following and g/G move the log, on a run page', async () => {
  const view = await mountAt('#/run/r-1a2b3c4d');
  try {
    const follow = document.querySelector('[data-action="follow"]') as HTMLElement;
    assert.ok(follow, 'the run page has a follow control');
    assert.match(follow.textContent || '', /Following/);

    press('f');
    assert.match(follow.textContent || '', /Follow output/, 'f stops following');
    press('f');
    assert.match(follow.textContent || '', /Following/, 'f follows again');

    scrollCalls = [];
    press('G');
    assert.equal(scrollCalls.at(-1)?.top, document.documentElement.scrollHeight, 'G goes to the end');
    press('g');
    assert.equal(scrollCalls.at(-1)?.top, 0, 'g goes to the top');
  } finally {
    view.unmount();
  }
});
