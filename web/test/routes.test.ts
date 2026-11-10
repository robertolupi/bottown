// The views' addresses (routes.ts) and the App following them: each route
// renders its view, a tab adds a history entry that Back returns from, and
// a hashchange switches the view. App.svelte runs on the default fixture,
// with fetch stubbed.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { render, flushSync } from '../testing/dom.ts';
import { parseRoute, routeHash, runRouteHash, flowRouteHash, VIEWS, type Route } from '../src/routes.ts';
import App from '../src/App.svelte';

// jsdom has no ResizeObserver, which the Timeline's bind:clientWidth uses.
(globalThis as any).ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// jsdom has no window.scrollTo
let currentScrollY = 0;
const scrollYProp = {
  configurable: true,
  get: () => currentScrollY,
  set: (val: number) => {
    currentScrollY = val;
  },
};
Object.defineProperty(window, 'scrollY', scrollYProp);
try {
  Object.defineProperty(globalThis, 'scrollY', scrollYProp);
} catch {}
Object.defineProperty(window, 'scrollTo', {
  configurable: true,
  writable: true,
  value: (x: any, y?: number) => {
    if (typeof x === 'object') {
      currentScrollY = x.top ?? 0;
    } else {
      currentScrollY = y ?? 0;
    }
  },
});

Object.defineProperty(document.documentElement, 'scrollHeight', {
  configurable: true,
  get() {
    const lines = document.querySelectorAll('.log-line').length;
    return 1000 + lines * 20;
  },
});

const fx = JSON.parse(readFileSync(new URL('../fixtures/default.json', import.meta.url), 'utf8'));

test('parseRoute names the view of a fragment', () => {
  const cases: [string, Route][] = [
    ['', { kind: 'view', view: 'now' }],
    ['#', { kind: 'view', view: 'now' }],
    ['#/', { kind: 'view', view: 'now' }],
    ['#/now', { kind: 'view', view: 'now' }],
    ['#/history', { kind: 'view', view: 'history' }],
    ['#/milestones', { kind: 'view', view: 'milestones' }],
    ['#/timeline', { kind: 'view', view: 'timeline' }],
    ['#/stats', { kind: 'view', view: 'stats' }],
    ['#/repo', { kind: 'view', view: 'repo' }],
    ['/repo', { kind: 'view', view: 'repo' }],
    ['#milestones', { kind: 'view', view: 'milestones' }],
    ['#timeline', { kind: 'view', view: 'timeline' }],
    ['#stats', { kind: 'view', view: 'stats' }],
    ['#history', { kind: 'view', view: 'now' }],
    ['#/nope', { kind: 'view', view: 'now' }],
    ['#nope', { kind: 'view', view: 'now' }],
    ['#/run/r-1a2b3c4d', { kind: 'run', id: 'r-1a2b3c4d' }],
    ['#/flow/42', { kind: 'flow', issue: 42, pr: undefined }],
    ['#/flow/42?pr=57', { kind: 'flow', issue: 42, pr: 57 }],
    ['#/flow/invalid', { kind: 'view', view: 'now' }],
  ];
  for (const [hash, expected] of cases) {
    assert.deepEqual(parseRoute(hash), expected, hash);
  }
  for (const { view } of VIEWS) {
    assert.deepEqual(parseRoute(routeHash(view)), { kind: 'view', view });
  }
  assert.equal(runRouteHash('r-1'), '#/run/r-1');
  assert.equal(flowRouteHash(42), '#/flow/42');
  assert.equal(flowRouteHash(42, 57), '#/flow/42?pr=57');
});

// Dynamic log store for testing poll increments
let dynamicLogs: Record<string, any[]> = {};
// The timeline requests since the last stubFetch, path and query.
let timelineRequests: string[] = [];

function stubFetch(overrides?: {
  history?: any[];
  live_runs?: any[];
  live_checks?: any[];
  active_flows?: any[];
  timelines?: Record<string, any>;
  logs?: Record<string, any[]>;
}) {
  const history = overrides?.history ? [...fx.history, ...overrides.history] : fx.history;
  const live_runs = overrides?.live_runs ? [...fx.live_runs, ...overrides.live_runs] : fx.live_runs;
  const live_checks = overrides?.live_checks ? [...(fx.live_checks ?? []), ...overrides.live_checks] : (fx.live_checks ?? []);
  const active_flows = overrides?.active_flows ? [...fx.active_flows, ...overrides.active_flows] : fx.active_flows;
  const timelines = { ...fx.timelines, ...(overrides?.timelines ?? {}) };
  dynamicLogs = JSON.parse(JSON.stringify({ ...(fx.logs || {}), ...(overrides?.logs ?? {}) }));
  timelineRequests = [];

  const status = { ...fx.status, inbox: fx.inbox, live_runs, live_checks, active_flows };
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost/');
    const json = (v: unknown, s = 200) => new Response(JSON.stringify(v), { status: s });
    if (url.pathname.startsWith('/api/runs/') && url.pathname.endsWith('/log')) {
      const id = decodeURIComponent(url.pathname.slice('/api/runs/'.length, -'/log'.length));
      if (id === 'r-unknown' || id === 'invalid!') {
        return json({ error: 'invalid run ID' }, 400);
      }
      if (id === 'r-notfound') {
        return new Response('Not found', { status: 404 });
      }
      return json({ id, lines: dynamicLogs[id] || [] });
    }
    const timelineMatch = url.pathname.match(/^\/api\/issues\/(\d+)\/timeline$/);
    if (timelineMatch) {
      const issueNum = timelineMatch[1];
      const prParam = url.searchParams.get('pr');
      timelineRequests.push(url.pathname + url.search);
      if (prParam) {
        const prKey = `${issueNum}:${prParam}`;
        if (timelines && timelines[prKey]) {
          return json(timelines[prKey]);
        }
        // The fixtures' issue-keyed timelines are their pre-PR threads,
        // unless a `N:0` key says otherwise.
        if (prParam !== '0') return new Response('Not found', { status: 404 });
      }
      if (timelines && timelines[issueNum]) {
        return json(timelines[issueNum]);
      }
      return json({ issue: parseInt(issueNum, 10), title: `Issue ${issueNum}`, events: [] });
    }
    const issueMatch = url.pathname.match(/^\/api\/issues\/(\d+)$/);
    if (issueMatch) {
      return json({ number: parseInt(issueMatch[1], 10), title: `Issue ${issueMatch[1]}`, body: '' });
    }
    switch (url.pathname) {
      case '/api/status':
        return json(status);
      case '/api/runs':
        if (url.searchParams.get('state') === 'live')
          return json({ live_runs, live_checks, active_flows });
        return json(history);
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

function headings(root: HTMLElement): string[] {
  return [...root.querySelectorAll('section.card h2')].map((h) => h.textContent!.trim());
}

function activeTab(root: HTMLElement): string {
  const el = root.querySelector('.tab-active') as HTMLElement;
  return el ? el.dataset.view! : '';
}

async function mountAt(hash: string, props: Record<string, any> = {}, overrides?: any) {
  history.replaceState(null, '', `/${hash}`);
  location.hash = hash;
  stubFetch(overrides);
  const view = render(App, props);
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
      const parsed = parseRoute(hash);
      assert.equal(activeTab(view.target), parsed.kind === 'view' ? parsed.view : 'now', hash);
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

test('#/run/<id> renders the fixture run log and denials', async () => {
  // r-9c8d7e6f is in default.json with 1 denial and logs in fx.logs['r-9c8d7e6f']
  const view = await mountAt('#/run/r-9c8d7e6f');
  try {
    const root = view.target;
    // Check breadcrumb and title
    const headerTitle = root.querySelector('.header-title');
    assert.ok(headerTitle, 'header title exists');
    assert.ok(headerTitle.textContent!.includes('Rate-limit the public API') || headerTitle.textContent!.includes('!57'));

    // Check denials are rendered
    const denialList = root.querySelector('.denials-section') || root.querySelector('.denial-list');
    assert.ok(denialList, 'denial list exists');
    assert.ok(denialList.textContent!.includes('verify runs cannot push'));

    // Check log lines are rendered
    const logLines = root.querySelectorAll('.run-page-log .log-line');
    assert.ok(logLines.length > 0, 'log lines rendered');

    // Check raw log link exists
    const rawLink = root.querySelector('.log-raw-link') as HTMLAnchorElement;
    assert.ok(rawLink);
    assert.equal(rawLink.getAttribute('href'), '/api/runs/r-9c8d7e6f/log?raw=1');

    // Check phase bar rendered
    const phaseBar = root.querySelector('.phase-bar');
    assert.ok(phaseBar, 'phase bar rendered for run in flow');
  } finally {
    view.unmount();
  }
});

test('a live run page shows new lines after a poll and stays at the end while following, and stays put when following is off', async () => {
  const runId = 'r-1a2b3c4d';
  const view = await mountAt(`#/run/${runId}`, { pollInterval: 25 });
  try {
    const root = view.target;
    const initialCount = root.querySelectorAll('.run-page-log .log-line').length;
    assert.ok(initialCount > 0, 'initial log lines rendered');

    // Following is initially on: scroll target is at bottom
    assert.ok(window.scrollY > 0, 'followed to bottom initially');

    // Append a line to dynamicLogs
    dynamicLogs[runId].push({ kind: 'text', text: 'newly polled line 999' });

    // Wait for poll
    await new Promise((r) => setTimeout(r, 60));
    await settle();

    const afterCount = root.querySelectorAll('.run-page-log .log-line').length;
    assert.equal(afterCount, initialCount + 1, 'new line rendered after update');
    assert.ok(root.textContent!.includes('newly polled line 999'));

    const scrollAtEnd = window.scrollY;

    // Turn follow off by clicking the follow button
    const followBtn = root.querySelector('.log-follow-btn') as HTMLButtonElement;
    assert.ok(followBtn, 'follow button found');
    assert.ok(followBtn.classList.contains('following'));

    followBtn.click();
    await settle();
    assert.ok(followBtn.classList.contains('paused'), 'follow paused');

    // Manually set scroll to 100
    window.scrollY = 100;

    // Append another line while following is off
    dynamicLogs[runId].push({ kind: 'text', text: 'another line while paused 1000' });
    await new Promise((r) => setTimeout(r, 60));
    await settle();

    const afterPausedCount = root.querySelectorAll('.run-page-log .log-line').length;
    assert.equal(afterPausedCount, afterCount + 1, 'another line rendered');
    assert.equal(window.scrollY, 100, 'scroll stayed put at 100 when following is off');
  } finally {
    view.unmount();
  }
});

test('#/flow/<issue> shows the three tabs with the fixture timeline and runs', async () => {
  const view = await mountAt('#/flow/42');
  try {
    const root = view.target;

    // Check tabs
    const tabs = [...root.querySelectorAll('.flow-tab')].map((t) => t.textContent!.trim());
    assert.ok(tabs.some((t) => t === 'Log'), 'Log tab exists');
    assert.ok(tabs.some((t) => t === 'Timeline'), 'Timeline tab exists');
    assert.ok(tabs.some((t) => t.startsWith('Runs')), 'Runs tab exists');

    // Default tab is Log: shows log toolbar
    assert.ok(root.querySelector('.log-toolbar'), 'Log toolbar rendered');

    // Check flow header metadata (actor, round, model, mode, elapsed against limit)
    const headerMeta = root.querySelector('.header-meta');
    assert.ok(headerMeta, 'header meta rendered');
    assert.ok(headerMeta.textContent!.includes('claude → kimi'), 'actor rendered in flow header');
    assert.ok(headerMeta.textContent!.includes('round 1/3'), 'round rendered in flow header');
    assert.ok(headerMeta.textContent!.includes('model opus'), 'model rendered in flow header');
    assert.ok(headerMeta.textContent!.includes('batch'), 'mode rendered in flow header');
    assert.ok(headerMeta.textContent!.includes('/ 60m'), 'elapsed against limit rendered in flow header');

    // Phase bar rendered and implement phase active
    const phaseBar = root.querySelector('.phase-bar');
    assert.ok(phaseBar, 'phase bar rendered in flow header');
    const activeStep = root.querySelector('.phase-step-active');
    assert.ok(activeStep?.textContent?.includes('implement'), 'implement step active in phase bar');

    // Switch to Timeline tab
    const timelineTab = [...root.querySelectorAll('.flow-tab')].find((t) => t.textContent!.trim() === 'Timeline') as HTMLElement;
    timelineTab.click();
    await settle();

    const timelineSection = root.querySelector('.flow-timeline-section');
    assert.ok(timelineSection, 'Flow timeline section rendered');

    // Switch to Runs tab
    const runsTab = [...root.querySelectorAll('.flow-tab')].find((t) => t.textContent!.trim().startsWith('Runs')) as HTMLElement;
    runsTab.click();
    await settle();

    const runsTable = root.querySelector('.table-container table');
    assert.ok(runsTable, 'Runs table rendered');
    const runLinks = root.querySelectorAll('.run-id-link');
    assert.ok(runLinks.length > 0, 'Runs tab contains run links');
    assert.ok(runLinks[0].getAttribute('href')!.startsWith('#/run/'));
  } finally {
    view.unmount();
  }
});

test('a History flow row and a Timeline bar navigate to the right route', async () => {
  // Test History flow row
  const historyView = await mountAt('#/history');
  try {
    const root = historyView.target;
    const flowRow = root.querySelector('.flow-thread-row') as HTMLElement;
    assert.ok(flowRow, 'flow thread row exists');

    flowRow.click();
    await settle();
    assert.ok(location.hash.startsWith('#/flow/'), `navigated to flow route: ${location.hash}`);
  } finally {
    historyView.unmount();
  }

  // Test Timeline bar
  const timelineView = await mountAt('#/timeline');
  try {
    const root = timelineView.target;
    const bar = root.querySelector('.tl-bar') as SVGElement;
    assert.ok(bar, 'timeline bar exists');

    bar.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await settle();
    assert.ok(location.hash.startsWith('#/run/'), `navigated to run route: ${location.hash}`);
  } finally {
    timelineView.unmount();
  }
});

test('an unknown run id shows an empty state, not an error banner', async () => {
  const view = await mountAt('#/run/r-unknown');
  try {
    const root = view.target;
    const emptyState = root.querySelector('.empty-state');
    assert.ok(emptyState, 'empty state rendered for unknown run');
    assert.ok(emptyState.textContent!.includes('No log or record found for run r-unknown'));

    // Crucial: no error banner on screen!
    const errorBanner = root.querySelector('.banner-error');
    assert.equal(errorBanner, null, 'no error banner rendered');
  } finally {
    view.unmount();
  }
});

test('a failed run with no log displays failure details and distinguishes from missing run', async () => {
  const failedRunId = 'r-6a7b8c9d';
  const view = await mountAt(`#/run/${failedRunId}`);
  try {
    const root = view.target;

    // Header target and title
    const headerTitle = root.querySelector('.header-title');
    assert.ok(headerTitle, 'header title rendered');
    assert.ok(headerTitle.textContent!.includes('#62'));
    assert.ok(headerTitle.textContent!.includes('Retry webhook deliveries with jitter'));

    // Failure details section is rendered
    const failureSection = root.querySelector('.run-failure-section');
    assert.ok(failureSection, 'run failure section rendered');
    assert.ok(failureSection.textContent!.includes('Startup failed'));
    assert.ok(failureSection.textContent!.includes('git worktree add'));

    // Empty state distinguishes known run with no log from an unknown ID
    const emptyState = root.querySelector('.empty-state');
    assert.ok(emptyState, 'empty state rendered in log section');
    assert.ok(
      emptyState.textContent!.includes(`No log was written for run ${failedRunId}`) ||
      emptyState.textContent!.includes(`No log lines recorded for run ${failedRunId}`)
    );
    assert.ok(
      !emptyState.textContent!.includes('No log or record found'),
      'does not misreport known run as missing'
    );

    // No error banner
    const errorBanner = root.querySelector('.banner-error');
    assert.equal(errorBanner, null, 'no error banner rendered');
  } finally {
    view.unmount();
  }
});

test('a flow Log tab polls its live run and updates with new lines', async () => {
  const issue = 42;
  const liveRunId = 'r-1a2b3c4d';
  const view = await mountAt(`#/flow/${issue}`, { pollInterval: 20 });
  try {
    const root = view.target;
    const initialCount = root.querySelectorAll('.run-page-log .log-line').length;
    assert.ok(initialCount > 0, 'initial log lines rendered in flow Log tab');

    // Append a line to dynamicLogs for the flow's live run
    dynamicLogs[liveRunId].push({ kind: 'text', text: 'newly polled line on flow log tab' });

    // Wait for poll
    await new Promise((r) => setTimeout(r, 60));
    await settle();

    const afterCount = root.querySelectorAll('.run-page-log .log-line').length;
    assert.equal(afterCount, initialCount + 1, 'new line rendered after poll on flow log tab');
    assert.ok(root.textContent!.includes('newly polled line on flow log tab'));
  } finally {
    view.unmount();
  }
});

test('a PR-specific flow route does not show another thread live run or active flow', async () => {
  // #/flow/42?pr=57 addresses PR 57 on issue 42, but r-1a2b3c4d is pre-PR and active_flow is pre-PR
  const view = await mountAt('#/flow/42?pr=57');
  try {
    const root = view.target;

    // Header target is #42
    const headerTitle = root.querySelector('.header-title');
    assert.ok(headerTitle?.textContent?.includes('#42'));

    // Should NOT show pre-PR round 1/3 from the active flow
    const headerMeta = root.querySelector('.header-meta');
    assert.ok(!headerMeta?.textContent?.includes('round 1/3'), 'does not show pre-PR active flow round');

    // Should NOT show r-1a2b3c4d's model or elapsed
    assert.ok(!headerMeta?.textContent?.includes('model opus'), 'does not show pre-PR live run model');

    // No raw log link for r-1a2b3c4d
    const rawLink = root.querySelector('.log-raw-link');
    assert.equal(rawLink, null, 'no raw log link pointing to unrelated live run');

    // Log section displays empty state without error banner
    const emptyState = root.querySelector('.empty-state');
    assert.ok(emptyState, 'empty state rendered in log section');
    assert.equal(root.querySelector('.banner-error'), null, 'no error banner rendered');
  } finally {
    view.unmount();
  }
});

test('a pre-PR flow after an older PR loads the pre-PR timeline, not the latest PR', async () => {
  const run = (id: string, created: string, extra: Record<string, any>) => ({
    id,
    target: '#80',
    issue: 80,
    issue_title: 'PR then pre-PR',
    kind: 'implement',
    actor: 'claude',
    family: 'anthropic',
    harness: 'claude',
    mode: 'batch',
    status: 'succeeded',
    duration: '5m 0s',
    created,
    summary: id,
    purged: false,
    denials: [],
    ...extra,
  });
  const overrides = {
    history: [
      run('r-80-pr10', '2026-11-01T08:00:00Z', { pr: 10, target: '!10', thread_key: 'flow:issue:80:pr:10' }),
      run('r-80-prepr', '2026-11-03T08:00:00Z', { thread_key: 'flow:issue:80' }),
    ],
    timelines: {
      // Without ?pr the server answers with the latest PR's thread.
      '80': {
        issue: 80,
        pr: 10,
        thread_key: 'flow:issue:80:pr:10',
        title: 'PR then pre-PR',
        events: [
          { type: 'pr_opened', time: '2026-11-01T08:05:00Z', title: 'Pull request !10 opened', url: 'http://forge/pulls/10' },
        ],
      },
      '80:0': {
        issue: 80,
        thread_key: 'flow:issue:80',
        title: 'PR then pre-PR',
        events: [
          { type: 'issue_created', time: '2026-11-03T07:00:00Z', title: 'Pre-PR thread started' },
        ],
      },
    },
    logs: {
      'r-80-pr10': [{ kind: 'text', text: 'Output from PR 10 run' }],
      'r-80-prepr': [{ kind: 'text', text: 'Output from pre-PR run' }],
    },
  };
  const view = await mountAt('#/flow/80', {}, overrides);
  try {
    const root = view.target;
    assert.ok(root.textContent?.includes('Output from pre-PR run'), 'log is the pre-PR run');
    assert.ok(
      timelineRequests.includes('/api/issues/80/timeline?pr=0'),
      `requests the pre-PR timeline: ${timelineRequests.join(', ')}`
    );
    assert.ok(!timelineRequests.includes('/api/issues/80/timeline'), 'does not request the latest-PR timeline');

    const timelineTab = [...root.querySelectorAll('.flow-tab')].find((t) => t.textContent!.trim() === 'Timeline') as HTMLElement;
    timelineTab.click();
    await settle();
    const section = root.querySelector('.flow-timeline-section')!;
    assert.ok(section.textContent?.includes('Pre-PR thread started'), 'shows the pre-PR timeline');
    assert.ok(!section.textContent?.includes('Pull request !10 opened'), 'does not mix in the older PR');
  } finally {
    view.unmount();
  }
});

test('same-issue runs associated with different PRs isolate active run, log, and header', async () => {
  const overrides = {
    history: [
      {
        id: 'r-70-pr10',
        target: '!10',
        target_url: 'http://forge/pulls/10',
        issue: 70,
        issue_title: 'Multi-PR issue',
        pr: 10,
        pr_title: 'PR 10 title',
        thread_key: 'flow:issue:70:pr:10',
        kind: 'implement',
        actor: 'claude',
        family: 'anthropic',
        harness: 'claude',
        spec: 'claude-haiku',
        mode: 'batch',
        status: 'succeeded',
        duration: '10m 0s',
        created: '2026-11-02T08:00:00Z',
        summary: 'PR 10 work',
        purged: false,
        denials: [],
      },
    ],
    live_runs: [
      {
        id: 'r-70-pr20-live',
        target: '!20',
        target_url: 'http://forge/pulls/20',
        issue: 70,
        issue_title: 'Multi-PR issue',
        pr: 20,
        pr_title: 'PR 20 title',
        thread_key: 'flow:issue:70:pr:20',
        kind: 'verify',
        actor: 'kimi',
        family: 'moonshot',
        harness: 'kimi',
        spec: 'kimi-k2',
        interactive: true,
        created: '2026-11-02T09:00:00Z',
        expires: '2026-11-02T10:00:00Z',
        elapsed: '5m 0s',
        timeout: '45m',
        flow: {
          issue: 70,
          round: 2,
          max_rounds: 3,
          step: 'verifying with kimi',
          label: '#70 · round 2/3 · verifying with kimi',
        },
        log_tail: [],
        denials: [],
      },
    ],
    active_flows: [
      {
        issue: 70,
        implement: 'claude',
        verify: 'kimi',
        round: 2,
        max_rounds: 3,
        step: 'verifying with kimi',
        label: '#70 · round 2/3 · verifying with kimi',
        waiting: false,
      },
    ],
    logs: {
      'r-70-pr10': [{ kind: 'text', text: 'Output from PR 10 run' }],
      'r-70-pr20-live': [{ kind: 'text', text: 'Live output from PR 20 verify' }],
    },
  };

  // 1. Visit PR 10 route: should show ended run r-70-pr10, not the live PR 20 run
  const view10 = await mountAt('#/flow/70?pr=10', {}, overrides);
  try {
    const root10 = view10.target;
    const headerMeta10 = root10.querySelector('.header-meta');
    assert.ok(headerMeta10?.textContent?.includes('model claude-haiku'), 'shows PR 10 run model');
    assert.ok(headerMeta10?.textContent?.includes('batch'), 'shows PR 10 batch mode');
    assert.ok(headerMeta10?.textContent?.includes('10m 0s'), 'shows PR 10 duration');
    assert.ok(!headerMeta10?.textContent?.includes('round 2/3'), 'does not show PR 20 active round on PR 10');

    // Raw log link points to r-70-pr10
    const rawLink10 = root10.querySelector('.log-raw-link');
    assert.ok(rawLink10?.getAttribute('href')?.includes('r-70-pr10'), 'raw log links to PR 10 run');

    // Log text has PR 10 content and not PR 20
    assert.ok(root10.textContent?.includes('Output from PR 10 run'));
    assert.ok(!root10.textContent?.includes('Live output from PR 20 verify'));
  } finally {
    view10.unmount();
  }

  // 2. Visit PR 20 route: should show live run r-70-pr20-live
  const view20 = await mountAt('#/flow/70?pr=20', {}, overrides);
  try {
    const root20 = view20.target;
    const headerMeta20 = root20.querySelector('.header-meta');
    assert.ok(headerMeta20?.textContent?.includes('model kimi-k2'), 'shows PR 20 run model');
    assert.ok(headerMeta20?.textContent?.includes('interactive'), 'shows PR 20 interactive mode');
    assert.ok(headerMeta20?.textContent?.includes('/ 45m'), 'shows PR 20 elapsed against limit');
    assert.ok(headerMeta20?.textContent?.includes('round 2/3'), 'shows PR 20 active round');

    // Raw log link points to r-70-pr20-live
    const rawLink20 = root20.querySelector('.log-raw-link');
    assert.ok(rawLink20?.getAttribute('href')?.includes('r-70-pr20-live'), 'raw log links to PR 20 run');

    // Log text has PR 20 live content and not PR 10
    assert.ok(root20.textContent?.includes('Live output from PR 20 verify'));
    assert.ok(!root20.textContent?.includes('Output from PR 10 run'));
  } finally {
    view20.unmount();
  }
});
