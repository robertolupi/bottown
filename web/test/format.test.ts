// The UI's formatting helpers and postAction. Run with `npm test`: node
// --test, with Node stripping the types.
import test from 'node:test';
import assert from 'node:assert/strict';
import { formatElapsed, formatUsage, statusTone } from '../src/format.ts';
import { postAction } from '../src/actions.ts';

test('formatElapsed counts minutes and padded seconds since created', () => {
  const created = '2026-11-02T12:00:00Z';
  const at = (s: number) => Date.parse(created) + s * 1000;
  assert.equal(formatElapsed(created, at(0)), '0m 00s');
  assert.equal(formatElapsed(created, at(425)), '7m 05s');
  assert.equal(formatElapsed(created, at(-5)), '0m 00s', 'never negative');
  assert.equal(formatElapsed('', at(5)), '');
});

test('formatUsage joins tokens, cost and turns', () => {
  assert.equal(formatUsage(undefined), '—');
  assert.equal(formatUsage({}), '—');
  assert.equal(
    formatUsage({ input_tokens: 120000, output_tokens: 18000, cost_usd: 1.2345, turns: 42 }),
    '120.0k in, 18.0k out · $1.234 · 42 turns',
  );
  assert.equal(formatUsage({ output_tokens: 500, turns: 1 }), '0.5k out · 1 turn');
  assert.equal(formatUsage({ cost_usd: 0 }), '$0.000');
});

test('statusTone maps execution statuses to badge tones', () => {
  assert.equal(statusTone('succeeded'), 'success');
  assert.equal(statusTone('failed'), 'error');
  assert.equal(statusTone('running'), 'accent');
  assert.equal(statusTone('revoked'), 'warning');
});

function stubFetch(ok: boolean, body: unknown) {
  const calls: Array<{ path: string; init: RequestInit }> = [];
  globalThis.fetch = (async (path: string, init: RequestInit) => {
    calls.push({ path, init });
    return { ok, json: async () => body };
  }) as any;
  return calls;
}

test('postAction posts JSON, or no body, and returns the result', async () => {
  let calls = stubFetch(true, { success: true, output: 'done' });
  assert.deepEqual(await postAction('/api/actions/gc', { failed: true }, 'GC failed'), { success: true, output: 'done' });
  assert.deepEqual(calls, [
    {
      path: '/api/actions/gc',
      init: { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"failed":true}' },
    },
  ]);

  calls = stubFetch(true, { success: true, output: '' });
  await postAction('/api/actions/pull', undefined, 'Pull failed');
  assert.deepEqual(calls[0].init, { method: 'POST' });
});

test('postAction throws the error, else the output, else the fallback', async () => {
  stubFetch(false, { success: false, output: 'out', error: 'boom' });
  await assert.rejects(postAction('/x', {}, 'X failed'), { message: 'boom' });
  stubFetch(true, { success: false, output: 'out' });
  await assert.rejects(postAction('/x', {}, 'X failed'), { message: 'out' });
  stubFetch(false, { success: false, output: '' });
  await assert.rejects(postAction('/x', {}, 'X failed'), { message: 'X failed' });
});
