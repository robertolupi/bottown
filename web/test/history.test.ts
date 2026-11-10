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

test('initial state and filters', () => {
  const h = new RunHistoryState();
  assert.deepEqual(h.runs, []);
  assert.equal(h.loading, false);
  assert.deepEqual(h.filters, { actor: '', kind: '', status: '', date: '', search: '' });
});

test('load fetches ended runs and handles background loading', async () => {
  const runs = [run('r-1', { issue: 12, ended: '2026-11-02T10:30:00Z' })];
  const calls = stubFetch({ '/api/runs?state=ended': runs });
  const h = new RunHistoryState();
  const live = () => ({ liveRuns: [], activeFlows: [] });

  const loading = h.load(live);
  assert.equal(h.loading, true, 'first foreground load shows loading note');
  await loading;
  assert.equal(h.loading, false);
  assert.deepEqual(h.runs, runs);
  assert.deepEqual(calls, ['/api/runs?state=ended']);

  const backgroundLoad = h.load(live, true);
  assert.equal(h.loading, false, 'background load does not set loading note');
  await backgroundLoad;
  assert.equal(calls.length, 2);
});

test('threads groups ended runs with live runs and active flows', () => {
  const h = new RunHistoryState();
  h.runs = [run('r-1', { issue: 12, pr: 70 })];
  const liveRuns = [run('r-2', { issue: 12, pr: 70, status: 'running' })];
  const activeFlows = [
    {
      issue: 12,
      implement: 'claude',
      verify: 'kimi',
      round: 1,
      max_rounds: 3,
      step: 'implementing',
      label: 'round 1',
      waiting: false,
    },
  ];

  const threads = h.threads(liveRuns, activeFlows);
  assert.equal(threads.length, 1);
  assert.equal(threads[0].kind, 'flow');
  assert.equal(threads[0].issue, 12);
  assert.equal(threads[0].run_count, 2);
  assert.equal(threads[0].active, true);
});
