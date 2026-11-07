<script lang="ts">
  // The Stats view (issue #319): the per-issue/PR metric vector, the same
  // vector grouped into breakdowns, the size labels' estimate against the
  // measured diff, and each milestone's burndown and burnup. The server
  // derives every number (GET /api/stats); this only names and draws them.
  import { onMount } from 'svelte';
  import type { StatsCell, StatsData, StatsIssue, StatsMilestone } from './types';
  import { formatSpan } from './timeline';
  import { reloadOn } from './reload.svelte';
  import {
    BREAKDOWN_LABELS,
    STATS_RANGE_PRESETS,
    agentByKindText,
    breakdownKey,
    burnMax,
    burnPath,
    burnValue,
    countMeasure,
    durationMeasure,
    statsQuery,
    target,
    tokens,
    usageText,
    type StatsRangePreset,
  } from './stats';

  interface Props {
    refreshKey: number;
    issuesLink: string;
    pullsLink: string;
  }

  let { refreshKey, issuesLink, pullsLink }: Props = $props();

  let preset = $state<StatsRangePreset>('30d');
  let customFrom = $state('');
  let customTo = $state('');
  let dimension = $state('actor');
  let data = $state<StatsData | null>(null);
  let loading = $state(false);
  let loadError = $state<string | null>(null);

  let customQuery = $derived(preset === 'custom' ? statsQuery(preset, new Date(), customFrom, customTo) : '');

  async function load() {
    const query = statsQuery(preset, new Date(), customFrom, customTo);
    if (loading || query === null) return;
    loading = true;
    try {
      const res = await fetch(`/api/stats?${query}`);
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error || `Failed to load the stats: ${res.statusText}`);
      data = body;
      loadError = null;
    } catch (e: any) {
      loadError = e.message || 'Failed to load the stats';
    } finally {
      loading = false;
    }
  }

  reloadOn(() => `${refreshKey} ${preset} ${customQuery}`, load);

  onMount(() => {
    const interval = setInterval(load, 60000);
    return () => clearInterval(interval);
  });

  let merged = $derived((data?.issues ?? []).filter((v) => v.merged));
  let breakdown = $derived((data?.breakdowns ?? []).find((b) => b.by === dimension) ?? null);

  function issueHref(v: StatsIssue): string {
    return v.issue ? `${issuesLink}/${v.issue}` : `${pullsLink}/${v.pr}`;
  }
</script>

<section class="card full-width stats-card">
  <div class="card-header">
    <div class="card-header-left">
      <h2>Stats</h2>
      {#if data}
        <span class="muted text-sm">
          {data.issues.length} issue{data.issues.length === 1 ? '' : 's'}, {merged.length} merged
        </span>
      {/if}
      {#if data && loading}
        <span class="muted text-sm stats-updating" role="status"><span class="spinner" aria-hidden="true"></span> Updating…</span>
      {/if}
    </div>
    <div class="card-header-right stats-controls">
      <label class="stats-range">
        <span class="sr-only">Range</span>
        <select bind:value={preset}>
          {#each STATS_RANGE_PRESETS as p}
            <option value={p.value}>{p.label}</option>
          {/each}
        </select>
      </label>
      {#if preset === 'custom'}
        <label class="stats-range">From <input type="datetime-local" bind:value={customFrom} /></label>
        <label class="stats-range">To <input type="datetime-local" bind:value={customTo} /></label>
      {/if}
    </div>
  </div>

  <div class="card-body">
    {#if customQuery === null}
      <div class="muted">Pick a start, and an end after it (or none, for now).</div>
    {:else if loadError}
      <div class="banner banner-error">{loadError}</div>
    {/if}
    {#if data?.incomplete?.length}
      <div class="banner stats-incomplete" title={data.incomplete.join('\n')}>
        Some forge reads failed; their numbers are missing ({data.incomplete.length}).
      </div>
    {/if}

    {#if !data && loading}
      <div class="loading-state stats-loading" role="status">
        <span class="spinner" aria-hidden="true"></span>
        Loading stats… The first load reads every issue and PR in the range from the forge, and can take a while.
      </div>
    {/if}

    {#if data}
      <p class="muted text-sm stats-note">
        Every number shows its coverage. Missing data is left out, never counted as zero;
        tokens are never added up across model families. Sizes are owner labels, never points.
      </p>

      <h3>Per issue and PR</h3>
      {#if data.issues.length === 0}
        <div class="muted">No issue or PR had activity in this window.</div>
      {:else}
        <div class="stats-scroll">
          <table class="stats-table">
            <thead>
              <tr>
                <th>Issue</th><th>Size</th><th>Milestone</th><th>Runs</th><th>Agent</th>
                <th>Impl / check rounds</th><th>Rejections</th><th>Failures</th><th>Owner</th>
                <th>Lead</th><th>Cycle</th><th>Measured</th><th>Usage</th>
              </tr>
            </thead>
            <tbody>
              {#each data.issues as v (target(v))}
                <tr>
                  <td class="stats-target">
                    <a href={issueHref(v)} target="_blank" rel="noreferrer">{target(v)}</a>
                    {#if v.title}<span class="stats-title">{v.title}</span>{/if}
                  </td>
                  <td>{v.size || '—'}</td>
                  <td>{v.milestone || '—'}</td>
                  <td>{v.runs}</td>
                  <td title={agentByKindText(v)}>{formatSpan(v.agent_seconds)}</td>
                  <td>{v.implement_rounds} / {v.check_rounds}</td>
                  <td>{v.verifier_rejections}{#if v.rejections_by_family} <span class="muted text-sm">{Object.entries(v.rejections_by_family).map(([k, n]) => `${k} ${n}`).join(', ')}</span>{/if}</td>
                  <td>{v.failed_runs}{#if v.failures_by_cause} <span class="muted text-sm">{Object.entries(v.failures_by_cause).map(([k, n]) => `${k} ${n}`).join(', ')}</span>{/if}</td>
                  <td title="interactive runs / flow stops">{v.interactive_runs} / {v.stops}</td>
                  <td>{v.lead_seconds !== undefined ? formatSpan(v.lead_seconds) : '—'}</td>
                  <td>{v.cycle_seconds !== undefined ? formatSpan(v.cycle_seconds) : '—'}</td>
                  <td>{v.measured ? `+${v.measured.added} −${v.measured.deleted}, ${v.measured.files} file${v.measured.files === 1 ? '' : 's'}` : '—'}</td>
                  <td>
                    {#if v.usage?.length}
                      {#each v.usage as u}
                        <div class="stats-usage"><span class="muted text-sm">{u.family || 'unknown'}:</span> {usageText(u)}</div>
                      {/each}
                    {:else}
                      <span class="muted">no runs</span>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      <h3>Breakdowns</h3>
      <div class="stats-dims" role="group" aria-label="Breakdown dimension">
        {#each data.breakdowns as b (b.by)}
          <button class="btn btn-sm" class:btn-primary={dimension === b.by} class:btn-secondary={dimension !== b.by}
            aria-pressed={dimension === b.by} onclick={() => (dimension = b.by)}>
            {BREAKDOWN_LABELS[b.by] ?? b.by}
          </button>
        {/each}
      </div>
      {#if breakdown}
        <div class="stats-scroll">
          <table class="stats-table">
            <thead>
              <tr>
                <th>{BREAKDOWN_LABELS[breakdown.by] ?? breakdown.by}</th><th>n</th><th>Lead</th><th>Cycle</th>
                <th>Agent</th><th>Impl rounds</th><th>Check rounds</th><th>Rejections</th>
              </tr>
            </thead>
            <tbody>
              {#each breakdown.cells as c (c.key)}
                <tr>
                  <td>{breakdownKey(breakdown.by, c.key)}</td>
                  <td>{c.n}</td>
                  <td>{durationMeasure(c.lead_seconds)}</td>
                  <td>{durationMeasure(c.cycle_seconds)}</td>
                  <td>{durationMeasure(c.agent_seconds)}</td>
                  <td>{countMeasure(c.implement_rounds)}</td>
                  <td>{countMeasure(c.check_rounds)}</td>
                  <td>{countMeasure(c.verifier_rejections)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      <h3>Size label vs measured</h3>
      <p class="muted text-sm">
        Measured buckets by changed lines, lock files excluded: S ≤ 20, M ≤ 100, L ≤ 400, XL above.
        Measured size is known only for merged PRs with a diff.
      </p>
      <div class="stats-scroll">
        <table class="stats-table">
          <thead>
            <tr><th>Size label</th><th>Issues</th><th>Measured</th><th>S</th><th>M</th><th>L</th><th>XL</th></tr>
          </thead>
          <tbody>
            {#each data.sizes as row (row.size)}
              <tr>
                <td>{row.size || 'unlabelled'}</td>
                <td>{row.n}</td>
                <td>{row.measured}</td>
                {#each row.buckets as b (b.bucket)}
                  <td>{b.n}</td>
                {/each}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      <h3>Milestone burndown and burnup</h3>
      {#if data.milestones.length === 0}
        <div class="muted">No milestone has issues.</div>
      {:else}
        <div class="stats-burns">
          {#each data.milestones as m (m.title)}
            {@render MilestoneBurn({ m, since: data.window.since, until: data.window.until })}
          {/each}
        </div>
      {/if}
    {/if}
  </div>
</section>

{#snippet MilestoneBurn({ m, since, until }: { m: StatsMilestone; since: string; until: string })}
  {@const sinceMs = Date.parse(since)}
  {@const untilMs = Date.parse(until)}
  {@const max = burnMax(m.points, m.weighted)}
  {@const x0 = 8}
  {@const x1 = 292}
  {@const y0 = 8}
  {@const y1 = 92}
  {@const totalPath = burnPath(m.points, (p) => burnValue(p, m.weighted).total, sinceMs, untilMs, x0, x1, y0, y1, max)}
  {@const closedPath = burnPath(m.points, (p) => burnValue(p, m.weighted).closed, sinceMs, untilMs, x0, x1, y0, y1, max)}
  <figure class="stats-burn">
    <figcaption>
      <strong>{m.title}</strong>
      <span class="muted text-sm">
        {m.closed} of {m.total} closed{m.weighted ? ' (weighted by size)' : ' (by count)'}
      </span>
    </figcaption>
    <svg viewBox="0 0 300 100" role="img" aria-label={`${m.title}: ${m.closed} of ${m.total} issues closed`}>
      <line x1={x0} y1={y1} x2={x1} y2={y1} class="stats-axis" />
      <line x1={x0} y1={y0} x2={x0} y2={y1} class="stats-axis" />
      <path d={totalPath} class="stats-total" fill="none" />
      <path d={closedPath} class="stats-closed" fill="none" />
    </svg>
    <div class="muted text-sm">— total scope · — closed · top {max}</div>
  </figure>
{/snippet}

<style>
  .stats-updating { display: inline-flex; align-items: center; gap: 6px; }
  .stats-controls {
    gap: 8px;
  }

  .stats-range {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--text-muted);
  }

  .stats-note {
    margin-top: 0;
  }

  .stats-incomplete {
    background: rgba(245, 158, 11, 0.12);
    border: 1px solid rgba(245, 158, 11, 0.4);
    color: var(--text);
    padding: 8px 12px;
    border-radius: 8px;
    font-size: 13px;
  }

  h3 {
    font-size: 14px;
    margin: 24px 0 8px;
  }

  .stats-scroll {
    overflow-x: auto;
  }

  .stats-table {
    border-collapse: collapse;
    font-size: 13px;
    width: 100%;
  }

  .stats-table th,
  .stats-table td {
    text-align: left;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
    white-space: nowrap;
  }

  .stats-table th {
    color: var(--text-muted);
    font-weight: 600;
  }

  .stats-target a {
    font-family: monospace;
    color: #818cf8;
    text-decoration: none;
  }

  .stats-target a:hover {
    text-decoration: underline;
  }

  .stats-title {
    margin-left: 8px;
  }

  .stats-usage + .stats-usage {
    margin-top: 2px;
  }

  .stats-dims {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 8px;
  }

  .stats-burns {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: 16px;
  }

  .stats-burn {
    margin: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 12px;
  }

  .stats-burn figcaption {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 8px;
  }

  .stats-burn svg {
    width: 100%;
    height: auto;
  }

  .stats-axis {
    stroke: var(--border);
    stroke-width: 1;
  }

  .stats-total {
    stroke: #818cf8;
    stroke-width: 2;
  }

  .stats-closed {
    stroke: #34d399;
    stroke-width: 2;
  }
</style>
