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
    // Needs you starts collapsed; its items are in the expanded list.
    view.target.querySelector<HTMLButtonElement>('.needs-you-toggle')!.click();
    flushSync();
    assert.match(view.target.textContent!, /ready to land/);
    assert.match(view.target.textContent!, /skips the checks at the combined tree/);
    const buttons = [...view.target.querySelectorAll('button')];
    assert.ok(!buttons.some((b) => b.textContent?.includes('Merge main')));
    buttons.find((b) => b.textContent?.trim() === 'Land')!.click();
    assert.deepEqual(calls, [[56, candidate.head_sha]]);
  } finally { view.unmount(); }
});

test('a landing in progress is a tile in Live with its log, opening its page', () => {
  const landing = fixture.live.items.find((i: any) => i.kind === 'land');
  // The preview fills a tile's tail from the fixture's logs.
  const item = { ...landing, tail: fixture.logs[landing.id] };
  const live = { ...fixture.live, items: [item], rows: [], ended: [] };
  const view = render(LiveRuns, { live, now: Date.now(), onStartFlow: () => {}, onStartRun: () => {}, onGC: () => {} });
  try {
    const tile = view.target.querySelector<HTMLElement>('.now-tile')!;
    assert.match(tile.textContent!, /land/);
    assert.match(tile.textContent!, /Running checks at the landing commit/);
    tile.click();
    assert.equal(location.hash, '#/run/land-59');
  } finally { view.unmount(); location.hash = ''; }
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
