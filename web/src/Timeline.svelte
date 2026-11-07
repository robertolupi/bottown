<script lang="ts">
  // The Timeline view: the window's runs on one time scale, in lanes by
  // actor or by flow thread, with each lane's share of the window and how
  // many batch runs were live at once. The server derives every interval
  // and number (GET /api/timeline); this draws them as plain SVG.
  import { onMount } from 'svelte';
  import type { TimelineData, TimelineFlowLane, TimelineRun, TimelineSegment } from './types';
  import { runOutcome } from './state';
  import { reloadOn } from './reload.svelte';
  import {
    LANE_PAD,
    RANGE_PRESETS,
    ROW_GAP,
    ROW_H,
    concurrencyPath,
    formatSpan,
    kindClass,
    laneHeight,
    percent,
    runEnd,
    runFailed,
    runLabel,
    runTarget,
    segmentEnd,
    segmentLabel,
    timeScale,
    timeTicks,
    timelineQuery,
    viewEnd,
    type RangePreset,
  } from './timeline';

  interface Props {
    refreshKey: number;
    now: number;
    onRun: (run: TimelineRun) => void;
    onFlow: (lane: TimelineFlowLane) => void;
  }

  let { refreshKey, now, onRun, onFlow }: Props = $props();

  let preset = $state<RangePreset>('6h');
  let customFrom = $state('');
  let customTo = $state('');
  let lanes = $state<'actor' | 'flow'>('actor');
  let data = $state<TimelineData | null>(null);
  let loading = $state(false);
  let loadError = $state<string | null>(null);
  let width = $state(960);

  const LABEL_W = 190;
  const STATS_W = 240;
  const AXIS_H = 22;
  const STRIP_H = 40;
  const GLYPH_W = 14;

  // A custom range that is not valid loads nothing. A preset's window is
  // worked out again at each load, so it moves with the clock.
  let customQuery = $derived(preset === 'custom' ? timelineQuery(preset, new Date(), customFrom, customTo) : '');

  async function load() {
    const query = timelineQuery(preset, new Date(), customFrom, customTo);
    if (loading || query === null) return;
    loading = true;
    try {
      const res = await fetch(`/api/timeline?${query}`);
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error || `Failed to load the timeline: ${res.statusText}`);
      data = body;
      loadError = null;
    } catch (e: any) {
      loadError = e.message || 'Failed to load the timeline';
    } finally {
      loading = false;
    }
  }

  reloadOn(() => `${refreshKey} ${preset} ${customQuery}`, load);

  onMount(() => {
    const interval = setInterval(() => {
      if (data?.open) load();
    }, 30000);
    return () => clearInterval(interval);
  });

  let runsById = $derived(new Map((data?.runs ?? []).map((r) => [r.id, r])));
  let end = $derived(data ? viewEnd(data, now) : 0);
  let since = $derived(data ? Date.parse(data.window.since) : 0);
  let windowSeconds = $derived(Math.max((Date.parse(data?.window.until ?? '') - since) / 1000, 0));
  let plotW = $derived(Math.max(width - LABEL_W - STATS_W, 200));
  let scale = $derived(timeScale(since, end, LABEL_W, LABEL_W + plotW));
  let ticks = $derived(timeTicks(since, end, Math.max(2, Math.floor(plotW / 90))));

  // The lanes as drawn: each with its y, height and rows.
  type Lane =
    | { kind: 'actor'; key: string; label: string; y: number; h: number; rows: string[][]; stats: string[]; title: string }
    | { kind: 'flow'; key: string; label: string; y: number; h: number; flow: TimelineFlowLane; stats: string[]; title: string };

  let drawn = $derived.by((): Lane[] => {
    if (!data) return [];
    let y = AXIS_H + STRIP_H + 8;
    const out: Lane[] = [];
    if (lanes === 'actor') {
      for (const a of data.actors) {
        const h = laneHeight(a.rows.length);
        out.push({
          kind: 'actor', key: a.actor, label: a.actor, y, h, rows: a.rows,
          stats: [`busy ${percent(a.busy, windowSeconds)}`, `${a.runs} run${a.runs === 1 ? '' : 's'}`],
          title: `${a.actor}: busy ${formatSpan(a.busy)} of ${formatSpan(windowSeconds)}, ${a.runs} runs`,
        });
        y += h;
      }
    } else {
      for (const f of data.flows) {
        const h = laneHeight(1);
        const target = runTarget(f);
        out.push({
          kind: 'flow', key: f.key, label: f.title ? `${target} ${f.title}` : target || f.key, y, h, flow: f,
          stats: [
            `agents ${percent(f.agent, windowSeconds)}`,
            `e2e ${percent(f.e2e, windowSeconds)}`,
            `owner ${percent(f.owner_wait, windowSeconds)}`,
          ],
          title: `${target}: agents ${formatSpan(f.agent)}, e2e ${formatSpan(f.e2e)}, waiting on the owner ${formatSpan(f.owner_wait)}, overhead ${formatSpan(f.overhead)}; ${f.runs} runs`,
        });
        y += h;
      }
    }
    return out;
  });

  let height = $derived(drawn.length ? drawn[drawn.length - 1].y + drawn[drawn.length - 1].h + 4 : AXIS_H + STRIP_H + 8);

  function rowY(lane: Lane, row: number): number {
    return lane.y + LANE_PAD + row * (ROW_H + ROW_GAP);
  }

  function barX(run: TimelineRun) {
    const x0 = scale.x(Date.parse(run.created));
    const x1 = scale.x(runEnd(run, data!, now));
    return { x: x0, w: Math.max(x1 - x0, 2) };
  }

  function segX(seg: TimelineSegment) {
    const x0 = scale.x(Date.parse(seg.start));
    const x1 = scale.x(segmentEnd(seg.end, seg.live, data!, now));
    return { x: x0, w: Math.max(x1 - x0, 0) };
  }

  function keyActivate(e: KeyboardEvent, fn: () => void) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      fn();
    }
  }

  function truncate(s: string, max: number): string {
    return s.length > max ? s.slice(0, max - 1) + '…' : s;
  }
</script>

<section class="card timeline-card">
  <div class="card-header">
    <div class="card-header-left">
      <h2>Timeline</h2>
      {#if data}
        <span class="muted text-sm">{data.runs.length} run{data.runs.length === 1 ? '' : 's'}</span>
      {/if}
    </div>
    <div class="card-header-right tl-controls">
      <div class="tl-toggle" role="group" aria-label="Lanes">
        <button class="btn btn-sm" class:btn-primary={lanes === 'actor'} class:btn-secondary={lanes !== 'actor'} aria-pressed={lanes === 'actor'} onclick={() => (lanes = 'actor')}>By actor</button>
        <button class="btn btn-sm" class:btn-primary={lanes === 'flow'} class:btn-secondary={lanes !== 'flow'} aria-pressed={lanes === 'flow'} onclick={() => (lanes = 'flow')}>By flow</button>
      </div>
      <label class="tl-range">
        <span class="sr-only">Range</span>
        <select bind:value={preset}>
          {#each RANGE_PRESETS as p}
            <option value={p.value}>{p.label}</option>
          {/each}
        </select>
      </label>
      {#if preset === 'custom'}
        <label class="tl-range">From <input type="datetime-local" bind:value={customFrom} /></label>
        <label class="tl-range">To <input type="datetime-local" bind:value={customTo} /></label>
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
      <div class="banner tl-incomplete" title={data.incomplete.join('\n')}>
        Some forge reads failed; their checks, stops or rounds are missing ({data.incomplete.length}).
      </div>
    {/if}

    {#if !data && loading}
      <div class="loading-state">Loading timeline...</div>
    {:else if data}
      <div class="tl-legend text-sm">
        <span><i class="tl-swatch tl-kind-implement"></i>implement</span>
        <span><i class="tl-swatch tl-kind-verify"></i>verify</span>
        <span><i class="tl-swatch tl-kind-discuss"></i>discuss</span>
        <span><i class="tl-swatch tl-kind-plan"></i>plan</span>
        <span><i class="tl-swatch tl-kind-other"></i>other</span>
        <span><i class="tl-swatch tl-swatch-hatch"></i>interactive</span>
        <span><i class="tl-swatch tl-swatch-failed"></i>failed or revoked</span>
        {#if lanes === 'flow'}
          <span><i class="tl-swatch tl-e2e"></i>e2e checks</span>
          <span><i class="tl-swatch tl-owner"></i>waiting on owner</span>
          <span><i class="tl-swatch tl-overhead-swatch"></i>flow overhead</span>
        {/if}
      </div>

      <div class="tl-plot" bind:clientWidth={width}>
        <svg width={Math.max(width, LABEL_W + STATS_W + 200)} {height} role="img" aria-label="Timeline of runs, {lanes === 'actor' ? 'by actor' : 'by flow'}">
          <defs>
            <pattern id="tl-hatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
              <rect width="6" height="6" fill="transparent" />
              <line x1="0" y1="0" x2="0" y2="6" class="tl-hatch-line" stroke-width="2.5" />
            </pattern>
          </defs>

          <!-- Axis and gridlines -->
          {#each ticks as tick (tick.t)}
            <line class="tl-grid" x1={scale.x(tick.t)} x2={scale.x(tick.t)} y1={AXIS_H - 4} y2={height} />
            <text class="tl-tick" x={scale.x(tick.t)} y={AXIS_H - 8} text-anchor="middle">{tick.label}</text>
          {/each}

          <!-- Concurrency strip -->
          <text class="tl-lane-label" x={8} y={AXIS_H + STRIP_H / 2 + 4}>Concurrent batch runs</text>
          <path class="tl-concurrency" d={concurrencyPath(data.concurrency.steps, end, data.concurrency.peak, scale, AXIS_H + 4, AXIS_H + STRIP_H)} />
          <line class="tl-baseline" x1={LABEL_W} x2={LABEL_W + plotW} y1={AXIS_H + STRIP_H} y2={AXIS_H + STRIP_H} />
          <text class="tl-stats" x={LABEL_W + plotW + 12} y={AXIS_H + STRIP_H / 2 + 4}>peak {data.concurrency.peak} · mean {data.concurrency.mean.toFixed(1)}</text>

          {#each drawn as lane, i (lane.key)}
            <g class="tl-lane">
              {#if i % 2 === 0}
                <rect class="tl-lane-bg" x={0} y={lane.y} width={LABEL_W + plotW + STATS_W} height={lane.h} />
              {/if}
              {#if lane.kind === 'flow'}
                {@const flow = lane.flow}
                {#if !flow.issue && !flow.pr}
                  <text class="tl-lane-label" x={8} y={lane.y + lane.h / 2 + 4}><title>{lane.label}</title>{truncate(lane.label, 26)}</text>
                {:else}
                <text
                  class="tl-lane-label tl-link"
                  x={8}
                  y={lane.y + lane.h / 2 + 4}
                  role="button"
                  tabindex="0"
                  aria-label="Open the timeline of {lane.label}"
                  onclick={() => onFlow(flow)}
                  onkeydown={(e) => keyActivate(e, () => onFlow(flow))}
                ><title>{lane.label}</title>{truncate(lane.label, 26)}</text>
                {/if}
                {#each flow.segments as seg, j (j)}
                  {@const sx = segX(seg)}
                  {@const y = rowY(lane, 0)}
                  {#if seg.kind === 'run'}
                    {@const run = seg.run_id ? runsById.get(seg.run_id) : undefined}
                    {#if run}
                      {@render bar(run, y)}
                    {/if}
                  {:else if seg.kind === 'e2e'}
                    {#if sx.w < 1}
                      <line class="tl-e2e-tick" x1={sx.x} x2={sx.x} y1={y - 2} y2={y + ROW_H + 2}><title>{segmentLabel(seg)}</title></line>
                    {:else}
                      <rect class="tl-e2e" class:tl-e2e-failed={seg.state === 'failure'} x={sx.x} y={y + 3} width={sx.w} height={ROW_H - 6} rx="2"><title>{segmentLabel(seg)}</title></rect>
                    {/if}
                  {:else if seg.kind === 'owner_wait'}
                    <rect class="tl-owner" x={sx.x} y={y + 2} width={sx.w} height={ROW_H - 4} rx="2"><title>{segmentLabel(seg)}</title></rect>
                    {#if sx.w > 80}
                      <text class="tl-seg-label" x={sx.x + 6} y={y + ROW_H / 2 + 4}>{truncate(seg.reason || 'waiting', Math.floor(sx.w / 7))}</text>
                    {/if}
                  {:else}
                    <line class="tl-overhead" x1={sx.x} x2={sx.x + sx.w} y1={y + ROW_H / 2} y2={y + ROW_H / 2}><title>{segmentLabel(seg)}</title></line>
                  {/if}
                {/each}
              {:else}
                <text class="tl-lane-label" x={8} y={lane.y + LANE_PAD + ROW_H / 2 + 4}><title>{lane.title}</title>{truncate(lane.label, 26)}</text>
                {#each lane.rows as row, r (r)}
                  {#each row as id (id)}
                    {@const run = runsById.get(id)}
                    {#if run}
                      {@render bar(run, rowY(lane, r))}
                    {/if}
                  {/each}
                {/each}
              {/if}
              <text class="tl-stats" x={LABEL_W + plotW + 12} y={lane.y + LANE_PAD + ROW_H / 2 + 4}><title>{lane.title}</title>{lane.stats.join(' · ')}</text>
            </g>
          {/each}
        </svg>
      </div>

      {#if drawn.length === 0}
        <div class="muted tl-empty">
          {lanes === 'actor' ? 'No runs in this window.' : 'No flows in this window.'}
        </div>
      {/if}
    {/if}
  </div>
</section>

{#snippet bar(run: TimelineRun, y: number)}
  {@const b = barX(run)}
  {@const o = runOutcome(run)}
  <g
    class="tl-bar"
    role="button"
    tabindex="0"
    aria-label="Open run {runLabel(run)}"
    onclick={() => onRun(run)}
    onkeydown={(e) => keyActivate(e, () => onRun(run))}
  >
    <title>{runLabel(run)}</title>
    <rect class="tl-run {kindClass(run.kind)}" class:tl-run-failed={runFailed(run)} class:tl-run-live={!run.ended} x={b.x} y={y} width={b.w} height={ROW_H} rx="3" />
    {#if run.interactive}
      <rect class="tl-run-hatch" x={b.x} y={y} width={b.w} height={ROW_H} rx="3" fill="url(#tl-hatch)" />
    {/if}
    {#if o && b.w >= GLYPH_W}
      <text class="tl-glyph" x={b.x + b.w - 4} y={y + ROW_H / 2 + 4} text-anchor="end">{o.icon}</text>
    {/if}
  </g>
{/snippet}

<style>
  .timeline-card {
    --tl-implement: #2a78d6;
    --tl-verify: #eb6834;
    --tl-discuss: #1baf7a;
    --tl-plan: #eda100;
    --tl-other: #6250d6;
    --tl-e2e: #64748b;
    --tl-owner: #cbd5e1;
    --tl-hatch: rgba(255, 255, 255, 0.55);
    --tl-band: rgba(125, 125, 125, 0.05);
  }

  @media (prefers-color-scheme: dark) {
    .timeline-card {
      --tl-implement: #3987e5;
      --tl-verify: #d95926;
      --tl-discuss: #199e70;
      --tl-plan: #c98500;
      --tl-other: #9085e9;
      --tl-e2e: #94a3b8;
      --tl-owner: #475569;
      --tl-hatch: rgba(0, 0, 0, 0.45);
    }
  }

  .tl-incomplete {
    background: var(--warning-bg);
    color: var(--warning);
  }

  .tl-controls {
    flex-wrap: wrap;
    gap: 8px;
  }

  .tl-toggle {
    display: inline-flex;
    gap: 2px;
  }

  .tl-range {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 13px;
  }

  .tl-legend {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 14px;
    margin-bottom: 8px;
    color: var(--text-muted);
  }

  .tl-legend span {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }

  .tl-swatch {
    display: inline-block;
    width: 14px;
    height: 10px;
    border-radius: 2px;
  }

  .tl-swatch.tl-kind-implement { background: var(--tl-implement); }
  .tl-swatch.tl-kind-verify { background: var(--tl-verify); }
  .tl-swatch.tl-kind-discuss { background: var(--tl-discuss); }
  .tl-swatch.tl-kind-plan { background: var(--tl-plan); }
  .tl-swatch.tl-kind-other { background: var(--tl-other); }
  .tl-swatch.tl-e2e { background: var(--tl-e2e); }
  .tl-swatch.tl-owner { background: var(--tl-owner); }

  .tl-swatch-hatch {
    background: repeating-linear-gradient(45deg, var(--tl-implement) 0 3px, var(--tl-hatch) 3px 5px), var(--tl-implement);
  }

  .tl-swatch-failed {
    border: 2px solid var(--text);
    background: var(--tl-implement);
  }

  .tl-overhead-swatch {
    height: 0;
    border-top: 2px dashed var(--text-muted);
  }

  .tl-plot {
    overflow-x: auto;
    width: 100%;
  }

  svg {
    display: block;
    font-size: 12px;
  }

  .tl-grid {
    stroke: var(--border);
    stroke-width: 1;
  }

  .tl-baseline {
    stroke: var(--border);
  }

  .tl-tick,
  .tl-stats {
    fill: var(--text-muted);
  }

  .tl-lane-label,
  .tl-glyph,
  .tl-seg-label {
    fill: var(--text);
  }

  .tl-link {
    cursor: pointer;
    text-decoration: underline;
    text-decoration-color: var(--border);
  }

  .tl-link:hover,
  .tl-link:focus {
    fill: var(--primary);
    outline: none;
  }

  .tl-lane-bg {
    fill: var(--tl-band);
  }

  .tl-concurrency {
    fill: var(--tl-implement);
    fill-opacity: 0.35;
    stroke: var(--tl-implement);
    stroke-width: 1.5;
  }

  .tl-bar {
    cursor: pointer;
  }

  .tl-bar:focus {
    outline: none;
  }

  .tl-bar:hover .tl-run,
  .tl-bar:focus .tl-run {
    stroke: var(--text);
    stroke-width: 2;
  }

  .tl-run {
    stroke: var(--card-bg);
    stroke-width: 1;
  }

  .tl-run.tl-kind-implement { fill: var(--tl-implement); }
  .tl-run.tl-kind-verify { fill: var(--tl-verify); }
  .tl-run.tl-kind-discuss { fill: var(--tl-discuss); }
  .tl-run.tl-kind-plan { fill: var(--tl-plan); }
  .tl-run.tl-kind-other { fill: var(--tl-other); }

  .tl-run-failed {
    stroke: var(--text);
    stroke-width: 2;
  }

  .tl-run-live {
    fill-opacity: 0.8;
  }

  .tl-run-hatch {
    pointer-events: none;
  }

  .tl-hatch-line {
    stroke: var(--tl-hatch);
  }

  .tl-glyph {
    font-size: 11px;
    font-weight: 600;
    fill: #fff;
    pointer-events: none;
  }

  .tl-e2e {
    fill: var(--tl-e2e);
  }

  .tl-e2e-failed {
    stroke: var(--text);
    stroke-width: 2;
  }

  .tl-e2e-tick {
    stroke: var(--tl-e2e);
    stroke-width: 2;
  }

  .tl-owner {
    fill: var(--tl-owner);
  }

  .tl-seg-label {
    font-size: 11px;
    pointer-events: none;
  }

  .tl-overhead {
    stroke: var(--text-muted);
    stroke-width: 2;
    stroke-dasharray: 3 3;
  }

  .tl-empty {
    margin-top: 8px;
  }
</style>
