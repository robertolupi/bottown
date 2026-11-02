// reloadOn's effect wiring, run against Svelte's client runtime: the real
// src/reload.svelte.ts is compiled here, as Vite would, and imported.
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import * as $ from 'svelte/internal/client';
import { compileModule } from 'svelte/compiler';

async function loadReloadOn(): Promise<(key: () => unknown, load: () => unknown) => void> {
  const src = fileURLToPath(new URL('../src/reload.svelte.ts', import.meta.url));
  const { js } = compileModule(stripTypeScriptTypes(readFileSync(src, 'utf8')), {
    filename: src,
    generate: 'client',
  });
  // Node resolves bare 'svelte' to its server build; the browser build
  // Vite bundles re-exports untrack from the client runtime.
  const code = js.code.replace(/from ['"]svelte['"]/g, "from 'svelte/internal/client'");
  // Inside node_modules so that the compiled module resolves svelte.
  const dir = new URL('../node_modules/.cache/bottown-test/', import.meta.url);
  mkdirSync(dir, { recursive: true });
  const out = new URL(`reload-${process.pid}.js`, dir);
  writeFileSync(out, code);
  return (await import(pathToFileURL(fileURLToPath(out)).href)).reloadOn;
}

const tick = () => new Promise((r) => setTimeout(r, 5));

test('reloadOn loads once per key change, not again when a load completes', async () => {
  const reloadOn = await loadReloadOn();
  const key = $.state(0);
  // Like Milestones.svelte's load: a reactive in-flight flag, read before
  // the first await and cleared when the request finishes.
  const loading = $.state(false);
  let calls = 0;
  const pending: Array<() => void> = [];
  async function load() {
    if ($.get(loading)) return;
    $.set(loading, true);
    calls++;
    try {
      await new Promise<void>((r) => pending.push(r));
    } finally {
      $.set(loading, false);
    }
  }
  const settle = async () => {
    while (pending.length) pending.shift()!();
    for (let i = 0; i < 4; i++) {
      await tick();
      $.flush();
    }
  };

  const cleanup = $.effect_root(() => reloadOn(() => $.get(key), load));
  try {
    $.flush();
    assert.equal(calls, 1, 'loads on mount');
    await settle();
    assert.equal(calls, 1, 'a completed load must not start another');

    $.set(key, 1);
    $.flush();
    assert.equal(calls, 2, 'a key change loads again');
    await settle();
    assert.equal(calls, 2, 'a completed reload must not start another');

    // A timer calling load directly, as the 30-second poll does.
    void load();
    await settle();
    assert.equal(calls, 3, 'a direct load runs once');
  } finally {
    cleanup();
  }
});
