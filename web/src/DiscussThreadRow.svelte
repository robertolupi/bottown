<script lang="ts">
  // A standalone thread (a discuss or retro run) in the run history: its one
  // run's row, with the same five cells as a flow row. Clicking it opens the
  // run page. A windowed retro's target is its window, linking its wiki page.
  import type { HistoryThread } from './types';
  import { describeRunFailure, formatThreadMainLine, retroWindowTitle } from './state';
  import { formatCost, formatDateTime, formatRelativeTime } from './format';
  import { runRouteHash } from './routes';
  import FailureReason from './FailureReason.svelte';
  import IssueReference from './IssueReference.svelte';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import SummaryText from './SummaryText.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    thread: HistoryThread;
    now: number;
  }

  let { thread, now }: Props = $props();

  let mainLine = $derived(formatThreadMainLine(thread));
  let h = $derived(thread.runs[0]);
  let failure = $derived(thread.active ? null : describeRunFailure(h));
  let cost = $derived(formatCost(thread.usage));

  function navigateToRun() {
    if (h) location.hash = runRouteHash(h.id);
  }
</script>

<tr
  class="discuss-thread-row"
  class:flow-row-active={thread.active}
  onclick={navigateToRun}
>
  <td class="target-cell">
    <div class="thread-main-line">
      {#if mainLine.issueLabel}
        <IssueReference
          number={thread.issue || 0}
          href={mainLine.issueUrl || h?.target_url || ''}
          label={mainLine.issueLabel}
          title={mainLine.issueTitle}
          showTitle
        />
      {:else if h}
        <TargetLink href={h.target_url} label={h.target} title={retroWindowTitle(h.window)} stopPropagation />
        {#if h.window}
          <span class="badge instance-badge" title="The window's start, as the owner gave it">since {h.window.since}</span>
        {/if}
      {/if}
      {#if mainLine.milestone}
        <span class="badge milestone-badge" title="Milestone">{mainLine.milestone}</span>
      {/if}
      <span class="thread-meta text-sm">
        {thread.kind} · {thread.run_count} {thread.run_count === 1 ? 'run' : 'runs'}
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
            if (h) location.hash = runRouteHash(h.id);
          }}
        />
      </div>
    {/if}
  </td>
  <td>
    <div class="thread-actors">
      {#if h}
        <span class="badge repo-badge">{h.actor} ({h.family})</span>
      {/if}
    </div>
  </td>
  <td>
    <div class="thread-state">
      {#if h}
        <StatusBadge status={h.status} />
        {#if h.outcome}
          <OutcomeBadge run={h} />
        {/if}
        {#if h.denials && h.denials.length > 0}
          <span class="badge badge-error" title="Operations the server's policy refused this run">
            {h.denials.length} denied
          </span>
        {/if}
      {/if}
    </div>
  </td>
  <td class="muted">
    {h?.duration || '—'}{#if cost} · {cost}{/if}
  </td>
  <td class="muted text-sm">
    <span title={formatDateTime(thread.last_activity)}>
      {formatRelativeTime(thread.last_activity, now)}
    </span>
  </td>
</tr>

<style>
  .discuss-thread-row {
    cursor: pointer;
    transition: background-color 0.12s ease;
  }

  .discuss-thread-row:hover {
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
