<script lang="ts">
  // Live, in Now (monitor-ui design §3): a wall of tiles, one per live
  // batch run and check group in start order; one line per flow with
  // nothing live and per interactive run; the threads that just ended;
  // and the buttons that start flows and runs or clean up.
  import type { ActiveFlowInfo, HistoryRunInfo, HistoryThread, LiveCheckInfo, LiveRunInfo } from './types';
  import { formatSpan, justEnded, lastOutputStatus, nowRows, nowTiles, type NowRow as NowRowInfo } from './now';
  import NowTile from './NowTile.svelte';

  interface Props {
    liveRuns: LiveRunInfo[] | undefined;
    liveChecks: LiveCheckInfo[] | undefined;
    activeFlows: ActiveFlowInfo[] | undefined;
    historyRuns: HistoryRunInfo[] | undefined;
    threads: HistoryThread[] | undefined;
    now: number;
    onStartFlow: () => void;
    onStartRun: () => void;
    onGC: () => void;
  }

  let { liveRuns, liveChecks, activeFlows, historyRuns, threads, now, onStartFlow, onStartRun, onGC }: Props = $props();

  let tiles = $derived(nowTiles(liveRuns, liveChecks));
  let rows = $derived(nowRows(activeFlows, liveRuns, liveChecks, historyRuns));
  let ended = $derived(justEnded(threads, now));
  let quiet = $derived(tiles.filter((t) => lastOutputStatus(t.kind === 'run' ? t.run : t.check, now).quiet).length);
  let waiting = $derived(rows.filter((r) => r.key.startsWith('flow-')).length);
  let interactive = $derived(rows.length - waiting);
  let summaryRest = $derived([waiting > 0 ? `${waiting} waiting` : '', interactive > 0 ? `${interactive} interactive` : ''].filter(Boolean));
  const rowText = (row: NowRowInfo) => [row.detail, row.since ? formatSpan(now - new Date(row.since).getTime()) : ''].filter(Boolean).join(' · ');
</script>

<section class="card full-width live-section">
  <div class="card-header">
    <div class="card-header-left">
      <h2>Live</h2>
      <span class="muted live-summary">
        {`${tiles.length} running`}{#if quiet > 0}{' · '}<span class="quiet-count">{quiet} quiet</span>{/if}{#each summaryRest as part (part)}{` · ${part}`}{/each}
      </span>
    </div>
    <div class="card-header-right">
      <button class="btn btn-sm btn-primary" onclick={onStartFlow}>
        Start Flow
      </button>
      <button class="btn btn-sm btn-secondary" onclick={onStartRun}>
        Start Run
      </button>
      <button class="btn btn-sm btn-secondary" onclick={onGC}>
        Clean up (gc)
      </button>
    </div>
  </div>

  <div class="card-body">
    {#if tiles.length === 0 && rows.length === 0}
      <div class="muted live-idle">Nothing running in this repository.</div>
    {/if}

    {#if tiles.length > 0}
      <div class="now-grid">
        {#each tiles as tile (tile.id)}
          <NowTile {tile} {now} />
        {/each}
      </div>
    {/if}

    {#if rows.length > 0}
      <ul class="now-rows">
        {#each rows as row (row.key)}
          <li class="now-row" data-key={row.key}>
            <a href={row.href} data-nav={row.key}>
              <strong>{row.label}</strong>
              {#if row.title}<span class="now-row-title">{row.title}</span>{/if}
              <span class="muted">{`· ${rowText(row)}`}</span>
            </a>
          </li>
        {/each}
      </ul>
    {/if}

    {#if ended.length > 0}
      <div class="just-ended muted">
        Just ended:
        {#each ended as t, i (t.key)}{i > 0 ? ', ' : ''}<a href={t.href} title={t.title}>{t.title ? `${t.label} ${t.title}` : t.label}</a>{/each}
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
