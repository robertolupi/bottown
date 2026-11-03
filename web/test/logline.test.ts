// LogLineView.svelte, compiled for the server as Vite would compile it for
// the browser, rendered for a line of each kind and tool class.
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { compile } from 'svelte/compiler';
import { render } from 'svelte/server';
import { splitToolLine } from '../src/state.ts';
import type { LogLine } from '../src/types.ts';

async function loadLogLineView(): Promise<any> {
  const src = fileURLToPath(new URL('../src/LogLineView.svelte', import.meta.url));
  const { js } = compile(readFileSync(src, 'utf8'), { filename: src, generate: 'server' });
  const state = pathToFileURL(fileURLToPath(new URL('../src/state.ts', import.meta.url))).href;
  const code = js.code.replace(/from ['"]\.\/state['"]/g, `from '${state}'`);
  // Inside node_modules so that the compiled module resolves svelte.
  const dir = new URL('../node_modules/.cache/bottown-test/', import.meta.url);
  mkdirSync(dir, { recursive: true });
  const out = new URL(`logline-${process.pid}.js`, dir);
  writeFileSync(out, code);
  return (await import(pathToFileURL(fileURLToPath(out)).href)).default;
}

// The rendered line's element: its classes, data-tool-class and inner HTML.
function renderLine(View: any, line: LogLine): { classes: string[]; toolClass?: string; inner: string; summary?: string } {
  const { body } = render(View, { props: { line } });
  const clean = body.replace(/<!--.*?-->/g, '').replace(/ svelte-[a-z0-9]+/g, '');
  const m = clean.match(/<(div|details) class="([^"]*)"(?: data-tool-class="([^"]*)")?[^>]*>(.*)<\/\1>/s);
  assert.ok(m, `no line element in ${body}`);
  const summary = m[4].match(/<summary[^>]*>(.*)<\/summary>/s)?.[1];
  return {
    classes: m[2].split(/\s+/).filter((c) => c.startsWith('log-')),
    toolClass: m[3],
    inner: m[4],
    summary,
  };
}

test('LogLineView renders each kind with its class', async () => {
  const View = await loadLogLineView();
  for (const kind of ['plain', 'text', 'output', 'error', 'status'] as const) {
    const r = renderLine(View, { kind, text: `a ${kind} line` });
    assert.deepEqual(r.classes, ['log-line', `log-${kind}`]);
    assert.equal(r.toolClass, undefined);
    assert.equal(r.inner, `a ${kind} line`);
  }
});

test('LogLineView renders a readable tool summary with its class', async () => {
  const View = await loadLogLineView();
  const calls: Array<[string, string, LogLine['tool_class']]> = [
    ['Bash', 'Bash: make check', 'shell'],
    ['Read', 'Read: AGENTS.md', 'read'],
    ['Edit', 'Edit: web/src/state.ts', 'edit'],
    ['Grep', 'Grep: LogLine', 'search'],
    ['Bash', 'Bash: bottown issue show 12', 'forge'],
    ['mcp__chrome-devtools__take_screenshot', 'mcp__chrome-devtools__take_screenshot', 'browser'],
    ['Task', 'Task: <explore>', 'other'],
  ];
  for (const [tool, text, toolClass] of calls) {
    const r = renderLine(View, { kind: 'tool', tool, tool_class: toolClass, text });
    assert.deepEqual(r.classes, ['log-line', 'log-tool']);
    assert.equal(r.toolClass, toolClass);
    const arg = text.slice(tool.length).replace(/^:\s*/, '').replace(/&/g, '&amp;').replace(/</g, '&lt;');
    assert.equal(r.summary, `<span class="log-tool">${tool}</span>${arg ? `<span class="log-arg">${arg}</span>` : ''}`);
  }
});

test('LogLineView expands tool parameters and output without cluttering the summary', async () => {
  const View = await loadLogLineView();
  const r = renderLine(View, {
    kind: 'tool',
    tool: 'Read',
    tool_class: 'read',
    state: 'completed',
    text: 'Read: web/test/state.test.ts',
    parameters: '{\n  "path": "web/test/state.test.ts"\n}',
    output: 'first line\nsecond line',
  });
  assert.match(r.summary || '', /^<span class="log-tool">Read<\/span><span class="log-arg">web\/test\/state\.test\.ts<\/span><span class="log-state log-state-completed">· completed<\/span>$/);
  assert.doesNotMatch(r.summary || '', /first line|"path"/);
  assert.match(r.inner, /<h4[^>]*>Parameters<\/h4>/);
  assert.match(r.inner, /<h4[^>]*>Output<\/h4>/);
  assert.match(r.inner, /first line\nsecond line/);
});

test('LogLineView marks failed calls and expands full assistant text', async () => {
  const View = await loadLogLineView();
  const failed = renderLine(View, { kind: 'tool', tool: 'Bash', state: 'failed', text: 'Bash: false', output: 'exit 1' });
  assert.match(failed.summary || '', /log-state-failed/);
  assert.match(failed.summary || '', /· failed/);

  const text = renderLine(View, { kind: 'text', text: 'First line.', detail: 'First line.\n\nFull explanation.' });
  assert.equal(text.summary, 'First line.');
  assert.match(text.inner, /Full explanation\./);
});

test('splitToolLine splits only tool lines that start with their tool', () => {
  assert.deepEqual(splitToolLine({ kind: 'tool', tool: 'Bash', text: 'Bash: ls' }), { tool: 'Bash', arg: 'ls' });
  assert.equal(splitToolLine({ kind: 'tool', tool: 'Bash', text: 'ls' }), null);
  assert.equal(splitToolLine({ kind: 'text', text: 'Bash: ls' }), null);
});
