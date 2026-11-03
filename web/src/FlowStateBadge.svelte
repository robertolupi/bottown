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

<style>
  .flow-state-badge {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    white-space: nowrap;
    font-weight: 600;
  }
</style>
