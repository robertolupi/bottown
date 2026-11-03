<script lang="ts">
  // A flow thread in the run history: one row for the flow (issue, PR,
  // milestone, actors, status) that expands, one flow at a time, to the
  // flow's combined timeline and its constituent runs.
  import type { HistoryThread, IssueTimeline, RunLogState } from './types';
  import { buildFlowConstituentRuns, buildFlowTimeline, formatThreadMainLine } from './state';
  import { formatTime, formatUsage } from './format';
  import ConstituentRuns from './ConstituentRuns.svelte';
  import FlowTimeline from './FlowTimeline.svelte';
  import IssueReference from './IssueReference.svelte';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    thread: HistoryThread;
    expanded: boolean;
    timeline: IssueTimeline | undefined;
    timelineLoading: boolean | undefined;
    timelineError: string | undefined;
    expandedLogs: Record<string, RunLogState>;
    onSelect: (thread: HistoryThread) => void;
    onToggleLog: (runId: string) => void;
  }

  let { thread, expanded, timeline, timelineLoading, timelineError, expandedLogs, onSelect, onToggleLog }: Props =
    $props();

  let mainLine = $derived(formatThreadMainLine(thread));
  let lastRun = $derived(thread.runs[thread.runs.length - 1]);
</script>

<tr
  class="flow-thread-row"
  class:flow-row-active={thread.active}
  class:flow-row-expanded={expanded}
  onclick={() => onSelect(thread)}
>
  <td>
    <div class="thread-main-line">
      <span class="thread-chevron" aria-hidden="true">{expanded ? '▾' : '▸'}</span>
      {#if mainLine.issueLabel}
        <IssueReference
          number={thread.issue || 0}
          href={mainLine.issueUrl || lastRun.target_url}
          label={mainLine.issueLabel}
          title={mainLine.issueTitle}
        />
      {/if}
      {#if mainLine.prLabel}
        <TargetLink href={mainLine.prUrl} label={mainLine.prLabel} stopPropagation />
      {/if}
      {#if !mainLine.issueLabel && !mainLine.prLabel && thread.runs.length > 0}
        <TargetLink href={lastRun.target_url} label={lastRun.target} stopPropagation />
      {/if}
      {#if mainLine.milestone}
        <span class="badge milestone-badge" title="Milestone">{mainLine.milestone}</span>
      {/if}
    </div>
  </td>
  <td>
    <span class="badge badge-neutral">
      flow · {thread.run_count} {thread.run_count === 1 ? 'run' : 'runs'}
    </span>
  </td>
  <td>
    <div class="thread-actors">
      {#each thread.actors as act}
        <span class="badge repo-badge">{act}</span>
      {/each}
    </div>
  </td>
  <td>
    <div class="thread-status-badges">
      {#if thread.active}
        <span class="badge badge-accent flow-live-badge">
          {thread.active_label ? `active · ${thread.active_label}` : 'active'}
        </span>
      {/if}
      <StatusBadge status={thread.status} />
      {#if thread.total_denials > 0}
        <span class="badge badge-error" title="Operations refused by server policy in this flow">
          {thread.total_denials} denied
        </span>
      {/if}
    </div>
  </td>
  <td><OutcomeBadge run={lastRun} /></td>
  <td class="muted">{thread.duration || '—'}</td>
  <td class="muted text-sm">{formatUsage(thread.usage)}</td>
  <td class="summary-cell" title={thread.summary}>{thread.summary || '—'}</td>
  <td class="muted text-sm">{formatTime(thread.last_activity)}</td>
  <td>
    <div class="row-actions">
      <button
        class="btn btn-sm btn-secondary"
        aria-expanded={expanded}
        onclick={(e) => {
          e.stopPropagation();
          onSelect(thread);
        }}
      >
        {expanded ? 'Collapse' : `Runs (${thread.run_count})`}
      </button>
    </div>
  </td>
</tr>

{#if expanded}
  {@const entries = buildFlowTimeline(thread, timeline)}
  {@const constituentRuns = buildFlowConstituentRuns(thread, entries)}
  <tr class="flow-expand-row">
    <td colspan="10">
      <div class="flow-expanded-panel">
        <!-- 1. Combined Flow Timeline at the top of the expanded flow -->
        <FlowTimeline
          {thread}
          {mainLine}
          {entries}
          loading={timelineLoading}
          error={timelineError}
          {expandedLogs}
          {onToggleLog}
        />

        <!-- 2. Constituent Runs in this Flow -->
        <ConstituentRuns runs={constituentRuns} {expandedLogs} {onToggleLog} />
      </div>
    </td>
  </tr>
{/if}

<style>
  .flow-thread-row {
    cursor: pointer;
    transition: background-color 0.12s ease;
  }

  .flow-thread-row:hover {
    background: rgba(125, 125, 125, 0.06);
  }

  .flow-row-expanded {
    background: rgba(37, 99, 235, 0.06);
  }

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

  .thread-chevron {
    display: inline-block;
    width: 14px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .milestone-badge {
    background: rgba(16, 185, 129, 0.15);
    color: var(--success);
    border: 1px solid rgba(16, 185, 129, 0.3);
  }

  .thread-actors, .thread-status-badges {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .flow-live-badge {
    font-weight: 600;
  }

  .summary-cell {
    max-width: max(280px, 25vw);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .row-actions {
    display: flex;
    gap: 6px;
    align-items: center;
  }

  /* Every cell in the expanded panel, the constituent runs' and their log
     rows' included; tr outranks the history table's cell padding
     (RunHistory). */
  tr.flow-expand-row :global(td) {
    padding: 0;
    background: rgba(125, 125, 125, 0.03);
  }

  .flow-expanded-panel {
    padding: 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 20px;
    border-left: 3px solid var(--accent);
  }
</style>
