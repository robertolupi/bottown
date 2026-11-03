<script lang="ts">
  // An expanded flow's runs, as a table whose rows expand to each run's
  // log and policy denials.
  import type { HistoryRunInfo, RunLogState } from './types';
  import { formatTime, formatUsage } from './format';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import RunLogRow from './RunLogRow.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    runs: HistoryRunInfo[];
    expandedLogs: Record<string, RunLogState>;
    onToggleLog: (runId: string) => void;
  }

  let { runs, expandedLogs, onToggleLog }: Props = $props();
</script>

<div class="flow-runs-section">
  <div class="flow-section-header">
    <strong>Constituent Runs ({runs.length})</strong>
  </div>
  <table class="data-table constituent-runs-table">
    <thead>
      <tr>
        <th>Run ID</th>
        <th>Target</th>
        <th>Kind</th>
        <th>Actor</th>
        <th>Status</th>
        <th>Outcome</th>
        <th>Duration</th>
        <th>Usage</th>
        <th>Summary</th>
        <th>When</th>
        <th>Details</th>
      </tr>
    </thead>
    <tbody>
      {#each runs as r (r.id)}
        <tr>
          <td class="font-mono text-sm">{r.id}</td>
          <td>
            <TargetLink href={r.target_url} label={r.target} />
          </td>
          <td><span class="badge badge-neutral">{r.kind}</span></td>
          <td><span class="badge repo-badge">{r.family ? `${r.actor} (${r.family})` : r.actor}</span>{#if r.spec} <span class="badge instance-badge" title="model">model {r.spec}</span>{/if}</td>
          <td>
            <StatusBadge status={r.status} />
            {#if r.denials && r.denials.length > 0}
              <span class="badge badge-error" title="Operations the server's policy refused this run; expand the log to see them">
                {r.denials.length} denied
              </span>
            {/if}
          </td>
          <td><OutcomeBadge run={r} /></td>
          <td class="muted">{r.duration || '—'}</td>
          <td class="muted text-sm">{formatUsage(r.usage)}</td>
          <td class="summary-cell" title={r.summary}>{r.summary || '—'}</td>
          <td class="muted text-sm">{formatTime(r.created)}</td>
          <td>
            <button
              class="btn btn-sm btn-secondary"
              onclick={() => onToggleLog(r.id)}
              title="Expand parsed run log and policy denials"
            >
              {expandedLogs[r.id] ? 'Hide Log' : 'Log'}
            </button>
          </td>
        </tr>
        {#if expandedLogs[r.id]}
          <RunLogRow
            title={`Run Log (${r.id} · ${r.kind} · ${r.actor})`}
            colspan={11}
            denials={r.denials}
            logState={expandedLogs[r.id]}
          />
        {/if}
      {/each}
    </tbody>
  </table>
</div>

<style>
  .flow-section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 12px;
    flex-wrap: wrap;
    gap: 8px;
  }

  .font-mono {
    font-family: monospace;
  }

  .summary-cell {
    max-width: max(280px, 25vw);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* Its cells are styled by the history table around it (RunHistory). */
  .data-table {
    width: 100%;
    border-collapse: separate;
    border-spacing: 0;
    font-size: 13px;
  }

  .constituent-runs-table {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
  }
</style>
