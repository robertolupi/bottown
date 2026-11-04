// Failure reasons in the run history (#132): describeRunFailure, the flow
// timeline and search that use it, and FailureReason.svelte, compiled for
// the server as Vite would compile it for the browser.
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { compile } from 'svelte/compiler';
import { render } from 'svelte/server';
import {
  buildFlowTimeline,
  describeRunFailure,
  failureText,
  filterHistoryThreads,
  groupHistoryThreads,
} from '../src/state.ts';
import type { HistoryRunInfo, RunFailure } from '../src/types.ts';

function run(over: Partial<HistoryRunInfo>): HistoryRunInfo {
  return {
    id: 'r-1', target: '#7', target_url: 'http://forge/issues/7', issue: 7, thread_key: 'flow:7',
    kind: 'implement', actor: 'claude', family: 'anthropic', harness: 'claude', mode: 'batch',
    status: 'failed', created: '2026-11-01T10:00:00Z', ended: '2026-11-01T10:05:00Z', duration: '5m',
    summary: '', purged: false, denials: [],
    ...over,
  };
}

test('describeRunFailure: only failed and revoked runs', () => {
  assert.equal(describeRunFailure(run({ status: 'succeeded', summary: 'Done' })), null);
  assert.equal(describeRunFailure(run({ status: 'running', ended: undefined })), null);
  assert.ok(describeRunFailure(run({ status: 'revoked' })));
});

test('describeRunFailure: a startup failure, before any log, says what failed', () => {
  const failure: RunFailure = { cause: 'startup', detail: "git worktree add: 'issue/132' is already used by worktree" };
  const d = describeRunFailure(run({ failure }))!;
  assert.equal(d.observed, 'Startup failed');
  assert.equal(d.observedDetail, failure.detail);
  assert.equal(d.reported, undefined);
  assert.equal(d.unavailable, false);
  assert.equal(d.hasLog, false);
});

test('describeRunFailure: a harness exit keeps the agent report apart', () => {
  const d = describeRunFailure(run({
    summary: 'API Error: 500 Internal server error.',
    failure: { cause: 'harness_exit', detail: 'the harness exited with code 1', exit_code: 1 },
  }))!;
  assert.equal(d.observed, 'Harness exited with code 1');
  // The detail only repeats the label.
  assert.equal(d.observedDetail, undefined);
  assert.equal(d.reported, 'API Error: 500 Internal server error.');
  assert.equal(d.hasLog, true);
  assert.equal(failureText(d), 'Observed: Harness exited with code 1 · Agent reported: API Error: 500 Internal server error.');
});

test('describeRunFailure: timeouts, stops and agent-reported blockers', () => {
  const timeout = describeRunFailure(run({ failure: { cause: 'timeout', detail: 'killed at the time limit, after 1h0m0s' } }))!;
  assert.equal(timeout.observed, 'Timed out');
  assert.equal(timeout.observedDetail, 'killed at the time limit, after 1h0m0s');
  const stopped = describeRunFailure(run({ failure: { cause: 'stopped', detail: 'stopped by the owner' } }))!;
  assert.equal(stopped.observed, 'Stopped by the owner');
  const blocked = describeRunFailure(run({
    summary: 'Blocked: the issue covers one binary of three.',
    failure: { cause: 'reported', detail: 'the agent ended the run as failed' },
  }))!;
  assert.equal(blocked.observed, 'Agent ended the run as failed');
  assert.equal(blocked.reported, 'Blocked: the issue covers one binary of three.');
  // An unknown cause is shown as recorded, not dropped.
  const unknown = describeRunFailure(run({ failure: { cause: 'disk_full', detail: 'no space left' } }))!;
  assert.equal(unknown.observed, 'disk full');
  assert.equal(unknown.observedDetail, 'no space left');
});

test('describeRunFailure: no evidence is "Reason unavailable", never a guess', () => {
  const d = describeRunFailure(run({ summary: '' }))!;
  assert.equal(d.observed, undefined);
  assert.equal(d.reported, undefined);
  assert.equal(d.unavailable, true);
  assert.equal(failureText(d), 'Reason unavailable');
  // A summary alone is the agent's report, not an observed cause.
  const reported = describeRunFailure(run({ summary: 'Checks failed' }))!;
  assert.equal(reported.observed, undefined);
  assert.equal(reported.reported, 'Checks failed');
  assert.equal(reported.unavailable, false);
});

test('the flow timeline and search carry the failure reason', () => {
  const runs = [
    run({ id: 'r-1', status: 'succeeded', summary: 'Opened !9', created: '2026-11-01T09:00:00Z', ended: '2026-11-01T09:10:00Z' }),
    run({ id: 'r-2', failure: { cause: 'startup', detail: 'worktree already used' } }),
  ];
  const threads = groupHistoryThreads(runs, [], []);
  assert.equal(threads.length, 1);
  const entries = buildFlowTimeline(threads[0], null);
  const ended = (id: string) => entries
    .flatMap((e) => e.entry_type === 'run_boundary' && e.run.id === id ? e.events : [])
    .find((ev) => ev.type === 'run_ended');
  assert.equal(ended('r-1')?.detail, 'Opened !9');
  assert.equal(ended('r-2')?.detail, 'Observed: Startup failed (worktree already used)');
  const filters = { actor: '', kind: '', status: '', date: '', search: 'already used' };
  assert.equal(filterHistoryThreads(threads, filters, Date.now()).length, 1);
});

async function loadFailureReason(): Promise<any> {
  const src = fileURLToPath(new URL('../src/FailureReason.svelte', import.meta.url));
  const { js } = compile(readFileSync(src, 'utf8'), { filename: src, generate: 'server' });
  const code = js.code.replace(/from ['"]\.\/(state|truncation)['"]/g, (_, m) =>
    `from '${pathToFileURL(fileURLToPath(new URL(`../src/${m}.ts`, import.meta.url))).href}'`);
  // Inside node_modules so that the compiled module resolves svelte.
  const dir = new URL('../node_modules/.cache/bottown-test/', import.meta.url);
  mkdirSync(dir, { recursive: true });
  const out = new URL(`failure-${process.pid}.js`, dir);
  writeFileSync(out, code);
  return (await import(pathToFileURL(fileURLToPath(out)).href)).default;
}

function text(html: string): string {
  return html.replace(/<!--.*?-->/gs, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim();
}

test('FailureReason labels observed facts apart from the agent report', async () => {
  const View = await loadFailureReason();
  const d = describeRunFailure(run({
    summary: 'API Error: 500',
    failure: { cause: 'harness_exit', exit_code: 1 },
  }))!;
  const { body } = render(View, { props: { failure: d, onDetails: () => {} } });
  assert.match(body, /failure-observed/);
  assert.match(text(body), /Observed Harness exited with code 1/);
  assert.match(text(body), /Agent reported API Error: 500/);
  assert.match(text(body), /View log/);
});

test('FailureReason without evidence says so and still offers details', async () => {
  const View = await loadFailureReason();
  const startup = describeRunFailure(run({ failure: { cause: 'startup', detail: 'boom' } }))!;
  assert.match(text(render(View, { props: { failure: startup, onDetails: () => {} } }).body), /View details/);
  const none = describeRunFailure(run({}))!;
  const { body } = render(View, { props: { failure: none, onDetails: () => {} } });
  assert.match(text(body), /Reason unavailable/);
  assert.doesNotMatch(text(body), /Observed|Agent reported/);
  assert.match(text(body), /View log/);
});
