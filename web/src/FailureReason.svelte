<script lang="ts">
  // Why a failed or revoked run failed, for a history summary cell: what
  // bottown observed apart from what the agent reported, or "Reason
  // unavailable", and a button to the run's log, or its details when no
  // log was written. Each line is clipped to one; when that hides text,
  // "more" shows all of it inline (#133).
  import type { FailureDescription } from './types';
  import { failureText } from './state';
  import { watchTruncation } from './truncation';

  interface Props {
    failure: FailureDescription;
    onDetails?: () => void;
  }

  let { failure, onDetails }: Props = $props();

  let expanded = $state(false);
  let truncated = $state(false);
</script>

<div
  class="failure-reason"
  class:expanded
  title={expanded ? undefined : failureText(failure)}
  use:watchTruncation={{ key: [failure, expanded], onChange: (t) => (truncated = t) }}
>
  {#if failure.observed}
    <span class="failure-line clip failure-observed">
      <span class="failure-label">Observed</span>{failure.observed}{#if failure.observedDetail}: {failure.observedDetail}{/if}
    </span>
  {/if}
  {#if failure.reported}
    <span class="failure-line clip failure-reported">
      <span class="failure-label">Agent reported</span>{failure.reported}
    </span>
  {/if}
  {#if failure.unavailable}
    <span class="failure-line failure-unavailable">Reason unavailable</span>
  {/if}
  {#if truncated || expanded || onDetails}
    <span class="failure-actions">
      {#if truncated || expanded}
        <button
          type="button"
          class="failure-details"
          aria-expanded={expanded}
          title={expanded ? 'Show each line of the reason on one line' : 'Show the whole reason'}
          onclick={(e) => {
            e.stopPropagation();
            expanded = !expanded;
          }}
        >{expanded ? 'less' : 'more'}</button>
      {/if}
      {#if onDetails}
        <button
          type="button"
          class="failure-details"
          title={failure.hasLog ? "Open this run's log" : 'No log was written; open the run details'}
          onclick={(e) => {
            e.stopPropagation();
            onDetails?.();
          }}
        >{failure.hasLog ? 'View log' : 'View details'}</button>
      {/if}
    </span>
  {/if}
</div>

<style>
  .failure-reason {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    min-width: 0;
  }

  .failure-line {
    max-width: 100%;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .expanded .failure-line {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .failure-actions {
    display: flex;
    gap: 10px;
  }

  .failure-label {
    margin-right: 6px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.02em;
  }

  .failure-observed .failure-label {
    color: var(--error);
  }

  .failure-reported .failure-label,
  .failure-unavailable {
    color: var(--text-muted);
  }

  .failure-details {
    padding: 0;
    border: none;
    background: none;
    color: var(--accent);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
    text-decoration: underline;
  }
</style>
