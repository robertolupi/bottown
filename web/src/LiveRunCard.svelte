<script lang="ts">
  // One live run: its target, kind, actor, model and mode, elapsed time,
  // a stop button, the live log tail (batch runs) and policy denials.
  import type { LiveRunInfo } from './types';
  import { parseIssueNumber } from './state';
  import { formatElapsed } from './format';
  import DenialList from './DenialList.svelte';
  import IssueReference from './IssueReference.svelte';
  import LiveLogTail from './LiveLogTail.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    run: LiveRunInfo;
    now: number;
    stopLabel: string;
    onStop: (runId: string) => void;
  }

  let { run, now, stopLabel, onStop }: Props = $props();
</script>

<div class="run-card">
  <div class="run-card-header">
    <div class="run-card-title">
      {#if run.issue || run.flow?.issue || run.target.startsWith('#')}
        <IssueReference
          number={run.issue || run.flow?.issue || parseIssueNumber(run.target) || 0}
          href={run.target_url}
          label={run.target}
          title={run.issue_title}
          showTitle
        />
      {:else}
        <TargetLink href={run.target_url} label={run.target} />
      {/if}
      <span class="badge badge-neutral">{run.kind}</span>
      <span class="badge repo-badge">{run.actor} ({run.family})</span>
      {#if run.spec}<span class="badge instance-badge" title="model">model {run.spec}</span>{/if}
      <span class="badge instance-badge">{run.interactive ? 'interactive' : 'batch'}</span>
    </div>
    <div class="run-card-actions">
      <span class="run-timer">{formatElapsed(run.created, now)} / {run.timeout}</span>
      <button class="btn btn-sm btn-danger" onclick={() => onStop(run.id)}>
        {stopLabel}
      </button>
    </div>
  </div>

  {#if !run.interactive}
    <LiveLogTail runId={run.id} lines={run.log_tail} />
  {/if}
  <DenialList denials={run.denials} />
</div>

<style>
  .run-card {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: rgba(125, 125, 125, 0.03);
    overflow: hidden;
  }

  .run-card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    background: rgba(125, 125, 125, 0.06);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
    gap: 12px;
  }

  .run-card-title {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .run-card-actions {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .run-timer {
    font-family: monospace;
    font-size: 13px;
    color: var(--text-muted);
  }
</style>
