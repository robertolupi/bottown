<script lang="ts">
  // Run history (§4.2): ended and live runs grouped into threads, flows
  // and discussions, filtered, in a table whose flow rows expand. Its
  // state is App's RunHistoryState, which lasts across views and follows
  // the expanded flow as runs start and end.
  import type { ActorDetail, HistoryThread } from './types';
  import type { RunHistoryState } from './history.svelte';
  import { filterHistoryThreads } from './state';
  import DiscussThreadRow from './DiscussThreadRow.svelte';
  import FlowThreadRow from './FlowThreadRow.svelte';
  import HistoryFilterBar from './HistoryFilterBar.svelte';

  interface Props {
    history: RunHistoryState;
    threads: HistoryThread[];
    actors: ActorDetail[] | undefined;
    now: number;
  }

  let { history, threads, actors, now }: Props = $props();

  let filteredThreads = $derived(filterHistoryThreads(threads, history.filters, now));
  const onToggleLog = (id: string) => history.toggleLog(id);
  const onSelectFlow = (thread: HistoryThread) => history.selectFlow(thread);
</script>

<section class="card full-width">
  <div class="card-header">
    <div class="card-header-left">
      <h2 id="run-history-heading">Run History</h2>
      <span class="badge badge-neutral">
        {threads.length} {threads.length === 1 ? 'thread' : 'threads'} · {history.runs.length} {history.runs.length === 1 ? 'run' : 'runs'}
      </span>
    </div>
    <HistoryFilterBar bind:filters={history.filters} {actors} />
  </div>

  <div class="card-body">
    {#if history.loading}
      <div class="loading-state">Loading history...</div>
    {:else if filteredThreads.length === 0}
      <div class="inbox-zero">
        <span>No matching history threads found.</span>
      </div>
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div
        class="table-container history-table-container"
        tabindex="0"
        role="region"
        aria-labelledby="run-history-heading"
      >
        <table class="data-table">
          <thead>
            <tr>
              <th>Flow / Target</th>
              <th>Kind</th>
              <th>Actors</th>
              <th>Status</th>
              <th>Outcome</th>
              <th>Duration</th>
              <th>Usage</th>
              <th>Summary</th>
              <th>Last Activity</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each filteredThreads as thread (thread.key)}
              {#if thread.kind === 'discuss'}
                <DiscussThreadRow {thread} logState={history.logs[thread.runs[0].id]} {onToggleLog} />
              {:else}
                <FlowThreadRow
                  {thread}
                  expanded={history.expandedThreadKey === thread.key}
                  timeline={history.timelines[thread.key]}
                  timelineLoading={history.timelineLoading[thread.key]}
                  timelineError={history.timelineError[thread.key]}
                  expandedLogs={history.logs}
                  onSelect={onSelectFlow}
                  {onToggleLog}
                />
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
</section>

<style>
  .table-container {
    overflow-x: auto;
  }

  .history-table-container {
    max-height: 1320px;
    max-block-size: 1320px;
    overflow: auto;
  }

  /* The table's head and cells, the nested tables' in an expanded flow
     included, whichever row component renders them. */
  .history-table-container :global(thead) {
    position: sticky;
    inset-block-start: 0;
    z-index: 1;
  }

  .history-table-container :global(th) {
    background: var(--card-bg);
  }

  .data-table {
    width: 100%;
    border-collapse: separate;
    border-spacing: 0;
    font-size: 13px;
  }

  .data-table :global(th), .data-table :global(td) {
    padding: 8px 12px;
    text-align: left;
    border-bottom: 1px solid var(--border);
  }

  .data-table :global(th) {
    color: var(--text-muted);
    font-weight: 500;
  }
</style>
