<script lang="ts">
  // A flow's post-implement checks while they run (§4.2): the PR head they
  // check, their commands, elapsed time against the flow's check timeout,
  // and the live tail of their log, as a run's card shows its own.
  import type { LiveCheckInfo } from './types';
  import { formatElapsed } from './format';
  import IssueReference from './IssueReference.svelte';
  import LiveLogTail from './LiveLogTail.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    check: LiveCheckInfo;
    now: number;
  }

  let { check, now }: Props = $props();
</script>

<div class="run-card check-card">
  <div class="run-card-header">
    <div class="run-card-title">
      {#if check.issue && check.issue_url}
        <IssueReference
          number={check.issue}
          href={check.issue_url}
          label={`#${check.issue}`}
          title={check.issue_title}
          showTitle
        />
      {/if}
      {#if check.pr && check.pr_url}
        <TargetLink href={check.pr_url} label={`!${check.pr}`} />
      {/if}
      <span class="badge badge-neutral">post-implement checks</span>
      <span class="badge instance-badge" title={check.sha}>at {check.sha.slice(0, 10)}</span>
      {#each check.commands as command (command)}
        <code class="check-command">{command}</code>
      {/each}
    </div>
    <div class="run-card-actions">
      <span class="run-timer">{formatElapsed(check.created, now)}{check.timeout ? ` / ${check.timeout}` : ''}</span>
    </div>
  </div>

  <LiveLogTail runId={check.id} lines={check.log_tail} />
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
    min-width: 0;
  }

  .check-command {
    font-size: 12px;
    overflow-wrap: anywhere;
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
