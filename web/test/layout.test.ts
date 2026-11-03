// The page layout (#154): the dashboard spans the viewport, and its grid
// columns may shrink below their content, so wide tables and log lines
// scroll in their card rather than widening the page on a narrow screen.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const src = (f: string) => readFileSync(new URL(`../src/${f}`, import.meta.url), 'utf8');

function rule(css: string, selector: string): string {
  const m = css.match(new RegExp(`(^|[\\s}])${selector.replace('.', '\\.')}\\s*\\{([^}]*)\\}`));
  assert.ok(m, `no ${selector} rule`);
  return m[2];
}

test('the page container has no maximum width', () => {
  const container = rule(src('App.svelte'), '.container');
  assert.doesNotMatch(container, /max-width/);
  assert.match(container, /padding:/);
});

test('the grid columns can shrink below their content', () => {
  const css = src('global.css');
  const columns = [...css.matchAll(/\.grid\s*\{([^}]*)\}/g)].map((m) => m[1]);
  assert.equal(columns.length, 2, 'the grid and its narrow-screen override');
  for (const body of columns) {
    assert.match(body, /grid-template-columns:[^;]*minmax\(0,\s*1fr\)/);
  }
});
