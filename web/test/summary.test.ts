// History summaries (#133): a clipped summary or failure reason offers to
// show all of it inline, not only in a hover title, and the history
// table's header stays in view as its rows scroll.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { anyTruncated, isTruncated } from '../src/truncation.ts';

const src = (f: string) => readFileSync(new URL(`../src/${f}`, import.meta.url), 'utf8');

test('isTruncated: text wider than its box is clipped', () => {
  assert.equal(isTruncated({ scrollWidth: 400, clientWidth: 280 }), true);
  assert.equal(isTruncated({ scrollWidth: 280, clientWidth: 280 }), false);
  // Subpixel rounding is not truncation.
  assert.equal(isTruncated({ scrollWidth: 281, clientWidth: 280 }), false);
});

test('anyTruncated: any clipped line counts', () => {
  assert.equal(anyTruncated([]), false);
  assert.equal(anyTruncated([{ scrollWidth: 10, clientWidth: 10 }, { scrollWidth: 90, clientWidth: 10 }]), true);
});

test('every history summary cell can expand its text inline', () => {
  for (const f of ['FlowThreadRow.svelte', 'DiscussThreadRow.svelte', 'ConstituentRuns.svelte']) {
    const s = src(f);
    const cells = [...s.matchAll(/<td class="summary-cell"[^>]*>([\s\S]*?)<\/td>/g)].map((m) => m[1]);
    assert.equal(cells.length, 2, `${f}: a failure cell and a summary cell`);
    assert.ok(cells.some((c) => /<FailureReason\b/.test(c)), `${f}: failure cell`);
    assert.ok(cells.some((c) => /<SummaryText\b/.test(c)), `${f}: summary cell`);
  }
});

test('summaries and failure reasons toggle between one line and all of it', () => {
  for (const f of ['SummaryText.svelte', 'FailureReason.svelte']) {
    const s = src(f);
    assert.match(s, /use:watchTruncation=/, `${f}: measures its clipped lines`);
    assert.match(s, /aria-expanded=\{expanded\}/, `${f}: an accessible toggle`);
    assert.match(s, /\{expanded \? 'less' : 'more'\}/, `${f}: more and less`);
    assert.match(s, /\.expanded \.[\w-]+ \{\s*white-space: pre-wrap;/, `${f}: expanded text wraps`);
    // The flow row's own click expands the flow; the toggle must not.
    assert.match(s, /e\.stopPropagation\(\);\s*expanded = !expanded;/, `${f}: toggle stops propagation`);
  }
});

test('the history table header sticks to the top of its scroll container', () => {
  const s = src('RunHistory.svelte');
  assert.match(s, /\.history-table-container \{[^}]*overflow: auto;/);
  assert.match(s, /\.history-table-container :global\(thead\) \{[^}]*position: sticky;[^}]*inset-block-start: 0;/);
});
