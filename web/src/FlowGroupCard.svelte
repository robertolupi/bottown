<script lang="ts">
  // An active flow: its issue, round and step, a stop button, and its live
  // runs and post-implement checks, or a note while the supervisor is
  // between rounds.
  import type { FlowGroup } from './state';
  import IssueReference from './IssueReference.svelte';
  import LiveCheckCard from './LiveCheckCard.svelte';
  import LiveRunCard from './LiveRunCard.svelte';

  interface Props {
    group: FlowGroup;
    issuesLink: string;
    now: number;
    onStop: (runId: string) => void;
  }

  let { group: fg, issuesLink, now, onStop }: Props = $props();
</script>

<div class="flow-group-card">
  <div class="flow-group-header">
    <div class="flow-group-title">
      <span class="badge badge-accent flow-badge">
        Flow for
        <IssueReference
          number={fg.flow.issue}
          href={fg.runs[0]?.issue_url || `${issuesLink}/${fg.flow.issue}`}
          label={`#${fg.flow.issue}`}
          title={fg.runs.find((run) => run.issue_title)?.issue_title}
          showTitle
        />
      </span>
      <strong>Round {fg.flow.round}/{fg.flow.max_rounds}</strong>
      <span class="muted text-sm">— {fg.flow.step}</span>
    </div>
    <div class="flow-group-actions">
      <button class="btn btn-sm btn-danger" onclick={() => onStop(`flow-${fg.flow.issue}`)}>
        Stop Flow
      </button>
    </div>
  </div>

  <div class="flow-group-runs">
    {#if fg.runs.length === 0 && fg.checks.length === 0}
      <div class="flow-waiting-state">
        <span class="muted text-sm">Waiting between rounds… supervisor will start the next actor.</span>
      </div>
    {:else}
      {#each fg.runs as run (run.id)}
        <LiveRunCard {run} {now} stopLabel="Stop Run" {onStop} />
      {/each}
      {#each fg.checks as check (check.id)}
        <LiveCheckCard {check} {now} />
      {/each}
    {/if}
  </div>
</div>

<style>
  .flow-waiting-state {
    padding: 10px 12px;
    background: rgba(125, 125, 125, 0.04);
    border-radius: 6px;
    font-style: italic;
  }

  .flow-group-card {
    border: 1px solid var(--accent);
    border-radius: 8px;
    background: rgba(125, 125, 125, 0.02);
    overflow: hidden;
  }

  .flow-group-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 14px;
    background: rgba(125, 125, 125, 0.06);
    border-bottom: 1px solid var(--border);
  }

  .flow-group-title {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .flow-group-runs {
    padding: 12px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .flow-badge {
    font-weight: 600;
  }
</style>
