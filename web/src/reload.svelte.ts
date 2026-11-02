import { untrack } from 'svelte';

// reloadOn runs load now and again each time the value key reads changes.
// load runs untracked: the state it reads, such as its own in-flight flag,
// must not re-run the effect, or every completed load would start another.
// Call it during component initialisation, like $effect.
export function reloadOn(key: () => unknown, load: () => unknown) {
  $effect(() => {
    key();
    untrack(() => {
      void load();
    });
  });
}
