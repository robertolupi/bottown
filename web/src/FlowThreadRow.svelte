<script lang="ts">
  // A flow thread in the run history: one row for the flow (issue, PR,
  // milestone, actors, status). Clicking it opens the flow page.
  // When its latest run failed, the summary cell is that run's failure reason.
  import type { HistoryThread } from './types';
  import { describeRunFailure, formatThreadMainLine } from './state';
  import { formatTime, formatUsage } from './format';
  import { flowRouteHash, runRouteHash } from './routes';
  import FailureReason from './FailureReason.svelte';
  import FlowStateBadge from './FlowStateBadge.svelte';
  import IssueReference from './IssueReference.svelte';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import SummaryText from './SummaryText.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    thread: HistoryThread;
  }

  let { thread }: Props = $props();

  let mainLine = $derived(formatThreadMainLine(thread));
  let lastRun = $derived(thread.runs[thread.runs.length - 1]);
  // A flow whose latest run failed, and that is not going again, leads
  // with why.
  let failure = $derived(thread.active || !lastRun ? null : describeRunFailure(lastRun));

  function navigateToFlow() {
    location.hash = flowRouteHash(thread.issue ?? 0, thread.pr);
  }
</script>

<tr
  class="flow-thread-row"
  class:flow-row-active={thread.active}
  onclick={navigateToFlow}
>
  <td>
    <div class="thread-main-line">
      {#if mainLine.issueLabel}
        <IssueReference
          number={thread.issue || 0}
          href={mainLine.issueUrl || lastRun?.target_url || ''}
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
      {#if thread.flow_state}
        <FlowStateBadge state={thread.flow_state} />
      {/if}
      {#if thread.active && thread.active_label}
        <span class="badge badge-accent flow-live-badge">
          {thread.active_label}
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
  {#if failure}
    <td class="summary-cell">
      <FailureReason
        {failure}
        onDetails={() => {
          if (lastRun) location.hash = runRouteHash(lastRun.id);
        }}
      />
    </td>
  {:else}
    <td class="summary-cell"><SummaryText text={thread.summary} /></td>
  {/if}
  <td class="muted text-sm">{formatTime(thread.last_activity)}</td>
  <td>
    <div class="row-actions">
      <a
        class="btn btn-sm btn-secondary"
        href={flowRouteHash(thread.issue ?? 0, thread.pr)}
        onclick={(e) => e.stopPropagation()}
      >
        Flow ({thread.run_count})
      </a>
    </div>
  </td>
</tr>

<style>
  .flow-thread-row {
    cursor: pointer;
    transition: background-color 0.12s ease;
  }

  .flow-thread-row:hover {
    background: rgba(125, 125, 125, 0.06);
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
  }

  .row-actions {
    display: flex;
    gap: 6px;
    align-items: center;
  }
</style>
