<script lang="ts">
  // A flow thread in the run history (§4.2): one row with five cells —
  // target, actors, state, duration and cost, last activity. Clicking it
  // opens the flow page. The target holds the issue and PR references,
  // the title, the kind and run count as a muted suffix, the summary as a
  // second clipped line, and, when its latest run failed, that reason
  // under the summary.
  import type { HistoryThread } from './types';
  import { describeRunFailure, formatThreadMainLine } from './state';
  import { formatCost, formatDateTime, formatRelativeTime } from './format';
  import { flowRouteHash, runRouteHash } from './routes';
  import FailureReason from './FailureReason.svelte';
  import FlowStateBadge from './FlowStateBadge.svelte';
  import IssueReference from './IssueReference.svelte';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import SummaryText from './SummaryText.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    thread: HistoryThread;
    now: number;
  }

  let { thread, now }: Props = $props();

  let mainLine = $derived(formatThreadMainLine(thread));
  let lastRun = $derived(thread.runs[thread.runs.length - 1]);
  // A flow whose latest run failed, and that is not going again, keeps
  // why on the row.
  let failure = $derived(thread.active || !lastRun ? null : describeRunFailure(lastRun));

  // These flow states already imply the run's recorded outcome (they fold
  // the review verdict in); elsewhere the row shows a recorded outcome too.
  const outcomeImplied = new Set(['merged', 'ready_to_merge', 'needs_changes', 'needs_intervention']);
  let showOutcome = $derived(
    !!lastRun?.outcome && (!thread.flow_state || !outcomeImplied.has(thread.flow_state.state)),
  );
  let cost = $derived(formatCost(thread.usage));

  function navigateToFlow() {
    location.hash = flowRouteHash(thread.issue ?? 0, thread.pr);
  }
</script>

<tr
  class="flow-thread-row"
  class:flow-row-active={thread.active}
  role="link"
  tabindex="0"
  data-nav={thread.key}
  onclick={navigateToFlow}
>
  <td class="target-cell">
    <div class="thread-main-line">
      {#if mainLine.issueLabel}
        <IssueReference
          number={thread.issue || 0}
          href={mainLine.issueUrl || lastRun?.target_url || ''}
          label={mainLine.issueLabel}
          title={mainLine.issueTitle}
          showTitle
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
      <span class="thread-meta text-sm">
        flow · {thread.run_count} {thread.run_count === 1 ? 'run' : 'runs'}
      </span>
    </div>
    {#if thread.summary}
      <div class="thread-summary summary-cell">
        <SummaryText text={thread.summary} />
      </div>
    {/if}
    {#if failure}
      <div class="thread-failure summary-cell">
        <FailureReason
          {failure}
          onDetails={() => {
            if (lastRun) location.hash = runRouteHash(lastRun.id);
          }}
        />
      </div>
    {/if}
  </td>
  <td>
    <div class="thread-actors">
      {#each thread.actors as act}
        <span class="badge repo-badge">{act}</span>
      {/each}
    </div>
  </td>
  <td>
    <div class="thread-state">
      {#if thread.flow_state}
        <FlowStateBadge state={thread.flow_state} />
      {/if}
      {#if showOutcome && lastRun}
        <OutcomeBadge run={lastRun} />
      {/if}
      {#if thread.total_denials > 0}
        <span class="badge badge-error" title="Operations refused by server policy in this flow">
          {thread.total_denials} denied
        </span>
      {/if}
    </div>
  </td>
  <td class="muted">
    {thread.duration || '—'}{#if cost} · {cost}{/if}
  </td>
  <td class="muted text-sm">
    <span title={formatDateTime(thread.last_activity)}>
      {formatRelativeTime(thread.last_activity, now)}
    </span>
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

  .target-cell {
    min-width: 0;
  }

  .thread-main-line {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .thread-meta {
    color: var(--text-muted);
    white-space: nowrap;
  }

  .thread-summary,
  .thread-failure {
    margin-top: 4px;
    min-width: 0;
    overflow: hidden;
  }

  .milestone-badge {
    background: rgba(16, 185, 129, 0.15);
    color: var(--success);
    border: 1px solid rgba(16, 185, 129, 0.3);
  }

  .thread-actors, .thread-state {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .summary-cell {
    max-width: 100%;
  }
</style>
