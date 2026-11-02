<script lang="ts">
  // The operations the server's policy refused a run, if any.
  import type { Denial } from './types';
  import { formatTime } from './format';

  let { denials }: { denials: Denial[] | undefined } = $props();
</script>

{#if denials && denials.length > 0}
  <div class="denials-box">
    <div class="denials-header">Denied by policy ({denials.length})</div>
    {#each denials as d}
      <div class="denial-line">
        <span class="muted">{formatTime(d.time)}</span>
        <span>{d.method ? `${d.method} ${d.path}` : d.op}</span>
        {#if d.rule}<span class="badge badge-error">{d.rule}</span>{/if}
        {#if d.detail}<span class="muted">{d.detail}</span>{/if}
      </div>
    {/each}
  </div>
{/if}

<style>
  .denials-box {
    border-top: 1px solid var(--error-bg);
    padding: 8px 12px;
    font-size: 12px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .denials-header {
    color: var(--error);
    font-weight: 600;
  }

  .denial-line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    font-family: monospace;
    word-break: break-all;
  }
</style>
