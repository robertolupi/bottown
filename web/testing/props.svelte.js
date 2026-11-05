// reactiveProps returns props a test can change after mounting, as a parent
// component's would be: each field is a getter over a $state.raw source, so
// a component gets the values a test assigns as they are, not deeply
// proxied. (A deep $state would hand it proxies, which hide some bugs: an
// effect that copies a prop into its own $state loops only on raw values.)
// Assign to a field, then call flushSync.
/** @template {Record<string, any>} P @param {P} initial @returns {P} */
export function reactiveProps(initial) {
  const props = /** @type {P} */ ({});
  for (const key of Object.keys(initial)) {
    let value = $state.raw(initial[key]);
    Object.defineProperty(props, key, {
      enumerable: true,
      get: () => value,
      set: (v) => {
        value = v;
      },
    });
  }
  return props;
}
