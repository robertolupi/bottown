import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { render } from '../testing/dom.ts';
import OwnerInbox from '../src/OwnerInbox.svelte';
import { flushSync } from '../testing/dom.ts';

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
    assert.match(view.target.textContent!, /bottown merge-all 56/);
    assert.deepEqual(calls, []);
  } finally { view.unmount(); }
});
