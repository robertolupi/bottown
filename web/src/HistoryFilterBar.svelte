<script lang="ts">
  // The run history's filters: free-text search, actor, kind, status and
  // time window, bound into RunHistoryState.filters.
  import type { ActorDetail } from './types';
  import type { HistoryFilters } from './state';

  interface Props {
    filters: Required<HistoryFilters>;
    actors: ActorDetail[] | undefined;
  }

  let { filters = $bindable(), actors }: Props = $props();
</script>

<div class="card-header-right filters-row">
  <input
    type="text"
    class="input input-sm"
    placeholder="Search issue, PR, milestone, or summary..."
    bind:value={filters.search}
  />
  <select class="input input-sm select-sm" bind:value={filters.actor}>
    <option value="">All Actors</option>
    {#if actors}
      {#each actors as act}
        <option value={act.name}>{act.name}</option>
      {/each}
    {/if}
  </select>
  <select class="input input-sm select-sm" bind:value={filters.kind}>
    <option value="">All Kinds</option>
    <option value="implement">Implement</option>
    <option value="verify">Verify</option>
    <option value="discuss">Discuss</option>
    <option value="plan">Plan</option>
  </select>
  <select class="input input-sm select-sm" bind:value={filters.status}>
    <option value="">All Statuses</option>
    <option value="succeeded">Succeeded</option>
    <option value="failed">Failed</option>
    <option value="revoked">Revoked</option>
  </select>
  <select class="input input-sm select-sm" bind:value={filters.date}>
    <option value="">All Time</option>
    <option value="1h">Past hour</option>
    <option value="24h">Past 24 hours</option>
    <option value="7d">Past 7 days</option>
    <option value="30d">Past 30 days</option>
  </select>
</div>

<style>
  .filters-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .input {
    background: var(--card-bg);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 8px 12px;
    border-radius: 6px;
    font-size: 13px;
  }

  .input-sm {
    padding: 4px 8px;
    font-size: 12px;
  }

  .select-sm {
    cursor: pointer;
  }
</style>
