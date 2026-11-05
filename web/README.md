# web/: the local web UI

A Svelte 5 app, built by Vite into `internal/webui/dist/ui` (`make web`) and
served by `bottown web ui`; its design is
[docs/design/web-ui.md](../docs/design/web-ui.md).

## Tests

`npm test` runs `test/*.test.ts` with Node's test runner, so `make test` and
`make check` do too. Node strips the TypeScript types; there is no build
step. `npm test` preloads `testing/svelte-hooks.js`, which lets a test import
`.svelte` components and `.svelte.ts` modules from `src/` as Vite would:
it compiles them for the client in dev mode, resolves `src/`'s
extensionless imports, and resolves `svelte` to the client runtime. To run
one file: `node --import ./testing/svelte-hooks.js --test test/NAME.test.ts`.

Logic that needs no DOM goes in plain modules (`state.ts`, `format.ts`, …)
with plain tests. Behaviour that lives in a component (effects, bindings,
events) gets a component test.

### Writing a component test

A component test mounts the component in a [jsdom](https://github.com/jsdom/jsdom)
document and drives it as the browser would:

```ts
import test from 'node:test';
import assert from 'node:assert/strict';
import { flushSync, render } from '../testing/dom.ts'; // first: installs the DOM
import { reactiveProps } from '../testing/props.svelte.js';
import LiveLogTail from '../src/LiveLogTail.svelte';

test('LiveLogTail renders appended lines', () => {
  const props = reactiveProps({ runId: 'r-1', lines: [{ kind: 'text', text: 'a' }] });
  const view = render(LiveLogTail, props);
  props.lines = [...props.lines, { kind: 'text', text: 'b' }];
  flushSync();
  assert.equal(view.target.querySelectorAll('.log-line').length, 2);
  view.unmount();
});
```

- **Import `testing/dom.ts` before any component.** It sets up `document`,
  `window` and the DOM classes as globals.
- **`render(Component, props)`** mounts into a new element in the document
  and flushes the effects. An effect that re-runs itself throws
  `effect_update_depth_exceeded` from `render` or the next `flushSync`, and
  fails the test.
- **`reactiveProps(initial)`** returns props a test can assign to, as a
  parent's would change. It passes values through as they are: a deep
  `$state` would hand the component proxies, and some effect loops show
  only on plain values.
- **Call `flushSync()`** after changing props or dispatching an event
  (`el.click()`, `el.dispatchEvent(new Event('scroll'))`), before
  asserting.
- **jsdom does no layout.** Sizes, offsets and scroll positions are 0, and
  setting `scrollTop` does nothing. A test that depends on them fakes them
  on `HTMLElement.prototype`, as `test/livelogtail.test.ts` does.
- **Check that the test fails without the fix.** A test written for a bug
  should fail with the bug put back.
