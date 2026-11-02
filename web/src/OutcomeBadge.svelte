<script lang="ts">
  // A run's recorded outcome (approved, changes requested, …) as an
  // outlined badge with a glyph, or "—" when it has none.
  import type { HistoryRunInfo } from './types';
  import { runOutcome } from './state';

  let { run }: { run: HistoryRunInfo | undefined } = $props();

  let o = $derived(run ? runOutcome(run) : null);
</script>

{#if o}
  <span class="badge badge-{o.tone} outcome-badge outcome-{run?.outcome || 'none'}" title={o.title}>
    <span class="sr-only">Outcome: </span><span aria-hidden="true">{o.icon}</span>
    {o.label}
  </span>
{:else}
  <span class="muted" title="No outcome yet">—</span>
{/if}
