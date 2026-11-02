<script lang="ts">
  // A discuss thread in the run history: its one run's row and, when
  // expanded, that run's log.
  import type { HistoryThread, RunLogState } from './types';
  import { formatThreadMainLine } from './state';
  import { formatTime, formatUsage } from './format';
  import IssueReference from './IssueReference.svelte';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import RunLogRow from './RunLogRow.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    thread: HistoryThread;
    logState: RunLogState | undefined;
    onToggleLog: (runId: string) => void;
  }

  let { thread, logState, onToggleLog }: Props = $props();

  let mainLine = $derived(formatThreadMainLine(thread));
  let h = $derived(thread.runs[0]);
</script>

<tr class="discuss-thread-row" class:flow-row-active={thread.active}>
  <td>
    <div class="thread-main-line">
      {#if mainLine.issueLabel}
        <IssueReference
          number={thread.issue || 0}
          href={mainLine.issueUrl || h.target_url}
          label={mainLine.issueLabel}
          title={mainLine.issueTitle}
        />
      {:else}
        <TargetLink href={h.target_url} label={h.target} />
      {/if}
      {#if mainLine.milestone}
        <span class="badge milestone-badge" title="Milestone">{mainLine.milestone}</span>
      {/if}
    </div>
  </td>
  <td><span class="badge badge-neutral">discuss</span></td>
  <td><span class="badge repo-badge">{h.actor} ({h.family})</span></td>
  <td>
    {#if thread.active}
      <span class="badge badge-accent flow-live-badge">active</span>
    {/if}
    <span class="badge badge-{h.status === 'succeeded' ? 'success' : h.status === 'failed' ? 'error' : 'warning'}">
      {h.status}
    </span>
    {#if h.denials && h.denials.length > 0}
      <span class="badge badge-error" title="Operations the server's policy refused this run; expand the log to see them">
        {h.denials.length} denied
      </span>
    {/if}
  </td>
  <td><OutcomeBadge run={h} /></td>
  <td class="muted">{h.duration || '—'}</td>
  <td class="muted text-sm">{formatUsage(h.usage)}</td>
  <td class="summary-cell" title={h.summary}>{h.summary || '—'}</td>
  <td class="muted text-sm">{formatTime(thread.last_activity)}</td>
  <td>
    <div class="row-actions">
      <button
        class="btn btn-sm btn-secondary"
        onclick={() => onToggleLog(h.id)}
        title="Expand parsed run log"
      >
        {logState ? 'Hide Log' : 'Log'}
      </button>
    </div>
  </td>
</tr>
{#if logState}
  <RunLogRow title={`Run Log (${h.id})`} colspan={10} denials={h.denials} {logState} />
{/if}

<style>
  .flow-row-active {
    background: rgba(37, 99, 235, 0.09);
  }

  .flow-row-active td:first-child {
    box-shadow: inset 3px 0 0 var(--accent);
  }

  .thread-main-line {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .milestone-badge {
    background: rgba(16, 185, 129, 0.15);
    color: var(--success);
    border: 1px solid rgba(16, 185, 129, 0.3);
  }

  .flow-live-badge {
    font-weight: 600;
  }

  .summary-cell {
    max-width: 280px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .row-actions {
    display: flex;
    gap: 6px;
    align-items: center;
  }
</style>
