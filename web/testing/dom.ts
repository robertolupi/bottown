// A jsdom document for component tests, installed as the globals Svelte's
// client runtime and the components use. Import it before any component:
//
//   import { render } from '../testing/dom.ts';
//
// jsdom does no layout: sizes and scroll offsets are 0 unless a test fakes
// them (see test/livelogtail.test.ts).
import { JSDOM } from 'jsdom';
import { flushSync, mount, unmount } from 'svelte';
import type { Component } from 'svelte';

const dom = new JSDOM('<!doctype html><html><body></body></html>', {
  url: 'http://localhost/',
  pretendToBeVisual: true,
});
const win = dom.window as unknown as Record<string, unknown>;

// Every window property Node does not already define (document, Element,
// HTMLElement, Event, getComputedStyle, requestAnimationFrame, …).
for (const key of Object.getOwnPropertyNames(win)) {
  if (key in globalThis) continue;
  Object.defineProperty(globalThis, key, {
    configurable: true,
    get: () => win[key],
  });
}
Object.defineProperty(globalThis, 'window', { configurable: true, value: dom.window });
// Node has its own Event and EventTarget; the DOM's must be jsdom's.
for (const key of ['Event', 'EventTarget', 'CustomEvent', 'navigator']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: win[key] });
}

export interface Rendered {
  target: HTMLElement;
  unmount(): void;
}

// render mounts a component in a fresh element in the document and flushes
// its effects, so that an effect loop (effect_update_depth_exceeded) throws
// here. Pass props from reactiveProps (testing/props.svelte.js) to change
// them later; call flushSync after a change or an event.
export function render<P extends Record<string, any>>(component: Component<P>, props: P): Rendered {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const instance = mount(component, { target, props });
  flushSync();
  return {
    target,
    unmount() {
      unmount(instance);
      target.remove();
    },
  };
}

export { flushSync };
