// The live poll's refresh decisions. Run with `npm test`: node --test, with
// Node stripping the types.
import test from 'node:test';
import assert from 'node:assert/strict';
import { lifecycleChange } from '../src/lifecycle.ts';

const run = (id: string, issue?: number) =>
  ({ id, flow: issue === undefined ? undefined : { issue } }) as any;
const flow = (issue: number, step = 'implement') => ({ issue, step }) as any;

const both = { status: true, history: true };
const statusOnly = { status: true, history: false };
const none = { status: false, history: false };

test('one of two live runs completing refreshes history while the other stays live', () => {
  assert.deepEqual(lifecycleChange([run('r-a'), run('r-b')], [], [run('r-b')], []), both);
});

test('a run replaced by another between polls is detected with equal counts', () => {
  assert.deepEqual(lifecycleChange([run('r-a'), run('r-b')], [], [run('r-a'), run('r-c')], []), both);
});

test('a new run refreshes status but not history', () => {
  assert.deepEqual(lifecycleChange([run('r-a')], [], [run('r-a'), run('r-b')], []), statusOnly);
});

test('the last run ending refreshes both', () => {
  assert.deepEqual(lifecycleChange([run('r-a')], [], [], []), both);
});

test('a flow starting refreshes status', () => {
  assert.deepEqual(lifecycleChange([], [], [], [flow(7)]), statusOnly);
});

test('a flow completing while another continues refreshes both', () => {
  assert.deepEqual(lifecycleChange([], [flow(7), flow(8)], [], [flow(8)]), both);
});

test('a flow replaced by another between polls is detected with equal counts', () => {
  assert.deepEqual(lifecycleChange([], [flow(7)], [], [flow(9)]), both);
});

test('a flow moving from its implement run to its verify run refreshes history', () => {
  assert.deepEqual(
    lifecycleChange([run('r-impl', 7)], [flow(7)], [run('r-ver', 7)], [flow(7, 'verify')]),
    both,
  );
});

test('an unchanged snapshot, reordered or with progress within a flow, refreshes nothing', () => {
  assert.deepEqual(
    lifecycleChange([run('r-a'), run('r-b', 7)], [flow(7)], [run('r-b', 7), run('r-a')], [flow(7, 'verify')]),
    none,
  );
});

test('empty and missing lists are the same snapshot', () => {
  assert.deepEqual(lifecycleChange(undefined, null, [], []), none);
});

test('a live run opening a PR transitions its thread key and refreshes status and history', () => {
  const before = [{ id: 'r-open', issue: 20, thread_key: 'flow:issue:20' }] as any;
  const after = [{ id: 'r-open', issue: 20, pr: 21, thread_key: 'flow:issue:20:pr:21' }] as any;
  assert.deepEqual(lifecycleChange(before, [flow(20)], after, [flow(20)]), both);
});
