<script lang="ts">
  // An expanded flow's combined timeline: each run as a boundary around
  // its events, and the events outside any run.
  import type { FlowTimelineEntry, HistoryThread, RunLogState } from './types';
  import type { ThreadMainLine } from './state';
  import { runOutcome } from './state';
  import { formatTime } from './format';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import TimelineEventItem from './TimelineEventItem.svelte';

  interface Props {
    thread: HistoryThread;
    mainLine: ThreadMainLine;
    entries: FlowTimelineEntry[];
    loading: boolean | undefined;
    error: string | undefined;
    expandedLogs: Record<string, RunLogState>;
    onToggleLog: (runId: string) => void;
  }

  let { thread, mainLine, entries, loading, error, expandedLogs, onToggleLog }: Props = $props();
</script>

<div class="flow-timeline-section">
  <div class="flow-section-header">
    <div class="flow-section-title">
      <strong>Combined Flow Timeline</strong>
      {#if mainLine.issueLabel}
        <span class="badge badge-neutral">{mainLine.issueLabel}</span>
      {/if}
      {#if mainLine.prLabel}
        <span class="badge badge-neutral">{mainLine.prLabel}</span>
      {/if}
      {#if mainLine.milestone}
        <span class="badge milestone-badge">{mainLine.milestone}</span>
      {/if}
      {#if thread.active}
        <span class="badge badge-accent">Live updating</span>
      {/if}
    </div>
    {#if loading}
      <span class="muted text-sm">Refreshing timeline…</span>
    {:else if error}
      <span class="muted text-sm">{error}</span>
    {/if}
  </div>

  <div class="flow-timeline-list">
    {#each entries as entry}
      {#if entry.entry_type === 'run_boundary'}
        <div class="timeline-run-boundary timeline-run-{entry.status}">
          <div class="timeline-run-boundary-header">
            <div class="timeline-run-boundary-meta">
              <span class="badge badge-neutral">{entry.run.kind}</span>
              <span class="badge repo-badge">{entry.run.family ? `${entry.run.actor} (${entry.run.family})` : entry.run.actor}</span>
              <code class="text-sm muted">{entry.run.id}</code>
              <StatusBadge status={entry.status} />
              {#if runOutcome(entry.run)}
                <OutcomeBadge run={entry.run} />
              {/if}
              {#if entry.duration && entry.duration !== '-'}
                <span class="muted text-sm">{entry.duration}</span>
              {/if}
            </div>
            <div class="timeline-run-boundary-actions">
              <span class="muted text-sm">
                {formatTime(entry.time)}{entry.ended ? ` → ${formatTime(entry.ended)}` : ' (in progress)'}
              </span>
              <button
                class="btn btn-sm btn-secondary"
                onclick={() => onToggleLog(entry.run.id)}
              >
                {expandedLogs[entry.run.id] ? 'Hide Run Details' : 'Run Details & Log'}
              </button>
            </div>
          </div>

          <div class="timeline-run-boundary-events">
            {#each entry.events as ev}
              <TimelineEventItem {ev} />
            {/each}
          </div>
        </div>
      {:else}
        <TimelineEventItem ev={entry.event} standalone />
      {/if}
    {/each}
  </div>
</div>

<style>
  .flow-section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 12px;
    flex-wrap: wrap;
    gap: 8px;
  }

  .flow-section-title {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .milestone-badge {
    background: rgba(16, 185, 129, 0.15);
    color: var(--success);
    border: 1px solid rgba(16, 185, 129, 0.3);
  }

  .flow-timeline-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding-left: 8px;
  }

  .timeline-run-boundary {
    border: 1px solid var(--border);
    border-left: 3px solid var(--primary);
    border-radius: 8px;
    background: var(--card-bg);
    overflow: hidden;
  }

  .timeline-run-succeeded {
    border-left-color: var(--success);
  }

  .timeline-run-failed {
    border-left-color: var(--error);
  }

  .timeline-run-running {
    border-left-color: var(--accent);
  }

  .timeline-run-boundary-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 8px 12px;
    background: rgba(125, 125, 125, 0.06);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
    gap: 8px;
  }

  .timeline-run-boundary-meta, .timeline-run-boundary-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .timeline-run-boundary-events {
    padding: 12px 16px 4px 24px;
  }
</style>
