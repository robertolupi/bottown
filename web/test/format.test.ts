// The UI's formatting helpers and postAction. Run with `npm test`: node
// --test, with Node stripping the types.
import test from 'node:test';
import assert from 'node:assert/strict';
import { formatElapsed, formatRelativeTime, formatCost, formatUsage, statusTone } from '../src/format.ts';

test('formatElapsed counts minutes and padded seconds since created', () => {
  const created = '2026-11-02T12:00:00Z';
  const at = (s: number) => Date.parse(created) + s * 1000;
  assert.equal(formatElapsed(created, at(0)), '0m 00s');
  assert.equal(formatElapsed(created, at(425)), '7m 05s');
  assert.equal(formatElapsed(created, at(-5)), '0m 00s', 'never negative');
  assert.equal(formatElapsed('', at(5)), '');
  assert.equal(formatElapsed(created, at(192 * 60 + 7)), '3h 12m', 'hours past an hour');
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

test('formatRelativeTime gives a coarse age, and formatCost a cost', () => {
  const now = Date.parse('2026-11-10T12:00:00Z');
  const ago = (s: number) => new Date(now - s * 1000).toISOString();
  assert.equal(formatRelativeTime(ago(5), now), 'just now');
  assert.equal(formatRelativeTime(ago(12 * 60), now), '12m ago');
  assert.equal(formatRelativeTime(ago(3 * 3600), now), '3h ago');
  assert.equal(formatRelativeTime(ago(2 * 86400), now), '2d ago');
  assert.equal(formatRelativeTime(ago(45 * 86400), now), '1mo ago');
  assert.equal(formatRelativeTime(ago(400 * 86400), now), '1y ago');
  assert.equal(formatRelativeTime(ago(-30), now), 'just now', 'never a future age');
  assert.equal(formatRelativeTime('', now), '');
  assert.equal(formatRelativeTime('not-a-date', now), 'not-a-date');

  assert.equal(formatCost(undefined), '');
  assert.equal(formatCost({ cost_usd: 0 }), '$0.00');
  assert.equal(formatCost({ input_tokens: 100 }), '', 'tokens alone are no cost');
  assert.equal(formatCost({ cost_usd: 1.239 }), '$1.24');
});
