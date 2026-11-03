// The checked-in `bottown web preview` fixtures, web/fixtures/*.json, run
// through the UI's own grouping: the Go tests check that they decode, these
// check that the views keyed by id render them (Svelte throws on a duplicate
// key in a keyed each).
import test from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { groupHistoryThreads } from '../src/state.ts';

const dir = new URL('../fixtures/', import.meta.url);
const fixtures = readdirSync(dir)
  .filter((f) => f.endsWith('.json'))
  .map((f) => ({ name: f, fx: JSON.parse(readFileSync(new URL(f, dir), 'utf8')) }));

function assertUnique(name: string, what: string, keys: unknown[]) {
  const seen = new Set<unknown>();
  for (const k of keys) {
    assert.ok(!seen.has(k), `${name}: duplicate ${what} ${String(k)}`);
    seen.add(k);
  }
}

test('fixtures are found', () => {
  assert.ok(fixtures.some((f) => f.name === 'default.json'));
});

for (const { name, fx } of fixtures) {
  test(`${name}: keyed views have unique keys`, () => {
    const threads = groupHistoryThreads(fx.history ?? [], fx.live_runs, fx.active_flows);
    assertUnique(name, 'history thread', threads.map((t) => t.key));
    for (const t of threads) assertUnique(name, `run in thread ${t.key}`, t.runs.map((r) => r.id));
    assertUnique(name, 'live run', (fx.live_runs ?? []).map((r: any) => r.id));
    assertUnique(name, 'active flow', (fx.active_flows ?? []).map((f: any) => f.issue));
    assertUnique(name, 'inbox item', (fx.inbox ?? []).map((i: any) => i.id));
  });
}
