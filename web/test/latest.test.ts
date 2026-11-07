// LatestLoad (src/latest.ts): one load in flight per view, for its range.
import test from 'node:test';
import assert from 'node:assert/strict';
import { LatestLoad } from '../src/latest.ts';

test('LatestLoad drops a repeat of the running range and aborts another', () => {
  const latest = new LatestLoad();
  const a = latest.start('30d')!;
  assert.ok(a);
  assert.equal(latest.start('30d'), null, 'the same range is already loading');
  assert.equal(a.aborted, false);

  const b = latest.start('7d')!;
  assert.ok(a.aborted, 'another range aborts the running load');
  assert.equal(b.aborted, false);
  assert.equal(latest.finish(a), false, 'an aborted load is not current');
  assert.equal(latest.finish(b), true);
  assert.ok(latest.start('7d'), 'a finished range loads again');
});

test('LatestLoad.cancel aborts the running load', () => {
  const latest = new LatestLoad();
  assert.equal(latest.cancel(), false, 'nothing to cancel');
  const a = latest.start('custom')!;
  assert.equal(latest.cancel(), true);
  assert.ok(a.aborted);
  assert.equal(latest.finish(a), false);
});
