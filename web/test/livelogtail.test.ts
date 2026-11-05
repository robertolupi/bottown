// LiveLogTail.svelte, mounted in jsdom: it renders the lines, follows new
// ones, pauses when scrolled away and resumes on "jump to latest".
import test from 'node:test';
import assert from 'node:assert/strict';
import { flushSync, render } from '../testing/dom.ts';
import { reactiveProps } from '../testing/props.svelte.js';
import LiveLogTail from '../src/LiveLogTail.svelte';
import type { LogLine } from '../src/types.ts';

// jsdom does no layout, so the log box gets a fake one: each line is 20px
// high, the box shows 100px, and scrollTop is clamped like a browser's.
const LINE = 20;
const VIEW = 100;
const scrollTops = new WeakMap<Element, number>();
function isLogBox(el: Element): boolean {
  return el.classList.contains('log-tail-content');
}
function contentHeight(el: Element): number {
  return el.querySelectorAll('.log-line').length * LINE;
}
const proto = HTMLElement.prototype;
const base = {
  scrollTop: Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop')!,
  scrollHeight: Object.getOwnPropertyDescriptor(Element.prototype, 'scrollHeight')!,
  clientHeight: Object.getOwnPropertyDescriptor(Element.prototype, 'clientHeight')!,
};
Object.defineProperty(proto, 'scrollHeight', {
  configurable: true,
  get(this: HTMLElement) {
    return isLogBox(this) ? contentHeight(this) : base.scrollHeight.get!.call(this);
  },
});
Object.defineProperty(proto, 'clientHeight', {
  configurable: true,
  get(this: HTMLElement) {
    return isLogBox(this) ? Math.min(VIEW, contentHeight(this)) : base.clientHeight.get!.call(this);
  },
});
Object.defineProperty(proto, 'scrollTop', {
  configurable: true,
  get(this: HTMLElement) {
    return isLogBox(this) ? (scrollTops.get(this) ?? 0) : base.scrollTop.get!.call(this);
  },
  set(this: HTMLElement, v: number) {
    if (!isLogBox(this)) return base.scrollTop.set!.call(this, v);
    const max = Math.max(0, this.scrollHeight - this.clientHeight);
    scrollTops.set(this, Math.min(Math.max(0, v), max));
  },
});

Object.defineProperty(proto, 'offsetTop', {
  configurable: true,
  get(this: HTMLElement) {
    const parent = this.parentElement;
    if (!parent || !isLogBox(parent) || !this.classList.contains('log-line')) return 0;
    return [...parent.querySelectorAll('.log-line')].indexOf(this) * LINE;
  },
});

function logLines(from: number, to: number): LogLine[] {
  const out: LogLine[] = [];
  for (let i = from; i < to; i++) out.push({ kind: 'text', text: `line ${i}` });
  return out;
}

function mountTail(lines: LogLine[]) {
  const props = reactiveProps({ runId: 'r-test', lines });
  const view = render(LiveLogTail, props);
  const box = view.target.querySelector('.log-tail-content') as HTMLElement;
  const follow = view.target.querySelector('[data-action="follow"]') as HTMLButtonElement;
  return { props, view, box, follow };
}

function bottom(box: HTMLElement): number {
  return box.scrollHeight - box.clientHeight;
}

// userScroll moves the box as a person would, firing the scroll event.
function userScroll(box: HTMLElement, top: number) {
  box.scrollTop = top;
  box.dispatchEvent(new Event('scroll'));
  flushSync();
}

test('LiveLogTail mounts and renders its lines without an effect loop', () => {
  // The regression for !149 at 4a2ca34: previousLines as $state made the
  // $effect.pre that writes it re-run itself until Svelte threw
  // effect_update_depth_exceeded, from mount or the first update.
  const { props, view, box, follow } = mountTail(logLines(0, 3));
  assert.deepEqual(
    [...box.querySelectorAll('.log-line')].map((el) => el.textContent),
    ['line 0', 'line 1', 'line 2'],
  );
  assert.equal(follow.dataset.action, 'follow');
  assert.match(follow.textContent ?? '', /Following/);
  props.lines = logLines(0, 4);
  flushSync();
  assert.equal(box.querySelectorAll('.log-line').length, 4);
  view.unmount();
});

test('LiveLogTail shows a placeholder with no lines', () => {
  const { view, box } = mountTail([]);
  assert.match(box.textContent ?? '', /Waiting for agent activity/);
  view.unmount();
});

test('LiveLogTail follows appended lines to the bottom', () => {
  const { props, view, box } = mountTail(logLines(0, 10));
  assert.equal(box.scrollTop, bottom(box));
  props.lines = logLines(0, 20);
  flushSync();
  assert.equal(box.querySelectorAll('.log-line').length, 20);
  assert.equal(box.scrollTop, bottom(box));
  view.unmount();
});

test('LiveLogTail pauses when scrolled away and resumes on jump to latest', () => {
  const { props, view, box, follow } = mountTail(logLines(0, 20));
  // The programmatic scroll to the bottom fires a scroll event, which must
  // not count as the reader scrolling.
  box.dispatchEvent(new Event('scroll'));
  flushSync();
  assert.match(follow.textContent ?? '', /Following/);

  userScroll(box, 40);
  assert.match(follow.textContent ?? '', /Follow output/);
  assert.ok(follow.classList.contains('paused'));

  // Paused: new lines do not move the reader.
  props.lines = logLines(0, 30);
  flushSync();
  assert.equal(box.scrollTop, 40);

  follow.click();
  flushSync();
  assert.match(follow.textContent ?? '', /Following/);
  assert.equal(box.scrollTop, bottom(box));

  props.lines = logLines(0, 35);
  flushSync();
  assert.equal(box.scrollTop, bottom(box));
  view.unmount();
});

test('LiveLogTail keeps a paused reader on the same line when old lines are trimmed', () => {
  const { props, view, box, follow } = mountTail(logLines(0, 20));
  userScroll(box, 200); // line 10 at the top
  assert.ok(follow.classList.contains('paused'));
  // The server keeps a bounded tail: the first 5 lines drop off.
  props.lines = logLines(5, 25);
  flushSync();
  assert.equal(box.scrollTop, 100); // line 10 still at the top
  view.unmount();
});
