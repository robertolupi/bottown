<script lang="ts">
  // A flow's concise next owner action state (Ready to merge, Needs changes,
  // In progress, Needs intervention, Merged, Needs review, Unknown)
  // as an accessible badge with a glyph and a screen-reader prefix.
  import type { FlowStateInfo } from './types';

  interface Props {
    state: FlowStateInfo | undefined;
  }

  let { state }: Props = $props();
</script>

{#if state}
  <span
    class="badge badge-{state.tone} flow-state-badge flow-state-{state.state}"
    class:flow-live-badge={state.state === 'in_progress'}
    title={state.title}
  >
    <span class="sr-only">Next action: </span><span aria-hidden="true">{state.icon}</span>
    {state.label}
  </span>
{/if}
