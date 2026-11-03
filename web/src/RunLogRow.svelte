<script lang="ts">
  // A table row, under an ended run's row, with a failed run's failure
  // reason, the run's policy denials and its parsed log, as loaded by the
  // history table.
  import type { Denial, FailureDescription, RunLogState } from './types';
  import DenialList from './DenialList.svelte';
  import LogLineView from './LogLineView.svelte';

  interface Props {
    title: string;
    runId: string;
    colspan: number;
    denials: Denial[] | undefined;
    failure?: FailureDescription | null;
    logState: RunLogState;
  }

  let { title, runId, colspan, denials, failure, logState }: Props = $props();
</script>

<tr class="log-expand-row">
  <td {colspan}>
    {#if failure}
      <dl class="run-failure">
        {#if failure.observed}
          <dt>Observed</dt>
          <dd class="run-failure-observed">{failure.observed}{#if failure.observedDetail}: {failure.observedDetail}{/if}</dd>
        {/if}
        {#if failure.reported}
          <dt>Agent reported</dt>
          <dd>{failure.reported}</dd>
        {/if}
        {#if failure.unavailable}
          <dt>Reason</dt>
          <dd class="muted">Reason unavailable: bottown recorded no cause and the agent no summary.</dd>
        {/if}
      </dl>
    {/if}
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
          <div class="log-line muted">
            {failure && !failure.hasLog ? 'No log: the run failed before its harness started.' : 'No log lines recorded.'}
          </div>
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

  .run-failure {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 12px;
    margin: 0 0 6px;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-left: 3px solid var(--error);
    border-radius: 6px;
    background: var(--card-bg);
    font-size: 13px;
  }

  .run-failure dt {
    color: var(--text-muted);
    font-weight: 600;
  }

  .run-failure dd {
    margin: 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
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
