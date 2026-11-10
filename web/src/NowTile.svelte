<script lang="ts">
  // One tile of Now (monitor-ui design §3): a live batch run or check
  // group, its two-line header, the time since its last output, and a
  // fixed-height tail of its log. A click anywhere opens its page.
  import type { NowTile } from './now';
  import { formatSpan, lastOutputStatus } from './now';
  import { runRouteHash } from './routes';
  import LogLineView from './LogLineView.svelte';

  // The lines a tile shows; its height fits them, so it never jumps.
  const tailLines = 8;

  let { tile, now }: { tile: NowTile; now: number } = $props();

  let item = $derived(tile.kind === 'run' ? tile.run : tile.check);
  let last = $derived(lastOutputStatus(item, now));
  let lines = $derived((item.log_tail ?? []).slice(-tailLines));
  let elapsed = $derived(formatSpan(now - new Date(tile.created).getTime()));

  let target = $derived.by(() => {
    if (tile.kind === 'check') {
      const c = tile.check;
      return c.issue ? `#${c.issue}` : c.pr ? `!${c.pr}` : c.id;
    }
    return tile.run.target;
  });
  let title = $derived(tile.kind === 'run' ? tile.run.issue_title || tile.run.pr_title : tile.check.issue_title);

  // The run's limit: its key's lifetime, else what the server reports.
  let limit = $derived.by(() => {
    if (tile.kind === 'check') return tile.check.timeout;
    const r = tile.run;
    const span = new Date(r.expires).getTime() - new Date(r.created).getTime();
    return span > 0 ? formatSpan(span) : r.timeout;
  });

  let meta = $derived.by(() => {
    const parts: string[] = [];
    if (tile.kind === 'check') {
      const c = tile.check;
      parts.push(c.group === 'check' ? 'checks' : 'checks e2e');
      if (c.pr) parts.push(`!${c.pr}`);
      parts.push(c.sha.slice(0, 7));
    } else {
      const r = tile.run;
      parts.push(r.flow ? `${r.kind} ${r.flow.round}/${r.flow.max_rounds}` : r.kind);
      parts.push(r.actor);
    }
    parts.push(limit ? `${elapsed}/${limit}` : elapsed);
    return parts.join(' · ');
  });

  let denials = $derived(tile.kind === 'run' ? tile.run.denials?.length ?? 0 : 0);

  function open() {
    location.hash = runRouteHash(tile.id);
  }
</script>

<div
  class="now-tile"
  class:quiet={last.quiet}
  role="link"
  tabindex="0"
  data-id={tile.id}
  onclick={open}
  onkeydown={(e) => { if (e.key === 'Enter') open(); }}
>
  <div class="tile-head">
    <div class="tile-line">
      <strong class="tile-target">{target}</strong>
      {#if title}<span class="tile-title">{title}</span>{/if}
    </div>
    <div class="tile-line tile-meta">
      <span class="tile-meta-text">{meta}</span>
      {#if denials > 0}<span class="badge badge-error tile-denials">{denials} denied</span>{/if}
      <span class="tile-last" class:quiet={last.quiet} title="Since its last output">{last.text}</span>
    </div>
  </div>
  <div class="tile-tail log-tail-box">
    {#if lines.length === 0}
      <div class="log-line tile-empty">No output yet.</div>
    {:else}
      {#each lines as line, i (i)}
        <LogLineView {line} />
      {/each}
    {/if}
  </div>
</div>

<style>
  .now-tile {
    display: flex;
    flex-direction: column;
    min-width: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    cursor: pointer;
    background: rgba(125, 125, 125, 0.03);
  }

  .now-tile:hover,
  .now-tile:focus-visible {
    border-color: var(--accent);
  }

  .now-tile.quiet {
    border-color: var(--warning);
    box-shadow: inset 0 0 0 1px var(--warning);
  }

  .tile-head {
    padding: 8px 12px;
    background: rgba(125, 125, 125, 0.06);
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .tile-line {
    display: flex;
    align-items: baseline;
    gap: 8px;
    min-width: 0;
    white-space: nowrap;
  }

  .tile-title,
  .tile-meta-text {
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .tile-meta {
    font-size: 13px;
    color: var(--text-muted);
  }

  .tile-meta-text {
    flex: 1;
  }

  /* The badge keeps the line's height, so every tile is the same size. */
  .tile-meta {
    height: 20px;
    line-height: 20px;
  }

  .tile-denials {
    flex: none;
    padding-block: 0;
    line-height: 18px;
  }

  .tile-last {
    flex: none;
    font-family: monospace;
  }

  .tile-last.quiet {
    color: var(--warning);
    font-weight: 600;
  }

  /* A fixed height of tailLines lines, clipped at the top: the newest line
     is always at the bottom and the tile never changes size. */
  .tile-tail {
    height: calc(8 * 1.5em + 16px);
    padding: 8px 12px;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    background: #090d16;
    color: #e2e8f0;
    font-family: monospace;
    font-size: 12px;
    line-height: 1.5;
  }

  .tile-tail :global(.log-line) {
    flex: none;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .tile-tail :global(.log-details) {
    display: none;
  }

  .tile-empty {
    color: #94a3b8;
  }
</style>
