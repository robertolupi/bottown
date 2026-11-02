<script lang="ts">
  // Live runs and flow actions (§4.2, §5): active flows with their runs,
  // then standalone runs, and the buttons that start flows and runs or
  // clean up.
  import type { ActiveFlowInfo, LiveRunInfo } from './types';
  import { groupLiveFlows, standaloneLiveRuns } from './state';
  import FlowGroupCard from './FlowGroupCard.svelte';
  import LiveRunCard from './LiveRunCard.svelte';

  interface Props {
    liveRuns: LiveRunInfo[] | undefined;
    activeFlows: ActiveFlowInfo[] | undefined;
    issuesLink: string;
    now: number;
    onStartFlow: () => void;
    onStartRun: () => void;
    onGC: () => void;
    onStop: (runId: string) => void;
  }

  let { liveRuns, activeFlows, issuesLink, now, onStartFlow, onStartRun, onGC, onStop }: Props = $props();

  let flowGroups = $derived(groupLiveFlows(activeFlows, liveRuns));
  let standaloneRuns = $derived(standaloneLiveRuns(liveRuns));
  let activeCount = $derived((liveRuns?.length || 0) + (activeFlows?.length || 0));
</script>

<section class="card full-width">
  <div class="card-header">
    <div class="card-header-left">
      <h2>Live Runs</h2>
      {#if activeCount > 0}
        <span class="badge badge-accent">{activeCount} active</span>
      {:else}
        <span class="badge badge-neutral">0 active</span>
      {/if}
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
    {#if (!liveRuns || liveRuns.length === 0) && (!activeFlows || activeFlows.length === 0)}
      <div class="inbox-zero">
        <span class="inbox-zero-icon">💤</span>
        <span>No runs or flows currently active in this repository.</span>
      </div>
    {:else}
      <div class="live-runs-list">
        <!-- Active Flows -->
        {#each flowGroups as fg (fg.flow.issue)}
          <FlowGroupCard group={fg} {issuesLink} {now} {onStop} />
        {/each}

        <!-- Standalone Runs -->
        {#each standaloneRuns as run (run.id)}
          <LiveRunCard {run} {now} stopLabel="Stop" {onStop} />
        {/each}
      </div>
    {/if}
  </div>
</section>

<style>
  .live-runs-list {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
</style>
