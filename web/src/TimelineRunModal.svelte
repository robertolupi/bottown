<script lang="ts">
  // A run picked on the Timeline: its details and its log, in a dialog.
  import { onMount } from 'svelte';
  import type { LogLine, TimelineRun } from './types';
  import { formatTime } from './format';
  import { formatSpan, runTarget } from './timeline';
  import LogLineView from './LogLineView.svelte';
  import Modal from './Modal.svelte';
  import OutcomeBadge from './OutcomeBadge.svelte';

  interface Props {
    run: TimelineRun;
    now: number;
    onclose: () => void;
  }

  let { run, now, onclose }: Props = $props();

  let lines = $state<LogLine[] | null>(null);
  let logError = $state<string | null>(null);

  let duration = $derived(((run.ended ? Date.parse(run.ended) : now) - Date.parse(run.created)) / 1000);

  onMount(async () => {
    try {
      const res = await fetch(`/api/runs/${encodeURIComponent(run.id)}/log`);
      if (!res.ok) throw new Error('Failed to load log');
      lines = (await res.json()).lines || [];
    } catch (e: any) {
      logError = e.message;
    }
  });
</script>

<Modal wide onbackdrop={onclose}>
  <div class="modal-header">
    <h3>Run {run.id}</h3>
    <button class="close-btn" onclick={onclose} aria-label="Close">✕</button>
  </div>

  <dl class="tl-run-details">
    <dt>Kind</dt><dd>{run.kind}{run.interactive ? ' (interactive)' : ''}</dd>
    <dt>Actor</dt><dd>{run.actor}{run.family ? ` (${run.family})` : ''}</dd>
    {#if runTarget(run)}<dt>Target</dt><dd>{runTarget(run)}</dd>{/if}
    <dt>Started</dt><dd>{formatTime(run.created)}</dd>
    <dt>Ended</dt><dd>{run.ended ? formatTime(run.ended) : 'still running'}</dd>
    <dt>Duration</dt><dd>{formatSpan(duration)}</dd>
    <dt>Status</dt><dd>{run.status || run.state}{run.purged ? ' (purged by run gc)' : ''}</dd>
    <dt>Outcome</dt><dd><OutcomeBadge {run} /></dd>
  </dl>

  <div class="log-tail-box">
    <div class="log-tail-header">
      <span>Log</span>
      {#if lines}<span class="muted text-sm">{lines.length} lines</span>{/if}
      <a class="log-raw-link" href="/api/runs/{run.id}/log?raw=1" target="_blank" rel="noreferrer">Raw log</a>
    </div>
    <div class="log-tail-content">
      {#if logError}
        <div class="log-line error">{logError}</div>
      {:else if lines === null}
        <div class="log-line muted">Loading log...</div>
      {:else if lines.length === 0}
        <div class="log-line muted">No log lines recorded{run.purged ? '; run gc may have removed the log' : ''}.</div>
      {:else}
        {#each lines as line}
          <LogLineView {line} />
        {/each}
      {/if}
    </div>
  </div>

  <div class="modal-actions">
    <button class="btn btn-secondary" onclick={onclose}>Close</button>
  </div>
</Modal>

<style>
  .tl-run-details {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 12px;
    margin: 12px 0;
    font-size: 13px;
  }

  .tl-run-details dt {
    color: var(--text-muted);
  }

  .tl-run-details dd {
    margin: 0;
  }

  .log-tail-content {
    max-height: 360px;
    overflow-y: auto;
  }
</style>
