<script lang="ts">
  // Live, in Now (monitor-ui design §3): the server's live view (GET
  // /api/live) laid out: a wall of tiles, one per item in its order; one
  // line per row; the threads that just ended; with links to their run and
  // flow pages.
  import type { Live } from './types';
  import { endedHref, rowHref, rowText } from './now';
  import NowTile from './NowTile.svelte';

  interface Props {
    live: Live;
    now: number;
  }

  let { live, now }: Props = $props();

  let tiles = $derived(live.items ?? []);
  let rows = $derived(live.rows ?? []);
  let ended = $derived(live.ended ?? []);
  let summary = $derived(live.summary);
  let summaryRest = $derived(
    [summary.waiting > 0 ? `${summary.waiting} waiting` : '', summary.interactive > 0 ? `${summary.interactive} interactive` : ''].filter(Boolean),
  );
</script>

<section class="card full-width live-section">
  <div class="card-header">
    <div class="card-header-left">
      <h2>Live</h2>
      <span class="muted live-summary">
        {`${summary.running} running`}{#if summary.quiet > 0}{' · '}<span class="quiet-count">{summary.quiet} quiet</span>{/if}{#each summaryRest as part (part)}{` · ${part}`}{/each}
      </span>
    </div>

  </div>

  <div class="card-body">
    {#if tiles.length === 0 && rows.length === 0}
      <div class="muted live-idle">Nothing running in this repository.</div>
    {/if}

    {#if tiles.length > 0}
      <div class="now-grid">
        {#each tiles as item (item.id)}
          <NowTile {item} {now} />
        {/each}
      </div>
    {/if}

    {#if rows.length > 0}
      <ul class="now-rows">
        {#each rows as row (row.key)}
          <li class="now-row" data-key={row.key}>
            <a href={rowHref(row)}>
              <strong>{row.target}</strong>
              {#if row.title}<span class="now-row-title">{row.title}</span>{/if}
              <span class="muted">{`· ${rowText(row, now)}`}</span>
            </a>
          </li>
        {/each}
      </ul>
    {/if}

    {#if ended.length > 0}
      <div class="just-ended muted">
        Just ended:
        {#each ended as t, i (t.key)}{i > 0 ? ', ' : ''}<a href={endedHref(t)} title={t.title}>{t.title ? `${t.target} ${t.title}` : t.target}</a>{/each}
      </div>
    {/if}
  </div>
</section>

<style>
  .now-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(420px, 100%), 1fr));
    gap: 12px;
  }

  .quiet-count {
    color: var(--warning);
    font-weight: 600;
  }

  .now-rows {
    list-style: none;
    margin: 12px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .now-row a {
    display: flex;
    gap: 8px;
    align-items: baseline;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: inherit;
    text-decoration: none;
    padding: 4px 8px;
    border-radius: 6px;
  }

  .now-row a:hover {
    background: rgba(125, 125, 125, 0.08);
  }

  .now-row-title {
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .just-ended {
    margin-top: 12px;
    font-size: 13px;
  }

  .just-ended a {
    color: var(--accent);
  }
</style>
