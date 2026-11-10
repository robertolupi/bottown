<script lang="ts">
  // Run history (§4.2): ended and live runs grouped into threads, flows
  // and discussions, filtered, in a table of five columns — target,
  // actors, state, duration and cost, last activity. A row opens its
  // page. Its state is App's RunHistoryState, which lasts across views.
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
              <th>Target</th>
              <th>Actors</th>
              <th>State</th>
              <th>Duration · cost</th>
              <th>Last activity</th>
            </tr>
          </thead>
          <tbody>
            {#each filteredThreads as thread (thread.key)}
              {#if thread.kind === 'discuss'}
                <DiscussThreadRow {thread} {now} />
              {:else}
                <FlowThreadRow {thread} {now} />
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
</section>

<style>
  /* The view fills the window's height instead of a fixed panel height:
     the table scrolls inside the card, so the head stays and the rows
     scroll, with no sideways scroll. */
  .card.full-width {
    height: calc(100vh - 160px);
    height: calc(100dvh - 160px);
    min-height: 320px;
  }

  .card-body {
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  .table-container {
    overflow-x: auto;
  }

  .history-table-container {
    flex: 1;
    min-height: 0;
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
    table-layout: fixed;
  }

  .data-table :global(th), .data-table :global(td) {
    padding: 8px 12px;
    text-align: left;
    border-bottom: 1px solid var(--border);
    box-sizing: border-box;
    vertical-align: top;
  }

  .data-table :global(th) {
    color: var(--text-muted);
    font-weight: 500;
  }

  /* Fixed widths keep the row inside the view's width: the target takes
     the room the four narrow columns leave. */
  .data-table :global(th:nth-child(1)) { width: 46%; }
  .data-table :global(th:nth-child(2)) { width: 12%; }
  .data-table :global(th:nth-child(3)) { width: 18%; }
  .data-table :global(th:nth-child(4)) { width: 13%; }
  .data-table :global(th:nth-child(5)) { width: 11%; }
</style>
