// Starting and stopping a milestone run from the Milestones view: the
// group's start action and running state (Milestones.svelte, on the
// default fixture's milestones with fetch stubbed), the start dialog
// (MilestoneRunModal.svelte) and the stop dialog's wording.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { render, flushSync } from '../testing/dom.ts';
import { reactiveProps } from '../testing/props.svelte.js';
import Milestones from '../src/Milestones.svelte';
import MilestoneRunModal from '../src/MilestoneRunModal.svelte';
import StopModal from '../src/StopModal.svelte';
import { buildMilestoneRunPayload } from '../src/actions.ts';
import { canStartMilestoneRun, schedulerFlows } from '../src/milestones.ts';
import type { MilestoneGroup } from '../src/types.ts';

const fixture = JSON.parse(readFileSync(new URL('../fixtures/default.json', import.meta.url), 'utf8'));
const running: MilestoneGroup = fixture.milestones[0];
const idle: MilestoneGroup = { ...running, id: 'epic-60', epic: { ...running.epic!, number: 60 }, plan: 'v0-4', scheduler: undefined };
const loose: MilestoneGroup = { ...running, id: 'milestone-9', epic: undefined, plan: undefined, scheduler: undefined };

async function mountMilestones(groups: MilestoneGroup[]) {
  globalThis.fetch = (async () => new Response(JSON.stringify(groups), { status: 200 })) as typeof fetch;
  const calls = { run: [] as string[], stop: [] as string[] };
  const props = reactiveProps({
    refreshKey: 0,
    onFlow: () => {},
    onRun: () => {},
    onMilestoneRun: (slug: string) => calls.run.push(slug),
    onMilestoneStop: (id: string) => calls.stop.push(id),
  });
  const view = render(Milestones, props);
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, 5));
    flushSync();
  }
  return { view, calls };
}

const card = (root: HTMLElement, id: string) => root.querySelector(`[data-group="${id}"]`) as HTMLElement;

test('the fixture has a running milestone run with a child flow', () => {
  assert.ok(running.scheduler, 'default.json shows the running state');
  assert.ok(!canStartMilestoneRun(running), 'no start while it runs');
  assert.ok(canStartMilestoneRun(idle));
  assert.ok(!canStartMilestoneRun(loose), 'a milestone without an epic has no plan to run');
  assert.ok(!canStartMilestoneRun({ ...idle, epic: { ...idle.epic!, state: 'closed' } }), 'a closed epic');
  assert.deepEqual(schedulerFlows(running).map((n) => n.number), [51]);
});

test('a running milestone run shows its pool, parallelism and child flows, and stops by its id', async () => {
  const { view, calls } = await mountMilestones([running, idle, loose]);
  try {
    const g = card(view.target, running.id);
    const panel = g.querySelector('[data-scheduler="milestone-v0-3"]') as HTMLElement;
    assert.ok(panel, 'the running panel');
    assert.match(g.textContent!, /milestone run · running/);
    const text = panel.textContent!;
    assert.match(text, /running 31m 40s · parallel 2/);
    assert.deepEqual(
      [...panel.querySelectorAll('.ms-pool-entry')].map((e) => e.textContent),
      ['codex=*', 'kimi=1', 'agy:gemini-3.8-flash=1', 'claude=2'],
    );
    assert.match(text, /#51\s*codex → kimi/);
    assert.match(text, /\.bottown\/milestone-v0-3\.log/);
    assert.equal(g.querySelector('.ms-run-start'), null, 'no start button while it runs');

    (panel.querySelector('.ms-run-stop') as HTMLButtonElement).click();
    assert.deepEqual(calls.stop, ['milestone-v0-3']);
  } finally {
    view.unmount();
  }
});

test('an open epic without a milestone run offers to start one for its plan', async () => {
  const { view, calls } = await mountMilestones([running, idle, loose]);
  try {
    const g = card(view.target, idle.id);
    assert.equal(g.querySelector('[data-scheduler]'), null);
    (g.querySelector('.ms-run-start') as HTMLButtonElement).click();
    assert.deepEqual(calls.run, ['v0-4']);
    assert.equal(card(view.target, loose.id).querySelector('.ms-run-start'), null);
  } finally {
    view.unmount();
  }
});

test('the start dialog submits a pool and parallelism, and shows the parser\'s refusal', () => {
  let submits = 0;
  const props = reactiveProps({
    actors: [{ name: 'claude', family: 'anthropic', harness: 'claude' }, { name: 'agy', family: 'google', harness: 'agy' }],
    form: { slug: 'v0-4', agents: '', parallel: 2 as number | string },
    error: null as string | null,
    actionLoading: false,
    onsubmit: () => submits++,
    onclose: () => {},
  });
  const view = render(MilestoneRunModal, props);
  try {
    const text = () => view.target.textContent!;
    const submit = () => view.target.querySelector('button[type="submit"]') as HTMLButtonElement;
    const form = view.target.querySelector('form') as HTMLFormElement;
    assert.match(text(), /v0-4/);
    assert.match(text(), /claude \(anthropic\), agy \(google\)/);
    assert.ok(submit().disabled, 'no pool yet');
    form.dispatchEvent(new Event('submit', { cancelable: true }));
    assert.equal(submits, 0);

    props.form = { slug: 'v0-4', agents: 'claude=*', parallel: 0 };
    flushSync();
    assert.ok(submit().disabled, 'parallel below 1');

    props.form = { slug: 'v0-4', agents: 'claude=*', parallel: 2 };
    flushSync();
    assert.ok(!submit().disabled);
    assert.equal((view.target.querySelector('#ms-run-agents') as HTMLInputElement).value, 'claude=*');
    form.dispatchEvent(new Event('submit', { cancelable: true }));
    assert.equal(submits, 1);

    props.error = 'agents pool: family "anthropic" cannot be verified by another family';
    flushSync();
    assert.equal(view.target.querySelector('.ms-run-error')!.textContent, props.error);
  } finally {
    view.unmount();
  }
});

test('the milestone run payload trims the spec and numbers parallel', () => {
  assert.deepEqual(buildMilestoneRunPayload({ slug: 'v0-4', agents: ' claude=*,agy=1 ', parallel: '3' }), {
    slug: 'v0-4',
    agents: 'claude=*,agy=1',
    parallel: 3,
  });
});

test('the stop dialog names a milestone run', () => {
  const view = render(StopModal, { runId: 'milestone-v0-3', actionLoading: false, onconfirm: () => {}, onclose: () => {} });
  try {
    assert.match(view.target.textContent!, /Stop Milestone Run/);
    assert.match(view.target.textContent!, /milestone run for v0-3/);
  } finally {
    view.unmount();
  }
});
