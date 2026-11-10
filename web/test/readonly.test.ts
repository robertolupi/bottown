import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { render, flushSync } from '../testing/dom.ts';
import CommandCopy from '../src/CommandCopy.svelte';
import OwnerInbox from '../src/OwnerInbox.svelte';
import UpstreamForge from '../src/UpstreamForge.svelte';
import Milestones from '../src/Milestones.svelte';
const fixture = JSON.parse(readFileSync(new URL('../fixtures/default.json', import.meta.url), 'utf8'));

test('frontend has no obsolete action URLs', () => {
  for (const file of readdirSync(new URL('../src', import.meta.url))) {
    const source = readFileSync(new URL(`../src/${file}`, import.meta.url), 'utf8');
    assert.deepEqual([...source.matchAll(/\/api\/actions\/([\w-]+)/g)].map(m => m[1]), [], file);
  }
});

test('CommandCopy writes the complete command and selects it without clipboard access', async () => {
  let copied = '';
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (s: string) => { copied = s; } } });
  const view = render(CommandCopy, { command: 'bottown run show r-1' });
  try {
    view.target.querySelector('button')!.click(); await new Promise(r => setTimeout(r, 0)); flushSync();
    assert.equal(copied, 'bottown run show r-1');
    assert.match(view.target.textContent!, /Copied/);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
    view.target.querySelector('button')!.click(); await new Promise(r => setTimeout(r, 0)); flushSync();
    assert.equal(window.getSelection()!.toString(), copied);
    assert.match(view.target.textContent!, /Selected/);
  } finally { view.unmount(); }
});

test('every actionable inbox kind displays its supplied command', () => {
  localStorage.clear();
  const kinds = ['ready_to_land', 'conflicts_with_base', 'stopped_flow', 'broken_run', 'discuss_answer', 'actor_filed_issue'];
  const inbox = kinds.map((kind, i) => ({ id: String(i), kind, title: kind, target: '#1', target_url: '#', description: '', action: { label: 'Action', command: `command ${i}` } }));
  const view = render(OwnerInbox, { inbox, inboxError: undefined });
  try {
    view.target.querySelector<HTMLButtonElement>('.needs-you-toggle')!.click(); flushSync();
    view.target.querySelector<HTMLInputElement>('input')!.click(); flushSync();
    for (let i = 0; i < kinds.length; i++) assert.match(view.target.textContent!, new RegExp(`command ${i}`));
  } finally { view.unmount(); localStorage.clear(); }
});

test('Repository shows pull, push and each after-pull command, and short SHAs', () => {
  const status = structuredClone(fixture.status);
  status.forge.behind_count = 1; status.forge.default_branch = 'trunk';
  status.upstream.ahead_count = 1; status.upstream.has_upstream = true;
  status.health.repo_settings.after_pull = ['make install', 'echo done'];
  const view = render(UpstreamForge, { status });
  try {
    for (const command of ['git pull --ff-only bottown trunk', 'git push origin trunk', 'make install', 'echo done']) assert.ok(view.target.textContent!.includes(command));
    assert.ok(view.target.textContent!.includes(status.forge.checkout_sha.slice(0, 8)));
    assert.ok(!view.target.textContent!.includes(status.forge.checkout_sha + ')'));
  } finally { view.unmount(); }
});

test('ready milestone nodes show labeled commands, placeholders and open the flow page', async () => {
  const group = structuredClone(fixture.milestones[0]);
  group.nodes = group.nodes.filter((n: any) => !n.external).slice(0, 2);
  group.edges = [];
  group.nodes.forEach((n: any) => { n.status = 'ready'; n.state = 'open'; n.run = undefined; n.flow = undefined; });
  group.nodes[0].implement = 'codex'; group.nodes[0].verify = 'claude';
  group.nodes[1].implement = ''; group.nodes[1].verify = '';
  globalThis.fetch = (async () => new Response(JSON.stringify([group]))) as typeof fetch;
  const view = render(Milestones, { refreshKey: 0 });
  try {
    await new Promise(r => setTimeout(r, 20)); flushSync();
    assert.ok(view.target.textContent!.includes(`bottown flow ${group.nodes[0].number} --implement codex --verify claude`));
    assert.ok(view.target.textContent!.includes(`bottown flow ${group.nodes[1].number} --implement ACTOR --verify ACTOR`));
    assert.ok(view.target.textContent!.includes(`bottown milestone run ${group.plan} --agents POOL --parallel N`));
    view.target.querySelector<HTMLElement>('.ms-node')!.click();
    assert.equal(location.hash, `#/flow/${group.nodes[0].number}`);
    assert.ok(![...view.target.querySelectorAll('button')].some(b => /^(Stop|Flow|Run)$/.test(b.textContent!.trim())));
  } finally { view.unmount(); location.hash = ''; }
});
