import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { render } from '../testing/dom.ts';
import OwnerInbox from '../src/OwnerInbox.svelte';
import LiveRuns from '../src/LiveRuns.svelte';
import Milestones from '../src/Milestones.svelte';
import { flushSync } from '../testing/dom.ts';
import StopModal from '../src/StopModal.svelte';

const fixture = JSON.parse(readFileSync(new URL('../fixtures/default.json', import.meta.url), 'utf8'));

test('approved behind candidate offers Land and explains the combined checks', () => {
  const candidate = fixture.inbox.find((i: any) => i.target === '!56');
  const calls: unknown[] = [];
  const view = render(OwnerInbox, { inbox: [candidate], inboxError: undefined, onGC: () => {}, onStop: () => {}, onLand: (number: number, head: string) => calls.push([number, head]) });
  try {
    assert.match(view.target.textContent!, /ready to land/);
    assert.match(view.target.textContent!, /skips the checks at the combined tree/);
    const buttons = [...view.target.querySelectorAll('button')];
    assert.ok(!buttons.some((b) => b.textContent?.includes('Merge main')));
    buttons.find((b) => b.textContent?.trim() === 'Land')!.click();
    assert.deepEqual(calls, [[56, candidate.head_sha]]);
  } finally { view.unmount(); }
});

test('a landing in progress renders under Live Runs with its log and stop control', () => {
  const landing = fixture.live_runs.find((r: any) => r.kind === 'land');
  const stops: string[] = [];
  const view = render(LiveRuns, { liveRuns: [landing], liveChecks: [], activeFlows: [], issuesLink: '', now: Date.now(), onStartFlow: () => {}, onStartRun: () => {}, onGC: () => {}, onStop: (id: string) => stops.push(id) });
  try {
    assert.match(view.target.textContent!, /land/);
    assert.match(view.target.textContent!, /Running checks at the landing commit/);
    [...view.target.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Stop')!.click();
    assert.deepEqual(stops, ['land-59']);
  } finally { view.unmount(); }
});

test('stopping a landing explains that the PR may remain unmerged', () => {
  const view = render(StopModal, { runId: 'land-56', actionLoading: false, onconfirm: () => {}, onclose: () => {} });
  try { assert.match(view.target.textContent!, /Stop Landing/); assert.match(view.target.textContent!, /unmerged/); }
  finally { view.unmount(); }
});


test('a milestone child shows its landing on the node', async () => {
  globalThis.fetch = (async () => new Response(JSON.stringify(fixture.milestones), { status: 200 })) as typeof fetch;
  const view = render(Milestones, { refreshKey: 0, onFlow: () => {}, onRun: () => {}, onMilestoneRun: () => {}, onMilestoneStop: () => {} });
  try {
    for (let i = 0; i < 4; i++) { await new Promise((r) => setTimeout(r, 5)); flushSync(); }
    assert.match(view.target.textContent!, /land · bottown · 2m 0s/);
  } finally { view.unmount(); }
});
