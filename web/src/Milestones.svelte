<script lang="ts">
  // The Milestones view (docs/design/milestones.md §2.7): each
  // Implementation Epic, or Forgejo milestone without one, with its
  // progress and its dependency DAG. Inline SVG draws the edges and plain
  // DOM the nodes, so every title is escaped text under the page's CSP.
  import { onMount } from 'svelte';
  import type { MilestoneGroup, MilestoneNode } from './types';
  import IssueReference from './IssueReference.svelte';
  import { actorPair, canOpenFlow, canStart, layoutDag, statusBadge } from './milestones';

  interface Props {
    refreshKey: number;
    onFlow: (issue: number) => void;
    onRun: (issue: number) => void;
  }

  let { refreshKey, onFlow, onRun }: Props = $props();

  let groups = $state<MilestoneGroup[]>([]);
  let loading = $state(false);
  let loaded = $state(false);
  let loadError = $state<string | null>(null);
  let showClosed = $state(false);

  let openGroups = $derived(groups.filter((g) => isOpen(g)));
  let closedGroups = $derived(groups.filter((g) => !isOpen(g)));

  function isOpen(g: MilestoneGroup): boolean {
    if (g.epic) return g.epic.state !== 'closed';
    return g.milestone?.state !== 'closed';
  }

  async function load() {
    if (loading) return;
    loading = true;
    try {
      const res = await fetch('/api/milestones');
      const data = await res.json();
      if (!res.ok) throw new Error(data?.error || `Failed to load milestones: ${res.statusText}`);
      groups = data;
      loadError = null;
    } catch (e: any) {
      loadError = e.message || 'Failed to load milestones';
    } finally {
      loading = false;
      loaded = true;
    }
  }

  $effect(() => {
    void refreshKey;
    load();
  });

  onMount(() => {
    const interval = setInterval(load, 30000);
    return () => clearInterval(interval);
  });

  function cardKey(e: KeyboardEvent, node: MilestoneNode) {
    if (e.target !== e.currentTarget) return;
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onFlow(node.number);
    }
  }
</script>

{#snippet nodeBody(n: MilestoneNode)}
  {@const badge = statusBadge(n)}
  <div class="ms-node-head">
    <IssueReference number={n.number} href={n.url} label={`#${n.number}`} title={n.title} />
    <span class="badge badge-{badge.tone} ms-status">{badge.label}</span>
  </div>
  <div class="ms-node-title">{n.title}</div>
  <div class="ms-node-meta muted">
    {#if n.key}<code>{n.key}</code>{/if}
    {#if actorPair(n)}<span>{actorPair(n)}</span>{/if}
  </div>
  <div class="ms-node-foot">
    {#if n.run}
      <span class="badge badge-accent" title={n.run.id}>{n.run.kind} · {n.run.actor} · {n.run.elapsed}</span>
    {:else if n.flow && n.status === 'running'}
      <span class="badge badge-accent">flow · between runs</span>
    {/if}
    {#if n.pr}
      <a
        class="ms-pr"
        href={n.pr.url}
        target="_blank"
        rel="noreferrer"
        title={n.pr.title}
        onclick={(e) => e.stopPropagation()}
      >!{n.pr.number}{n.pr.state !== 'open' ? ` ${n.pr.state}` : ''}</a>
    {/if}
    {#if canStart(n)}
      <span class="ms-actions">
        <button
          class="btn btn-xs btn-primary"
          onclick={(e) => { e.stopPropagation(); onFlow(n.number); }}
        >Flow</button>
        <button
          class="btn btn-xs btn-secondary"
          onclick={(e) => { e.stopPropagation(); onRun(n.number); }}
        >Run</button>
      </span>
    {/if}
  </div>
{/snippet}

{#snippet groupCard(g: MilestoneGroup)}
  {@const layout = layoutDag(g)}
  <section class="card full-width ms-card" data-group={g.id}>
    <div class="card-header">
      <div class="card-header-left ms-title">
        {#if g.epic}
          <h2>{g.epic.title}</h2>
          <a class="ms-pr" href={g.epic.url} target="_blank" rel="noreferrer">#{g.epic.number}</a>
          {#if g.epic.state === 'closed'}<span class="badge badge-success">closed</span>{/if}
        {:else if g.milestone}
          <h2>{g.milestone.title}</h2>
          <span class="badge badge-neutral">no plan epic</span>
        {/if}
      </div>
      <div class="card-header-right">
        <span class="badge badge-neutral ms-base" title="Base branch for this milestone's issues">base: {g.base}</span>
      </div>
    </div>

    <div class="card-body">
      <div class="ms-summary">
        {#if g.milestone}
          <span>Milestone <a href={g.milestone.url} target="_blank" rel="noreferrer">{g.milestone.title} ↗</a></span>
        {/if}
        {#if g.kickoff}
          <span>Kickoff
            <IssueReference number={g.kickoff.number} href={g.kickoff.url} label={`#${g.kickoff.number}`} title={g.kickoff.title} />
          </span>
        {/if}
        {#if g.design}
          <span>Design <a href={g.design_url} target="_blank" rel="noreferrer"><code>{g.design}</code></a></span>
        {/if}
        {#if g.plan_file}
          <span>Plan <code>{g.plan_file}</code></span>
        {/if}
      </div>

      <div class="ms-counts">
        <span class="ms-progress" title="Closed of total">
          <span class="ms-progress-bar"><span style="width: {g.counts.total ? (100 * g.counts.closed) / g.counts.total : 0}%"></span></span>
          <strong>{g.counts.closed}/{g.counts.total}</strong> closed
        </span>
        <span class="badge badge-accent">{g.counts.ready} ready</span>
        <span class="badge badge-accent">{g.counts.running} running</span>
        <span class="badge badge-neutral">{g.counts.in_review} in review</span>
        <span class="badge badge-success">{g.counts.awaiting_merge} awaiting merge</span>
        <span class="badge badge-warning">{g.counts.blocked} blocked</span>
        <span class="badge badge-error">{g.counts.stopped} stopped</span>
      </div>

      {#if g.error}
        <div class="banner banner-error"><span>{g.error}</span></div>
      {/if}

      {#if g.removed.length > 0}
        <div class="banner banner-warning ms-removed">
          <span>
            <strong>Flagged (removed from plan):</strong>
            {#each g.removed as r, i}
              {#if i > 0}, {/if}
              <IssueReference number={r.number} href={r.url} label={`#${r.number}`} title={r.title} />
              {#if r.key}<code>{r.key}</code>{/if} ({r.state})
            {/each}
          </span>
        </div>
      {/if}

      {#if layout.nodes.length === 0}
        <div class="muted text-sm">No issues.</div>
      {:else}
        <div class="ms-dag-scroll">
          <div class="ms-dag" style="width: {layout.width}px; height: {layout.height}px">
            <svg class="ms-edges" width={layout.width} height={layout.height} aria-hidden="true">
              <defs>
                <marker id="ms-arrow-{g.id}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                  <path d="M 0 0 L 10 5 L 0 10 z" class="ms-arrowhead" />
                </marker>
              </defs>
              {#each layout.edges as e (e.key)}
                <path d={e.path} class="ms-edge" class:ms-edge-blocking={e.blocking} marker-end="url(#ms-arrow-{g.id})">
                  <title>#{e.to} depends on #{e.from}</title>
                </path>
              {/each}
            </svg>

            {#each layout.nodes as p (p.node.number)}
              {@const n = p.node}
              {#if n.external}
                <div
                  class="ms-pill"
                  class:ms-pill-closed={n.state === 'closed'}
                  style="left: {p.x}px; top: {p.y}px; width: {p.width}px; height: {p.height}px"
                  title="Dependency outside this milestone"
                >
                  <IssueReference number={n.number} href={n.url} label={`#${n.number}`} title={n.title} />
                  <span class="ms-pill-title">{n.title}</span>
                  <span class="muted text-sm">{n.state}</span>
                </div>
              {:else if canOpenFlow(n)}
                <div
                  class="ms-node ms-clickable ms-status-{n.status}"
                  style="left: {p.x}px; top: {p.y}px; width: {p.width}px; height: {p.height}px"
                  role="button"
                  tabindex="0"
                  title="Start a flow on #{n.number}"
                  onclick={() => onFlow(n.number)}
                  onkeydown={(e) => cardKey(e, n)}
                >
                  {@render nodeBody(n)}
                </div>
              {:else}
                <div
                  class="ms-node ms-status-{n.status}"
                  style="left: {p.x}px; top: {p.y}px; width: {p.width}px; height: {p.height}px"
                >
                  {@render nodeBody(n)}
                </div>
              {/if}
            {/each}
          </div>
        </div>
      {/if}
    </div>
  </section>
{/snippet}

<div class="grid">
  {#if loadError}
    <div class="banner banner-error full-width">
      <span>{loadError}</span>
      <button class="btn btn-sm btn-secondary" onclick={() => load()}>Retry</button>
    </div>
  {/if}

  {#if !loaded}
    <div class="loading-state full-width">Loading milestones...</div>
  {:else if groups.length === 0 && !loadError}
    <section class="card full-width">
      <div class="card-body">
        <div class="inbox-zero">
          <span>No milestones yet. Apply a merged plan with <code>bottown milestone apply SLUG</code>.</span>
        </div>
      </div>
    </section>
  {:else}
    {#each openGroups as g (g.id)}
      {@render groupCard(g)}
    {/each}
    {#if closedGroups.length > 0}
      <div class="full-width">
        <button class="btn btn-sm btn-secondary" onclick={() => (showClosed = !showClosed)}>
          {showClosed ? 'Hide' : 'Show'} {closedGroups.length} closed
        </button>
      </div>
      {#if showClosed}
        {#each closedGroups as g (g.id)}
          {@render groupCard(g)}
        {/each}
      {/if}
    {/if}
  {/if}
</div>

<style>
  .ms-title h2 {
    margin: 0;
  }

  .ms-summary {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 20px;
    font-size: 13px;
    margin-bottom: 10px;
  }

  .ms-summary a {
    color: var(--primary);
  }

  .ms-counts {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-bottom: 12px;
    font-size: 13px;
  }

  .ms-progress {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin-right: 8px;
  }

  .ms-progress-bar {
    display: inline-block;
    width: 120px;
    height: 8px;
    border-radius: 4px;
    background: var(--border);
    overflow: hidden;
  }

  .ms-progress-bar > span {
    display: block;
    height: 100%;
    background: var(--success);
  }

  .ms-removed {
    margin-bottom: 12px;
  }

  .banner-warning {
    background: var(--warning-bg);
    color: var(--warning);
  }

  .ms-pr {
    background: rgba(99, 102, 241, 0.15);
    color: #818cf8;
    text-decoration: none;
    font-family: monospace;
    font-weight: bold;
    padding: 2px 6px;
    border-radius: 4px;
  }

  .ms-pr:hover {
    text-decoration: underline;
  }

  .ms-dag-scroll {
    overflow-x: auto;
    padding-bottom: 4px;
  }

  .ms-dag {
    position: relative;
  }

  .ms-edges {
    position: absolute;
    left: 0;
    top: 0;
    pointer-events: none;
  }

  .ms-edges :global(.ms-edge) {
    fill: none;
    stroke: var(--text-muted);
    stroke-width: 1.5;
    opacity: 0.6;
  }

  .ms-edges :global(.ms-edge-blocking) {
    stroke: var(--warning);
    opacity: 0.9;
  }

  .ms-edges :global(.ms-arrowhead) {
    fill: var(--text-muted);
  }

  .ms-node,
  .ms-pill {
    position: absolute;
    box-sizing: border-box;
    border: 1px solid var(--border);
    background: var(--card-bg);
    border-radius: 8px;
  }

  .ms-node {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 8px 10px;
    border-left-width: 4px;
    font-size: 13px;
  }

  .ms-clickable {
    cursor: pointer;
  }

  .ms-clickable:hover,
  .ms-clickable:focus-visible {
    box-shadow: 0 0 0 2px var(--primary);
    outline: none;
  }

  .ms-status-closed { border-left-color: var(--success); opacity: 0.75; }
  .ms-status-approved { border-left-color: var(--success); }
  .ms-status-ready,
  .ms-status-running { border-left-color: var(--accent); }
  .ms-status-in_review { border-left-color: var(--text-muted); }
  .ms-status-blocked { border-left-color: var(--warning); }
  .ms-status-stopped { border-left-color: var(--error); }

  .ms-node-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 6px;
  }

  .ms-status {
    max-width: 150px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .ms-node-title {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .ms-node-meta {
    display: flex;
    gap: 6px;
    font-size: 12px;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .ms-node-foot {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: auto;
    font-size: 12px;
  }

  .ms-actions {
    margin-left: auto;
    display: inline-flex;
    gap: 4px;
  }

  .ms-pill {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 0 12px;
    border-radius: 18px;
    border-style: dashed;
    font-size: 12px;
  }

  .ms-pill-closed {
    opacity: 0.7;
  }

  .ms-pill-title {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .btn-xs {
    padding: 2px 8px;
    font-size: 11px;
  }
</style>
