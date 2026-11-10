// The run page's log in increments (logcursor.ts, design §4): after every
// poll the page's lines equal a full load's, a reset replaces them, and
// when the run ends the page loads the whole log without a cursor.
import test from 'node:test';
import assert from 'node:assert/strict';
import { render, flushSync } from '../testing/dom.ts';
import { reactiveProps } from '../testing/props.svelte.js';
import { applyLog, logURL, type LogCursor, type LogResponse } from '../src/logcursor.ts';
import RunFlowPage from '../src/RunFlowPage.svelte';
import type { LogLine } from '../src/types.ts';

// jsdom has no window.scrollTo.
Object.defineProperty(window, 'scrollTo', { configurable: true, writable: true, value: () => {} });

// serve answers GET /api/runs/ID/log as the server does (pageLog in
// internal/webui/logpage.go): the stable prefix stops at the first running
// call, or before a live log's last line; its digest is opaque to the page.
function serve(log: LogLine[], ended: boolean, url: string): LogResponse {
  const q = new URL(url, 'http://localhost/').searchParams;
  const running = log.findIndex((l) => l.kind === 'tool' && l.state === 'running');
  const stable = ended ? log.length : running >= 0 ? running : Math.max(log.length - 1, 0);
  const sum = (n: number) => JSON.stringify(log.slice(0, n));
  const res: LogResponse = { lines: log, stable, stable_sum: sum(stable) };
  if (!q.has('after')) return res;
  const n = Number(q.get('after'));
  if (!q.get('sum') || n > log.length || sum(n) !== q.get('sum')) return { ...res, reset: true };
  return { ...res, lines: log.slice(n) };
}

const text = (t: string): LogLine => ({ kind: 'text', text: t });
const call = (state: string): LogLine => ({ kind: 'tool', tool: 'bash', state, text: 'bash: make' });
const stdout = (t: string): LogLine => ({ kind: 'plain', text: t });

// The sequences of the Go tests (internal/webui/logpage_test.go): each
// step is the log as rendered at a poll, and whether the run has ended.
const sequences: Record<string, [LogLine[], boolean][]> = {
  grows: [
    [[text('a')], false],
    [[text('a'), text('b')], false],
    [[text('a'), text('b'), text('c d')], false],
  ],
  'a call completes': [
    [[text('a'), call('running')], false],
    [[text('a'), call('completed'), text('b')], false],
  ],
  'an unfinished call, final text, then its completion': [
    [[text('a'), call('running'), text('b')], false],
    [[text('a'), call('running'), text('b'), text('c')], false],
    [[text('a'), call('completed'), text('b'), text('c')], false],
    [[text('a'), call('completed'), text('b'), text('c')], true],
  ],
  'the transcript handoff': [
    [[{ kind: 'status', text: 'session started' }, text('Looking.'), text('Found it.')], false],
    [[text('one'), text('two'), text('three')], false],
    [[{ kind: 'status', text: 'session started' }, text('Looking.'), text('Found it.'), stdout('one'), stdout('two'), stdout('three')], false],
  ],
};

for (const [name, steps] of Object.entries(sequences)) {
  test(`increments equal a full load: ${name}`, () => {
    let cursor: LogCursor | null = null;
    for (const [log, ended] of steps) {
      const url = logURL('r-1', cursor);
      cursor = applyLog(cursor, serve(log, ended, url));
      assert.deepEqual(cursor.lines, serve(log, ended, logURL('r-1', null)).lines, url);
    }
  });
}

test('the transcript handoff resets the page, and an answer without a cursor loads the whole log next', () => {
  let cursor: LogCursor | null = applyLog(null, serve(sequences['the transcript handoff'][0][0], false, '/x'));
  assert.equal(cursor.stable, 2);
  const url = logURL('r-1', cursor);
  assert.match(url, /\?after=2&sum=/);
  const res = serve([text('one'), text('two'), text('three')], false, url);
  assert.equal(res.reset, true);
  cursor = applyLog(cursor, res);
  assert.deepEqual(cursor.lines, [text('one'), text('two'), text('three')]);

  // An older server sends no stable prefix: the next poll has no cursor.
  cursor = applyLog(cursor, { lines: [text('x')] });
  assert.equal(logURL('r-1', cursor), '/api/runs/r-1/log');
  // full ignores the cursor, and its answer replaces the lines.
  cursor = applyLog(null, serve([text('a'), text('b')], false, '/x'));
  assert.equal(logURL('r-1', cursor, true), '/api/runs/r-1/log');
  assert.deepEqual(applyLog(cursor, { lines: [text('z')], stable: 1, stable_sum: 's' }, true).lines, [text('z')]);
});

const settle = async (times = 8) => {
  for (let i = 0; i < times; i++) {
    await new Promise((r) => setTimeout(r, 5));
    flushSync();
  }
};

test('the run page polls in increments, applies resets, and loads the whole log when the run ends', async () => {
  let log: LogLine[] = [text('a')];
  let ended = false;
  const urls: string[] = [];
  const saved = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.startsWith('/api/runs/r-1/log')) {
      urls.push(url);
      // The answer once the run has ended differs from what the polls
      // built, so the page must show exactly it.
      const body = ended && !url.includes('after=') ? { lines: [text('final')], stable: 1, stable_sum: 'f' } : serve(log, ended, url);
      return new Response(JSON.stringify(body));
    }
    return new Response('{}', { status: 404 });
  }) as typeof fetch;
  const live = {
    id: 'r-1', target: '#42', target_url: '', issue: 42, kind: 'implement', actor: 'claude', family: 'anthropic', harness: 'claude',
    interactive: false, created: new Date().toISOString(), expires: '', elapsed: '1m', timeout: '1h', log_tail: [], denials: [],
  };
  const props = reactiveProps({
    route: { kind: 'run', id: 'r-1' } as const,
    status: { live_runs: [live], live_checks: [], active_flows: [] } as any,
    historyRuns: [] as any[],
    now: Date.now(),
    pollInterval: 10,
  });
  const view = render(RunFlowPage, props);
  const shown = () => [...view.target.querySelectorAll('.run-page-log .log-line')].map((e) => (e.querySelector('summary') ?? e).textContent!.trim());
  const full = () => serve(log, ended, '/api/runs/r-1/log').lines!.map((l) => (l.kind === 'tool' ? `bashmake· ${l.state}` : l.text));
  try {
    const steps: LogLine[][] = [
      [text('a'), text('b')],
      [text('a'), call('running'), text('b')],
      [text('a'), call('running'), text('b'), text('c')],
      [text('a'), call('completed'), text('b'), text('c')],
      [text('one'), text('two'), text('three'), text('four')], // another source: a reset
      [text('one'), text('two'), text('three'), text('four'), text('five')],
    ];
    await settle();
    for (const step of steps) {
      log = step;
      const before = urls.length;
      await settle();
      assert.ok(urls.length > before, 'the page polled');
      assert.match(urls[urls.length - 1], /\?after=\d+&sum=/, 'polls carry the cursor');
      assert.deepEqual(shown(), full());
    }

    // The run ends: the page asks for the whole log, without a cursor, and
    // shows that answer.
    ended = true;
    props.status = { live_runs: [], live_checks: [], active_flows: [] } as any;
    props.historyRuns = [{ ...live, status: 'succeeded', ended: new Date().toISOString() }];
    flushSync();
    await settle();
    assert.equal(urls[urls.length - 1], '/api/runs/r-1/log');
    assert.deepEqual(shown(), ['final']);
  } finally {
    view.unmount();
    globalThis.fetch = saved;
  }
});
