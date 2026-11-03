<script lang="ts">
  // Why a failed or revoked run failed, for a history summary cell: what
  // bottown observed apart from what the agent reported, or "Reason
  // unavailable", and a button to the run's log, or its details when no
  // log was written.
  import type { FailureDescription } from './types';
  import { failureText } from './state';

  interface Props {
    failure: FailureDescription;
    onDetails?: () => void;
  }

  let { failure, onDetails }: Props = $props();
</script>

<div class="failure-reason" title={failureText(failure)}>
  {#if failure.observed}
    <span class="failure-line failure-observed">
      <span class="failure-label">Observed</span>{failure.observed}{#if failure.observedDetail}: {failure.observedDetail}{/if}
    </span>
  {/if}
  {#if failure.reported}
    <span class="failure-line failure-reported">
      <span class="failure-label">Agent reported</span>{failure.reported}
    </span>
  {/if}
  {#if failure.unavailable}
    <span class="failure-line failure-unavailable">Reason unavailable</span>
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
