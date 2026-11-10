<script lang="ts">
  // A standalone thread (a discuss or retro run) in the run history: its one run's row.
  // Clicking it opens the run page. A windowed retro's target is its window, linking its wiki page.
  import type { HistoryThread } from './types';
  import { describeRunFailure, formatThreadMainLine, retroWindowTitle } from './state';
  import { formatTime, formatUsage } from './format';
  import { runRouteHash } from './routes';
  import FailureReason from './FailureReason.svelte';
  import IssueReference from './IssueReference.svelte';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import SummaryText from './SummaryText.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    thread: HistoryThread;
  }

  let { thread }: Props = $props();

  let mainLine = $derived(formatThreadMainLine(thread));
  let h = $derived(thread.runs[0]);
  let failure = $derived(thread.active ? null : describeRunFailure(h));

  function navigateToRun() {
    if (h) location.hash = runRouteHash(h.id);
  }
</script>

<tr
  class="discuss-thread-row"
  class:flow-row-active={thread.active}
  onclick={navigateToRun}
>
  <td>
    <div class="thread-main-line">
      {#if mainLine.issueLabel}
        <IssueReference
          number={thread.issue || 0}
          href={mainLine.issueUrl || h?.target_url || ''}
          label={mainLine.issueLabel}
          title={mainLine.issueTitle}
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
    </div>
  </td>
  <td><span class="badge badge-neutral">{h?.kind}</span></td>
  <td><span class="badge repo-badge">{h?.actor} ({h?.family})</span></td>
  <td>
    {#if thread.active}
      <span class="badge badge-accent flow-live-badge">active</span>
    {/if}
    {#if h}
      <span class="badge badge-{h.status === 'succeeded' ? 'success' : h.status === 'failed' ? 'error' : 'warning'}">
        {h.status}
      </span>
      {#if h.denials && h.denials.length > 0}
        <span class="badge badge-error" title="Operations the server's policy refused this run">
          {h.denials.length} denied
        </span>
      {/if}
    {/if}
  </td>
  <td>{#if h}<OutcomeBadge run={h} />{/if}</td>
  <td class="muted">{h?.duration || '—'}</td>
  <td class="muted text-sm">{formatUsage(h?.usage)}</td>
  {#if failure}
    <td class="summary-cell">
      <FailureReason
        {failure}
        onDetails={() => {
          if (h) location.hash = runRouteHash(h.id);
        }}
      />
    </td>
  {:else}
    <td class="summary-cell"><SummaryText text={thread.summary} /></td>
  {/if}
  <td class="muted text-sm">{formatTime(thread.last_activity)}</td>
  <td>
    <div class="row-actions">
      {#if h}
        <a
          class="btn btn-sm btn-secondary"
          href={runRouteHash(h.id)}
          onclick={(e) => e.stopPropagation()}
        >
          Run
        </a>
      {/if}
    </div>
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
    max-width: max(280px, 25vw);
  }

  .row-actions {
    display: flex;
    gap: 6px;
    align-items: center;
  }
</style>
