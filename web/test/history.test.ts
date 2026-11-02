// RunHistoryState, the run history's state and loading, run against
// Svelte's client runtime: src/history.svelte.ts is compiled here, as Vite
// would, and imported, with fetch stubbed.
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { compileModule } from 'svelte/compiler';

async function loadHistoryState(): Promise<any> {
  const src = fileURLToPath(new URL('../src/history.svelte.ts', import.meta.url));
  const { js } = compileModule(stripTypeScriptTypes(readFileSync(src, 'utf8')), {
    filename: src,
    generate: 'client',
  });
  const state = new URL('../src/state.ts', import.meta.url).href;
  const code = js.code
    .replace(/from ['"]svelte['"]/g, "from 'svelte/internal/client'")
    .replace(/from ['"]\.\/state['"]/g, `from '${state}'`);
  // Inside node_modules so that the compiled module resolves svelte.
  const dir = new URL('../node_modules/.cache/bottown-test/', import.meta.url);
  mkdirSync(dir, { recursive: true });
  const out = new URL(`history-${process.pid}.js`, dir);
  writeFileSync(out, code);
  return (await import(pathToFileURL(fileURLToPath(out)).href)).RunHistoryState;
}

const RunHistoryState = await loadHistoryState();

// stubFetch answers each path with its body (200) or, for a number, that
// status; it records the paths fetched.
function stubFetch(routes: Record<string, unknown>): string[] {
  const calls: string[] = [];
  globalThis.fetch = (async (path: string) => {
    calls.push(path);
    const body = routes[path];
    if (body === undefined || typeof body === 'number') {
      return { ok: false, status: body ?? 404, statusText: 'Not Found', json: async () => ({}) };
    }
    return { ok: true, status: 200, statusText: 'OK', json: async () => body };
  }) as any;
  return calls;
}

const run = (id: string, extra: Record<string, unknown> = {}) =>
  ({ id, target: '#12', target_url: 'u', kind: 'implement', actor: 'claude', family: 'anthropic', status: 'succeeded', created: '2026-11-02T10:00:00Z', ...extra }) as any;
const flowThread = (key: string, runs: any[], extra: Record<string, unknown> = {}) =>
  ({ key, kind: 'flow', issue: 12, active: false, runs, actors: [], ...extra }) as any;

test('toggleLog loads a run log, then collapses it', async () => {
  const calls = stubFetch({ '/api/runs/r-1/log': { lines: [{ kind: 'text', text: 'hi' }] } });
  const h = new RunHistoryState();
  await h.toggleLog('r-1');
  assert.deepEqual(calls, ['/api/runs/r-1/log']);
  assert.deepEqual(h.logs['r-1'], { loading: false, lines: [{ kind: 'text', text: 'hi' }] });
  await h.toggleLog('r-1');
  assert.equal(h.logs['r-1'], undefined);
  assert.equal(calls.length, 1, 'collapsing does not fetch');
});

test('toggleLog records a failed load', async () => {
  stubFetch({ '/api/runs/r-2/log': 500 });
  const h = new RunHistoryState();
  await h.toggleLog('r-2');
  assert.deepEqual(h.logs['r-2'], { loading: false, error: 'Failed to load log' });
});

test('selectFlow expands one flow, loading its timeline, and collapses it', async () => {
  const timeline = { issue: 12, title: 't', state: 'open', url: 'u', events: [] };
  const calls = stubFetch({ '/api/issues/12/timeline?pr=70': timeline });
  const h = new RunHistoryState();
  const t = flowThread('flow:issue:12:pr:70', [run('r-1')], { pr: 70 });
  h.selectFlow(t);
  assert.equal(h.expandedThreadKey, t.key);
  assert.equal(h.timelineLoading[t.key], true, 'first load shows the loading note');
  await new Promise((r) => setTimeout(r, 0));
  assert.deepEqual(calls, ['/api/issues/12/timeline?pr=70']);
  assert.deepEqual(h.timelines[t.key], timeline);
  assert.equal(h.timelineLoading[t.key], false);
  h.selectFlow(t);
  assert.equal(h.expandedThreadKey, null);

  h.selectFlow({ ...t, kind: 'discuss' });
  assert.equal(h.expandedThreadKey, null, 'discuss threads do not expand');
});

test('a background timeline load keeps the last error away', async () => {
  stubFetch({});
  const h = new RunHistoryState();
  const t = flowThread('flow:issue:12', [run('r-1')]);
  await h.loadTimeline(t, true);
  assert.equal(h.timelineError[t.key], undefined);
  await h.loadTimeline(t);
  assert.equal(h.timelineError[t.key], 'Failed to load timeline for #12');
});

test('reconcile follows a pre-PR flow to its PR thread with its timeline', () => {
  const h = new RunHistoryState();
  const timeline = { issue: 12, events: [] };
  h.expandedThreadKey = 'flow:issue:12';
  h.timelines = { 'flow:issue:12': timeline };
  const prev = [flowThread('flow:issue:12', [run('r-1')])];
  const next = [flowThread('flow:issue:12:pr:70', [run('r-1'), run('r-2')], { pr: 70 })];
  h.reconcile(prev, next);
  assert.equal(h.expandedThreadKey, 'flow:issue:12:pr:70');
  assert.deepEqual(h.timelines['flow:issue:12:pr:70'], timeline);
});

test('load fetches the ended runs and refreshes the expanded flow in the background', async () => {
  const runs = [run('r-1', { issue: 12, ended: '2026-11-02T10:30:00Z' })];
  const calls = stubFetch({ '/api/runs?state=ended': runs, '/api/issues/12/timeline?pr=0': { issue: 12, events: [] } });
  const h = new RunHistoryState();
  const live = () => ({ liveRuns: [], activeFlows: [] });
  const loading = h.load(live);
  assert.equal(h.loading, true, 'a first load shows the loading note');
  await loading;
  assert.equal(h.loading, false);
  assert.deepEqual(h.runs, runs);
  const [thread] = h.threads([], []);
  assert.equal(calls.length, 1, 'nothing expanded, no timeline');

  h.expandedThreadKey = thread.key;
  const again = h.load(live, true);
  assert.equal(h.loading, false, 'a background load shows no loading note');
  await again;
  assert.equal(h.expandedThreadKey, thread.key);
  assert.deepEqual(calls.slice(1), ['/api/runs?state=ended', `/api/issues/12/timeline?pr=0`]);
});
