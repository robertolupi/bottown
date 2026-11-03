<script lang="ts">
  // A table row, under an ended run's row, with the run's policy denials
  // and its parsed log, as loaded by the history table.
  import type { Denial, RunLogState } from './types';
  import DenialList from './DenialList.svelte';
  import LogLineView from './LogLineView.svelte';

  interface Props {
    title: string;
    runId: string;
    colspan: number;
    denials: Denial[] | undefined;
    logState: RunLogState;
  }

  let { title, runId, colspan, denials, logState }: Props = $props();
</script>

<tr class="log-expand-row">
  <td {colspan}>
    <DenialList {denials} />
    <div class="log-tail-box history-log-box">
      <div class="log-tail-header">
        <span>{title}</span>
        {#if logState.lines}
          <span class="muted text-sm">{logState.lines.length} lines</span>
        {/if}
        <a
          class="log-raw-link"
          href="/api/runs/{runId}/log?raw=1"
          target="_blank"
          rel="noreferrer"
          title="Open the complete raw log with encoded payloads elided"
        >Raw log</a>
      </div>
      <div class="log-tail-content">
        {#if logState.loading}
          <div class="log-line muted">Loading log...</div>
        {:else if logState.error}
          <div class="log-line error">{logState.error}</div>
        {:else if !logState.lines || logState.lines.length === 0}
          <div class="log-line muted">No log lines recorded.</div>
        {:else}
          {#each logState.lines as line}
            <LogLineView {line} />
          {/each}
        {/if}
      </div>
    </div>
  </td>
</tr>

<style>
  /* Its padding is the history table's cells' (RunHistory), or none in an
     expanded flow (FlowThreadRow). */
  .log-expand-row td {
    background: rgba(125, 125, 125, 0.03);
  }

  .history-log-box {
    margin-top: 6px;
    max-height: 280px;
  }

  .log-tail-box {
    background: #090d16;
    color: #e2e8f0;
    font-family: monospace;
    font-size: 12px;
  }

  .log-tail-header {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 12px;
    background: #111827;
    color: #94a3b8;
    font-size: 11px;
    border-bottom: 1px solid #1e293b;
  }

  .log-tail-header > :first-child { flex: 1; }
  .log-raw-link { color: #cbd5e1; white-space: nowrap; }

  .log-tail-content {
    max-height: 180px;
    overflow-y: auto;
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .log-line {
    white-space: pre-wrap;
    word-break: break-all;
  }
</style>
