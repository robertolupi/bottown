<script lang="ts">
  // Upstream and forge status (§4.3): the checkout against the forge and
  // origin, with pull, push and after-pull actions when they apply.
  import type { UIStatusResponse } from './types';

  interface Props {
    status: UIStatusResponse;
    actionLoading: boolean;
    onPull: () => void;
    onPush: () => void;
    onAfterPull: () => void;
  }

  let { status, actionLoading, onPull, onPush, onAfterPull }: Props = $props();
</script>

<section class="card">
  <div class="card-header">
    <h2>Upstream & Forge</h2>
    {#if status.upstream.has_upstream}
      <span class="badge badge-success">Upstream linked</span>
    {:else}
      <span class="badge badge-neutral">No upstream</span>
    {/if}
  </div>

  <div class="card-body">
    <div class="metric-row">
      <span class="metric-label">Local Default Branch</span>
      <span class="metric-value font-mono">{status.forge.default_branch} ({status.forge.checkout_sha})</span>
    </div>

    <div class="metric-row">
      <span class="metric-label">Forge ({status.forge.remote})</span>
      <span class="metric-value font-mono">
        <a href={status.links.commits} target="_blank" rel="noreferrer">{status.forge.forge_sha}</a>
        {#if status.forge.behind_count > 0}
          <span class="badge badge-warning">{status.forge.behind_count} behind</span>
        {/if}
        {#if status.forge.ahead_count > 0}
          <span class="badge badge-accent">{status.forge.ahead_count} ahead</span>
        {/if}
      </span>
    </div>

    {#if status.upstream.has_upstream}
      <div class="metric-row">
        <span class="metric-label">Origin Head</span>
        <span class="metric-value font-mono">
          {status.upstream.head_sha || 'unknown'}
          {#if status.upstream.ahead_count > 0}
            <span class="badge badge-accent">{status.upstream.ahead_count} ahead</span>
          {/if}
          {#if status.upstream.behind_count > 0}
            <span class="badge badge-warning">{status.upstream.behind_count} behind</span>
          {/if}
        </span>
      </div>
    {/if}

    <!-- Checkout Behind: Pull Action -->
    {#if status.forge.behind_count > 0}
      <div class="action-banner">
        <div>
          <strong>Checkout is behind forge main.</strong>
          <p class="muted text-sm">Pull fast-forward changes merged in Forgejo.</p>
        </div>
        <button class="btn btn-primary" onclick={onPull} disabled={actionLoading}>
          {actionLoading ? 'Pulling...' : 'Pull'}
        </button>
      </div>
    {/if}

    <!-- Ahead of origin: Push Action -->
    {#if status.upstream.has_upstream && status.upstream.ahead_count > 0}
      <div class="action-banner">
        <div>
          <strong>Local main is ahead of origin.</strong>
          <p class="muted text-sm">{status.upstream.ahead_count} commit(s) ready to push.</p>
        </div>
        <button class="btn btn-accent" onclick={onPush} disabled={actionLoading}>
          Push to Origin...
        </button>
      </div>
    {/if}

    {#if status.health.repo_settings.after_pull.length > 0}
      <div class="commits-list">
        <h3>After-pull steps (<code>{status.health.repo_settings.file}</code> on <code>{status.health.repo_settings.branch}</code>):</h3>
        <ul>
          {#each status.health.repo_settings.after_pull as step}
            <li class="commit-item"><code>{step}</code></li>
          {/each}
        </ul>
      </div>
      <div class="actions-row">
        <button class="btn btn-secondary" onclick={onAfterPull} disabled={actionLoading}>
          Run after-pull steps…
        </button>
      </div>
    {/if}
  </div>
</section>

<style>
  .metric-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 0;
    border-bottom: 1px solid var(--border);
    font-size: 14px;
  }

  .font-mono {
    font-family: monospace;
  }

  .action-banner {
    margin-top: 16px;
    padding: 12px 16px;
    background: rgba(125, 125, 125, 0.05);
    border: 1px solid var(--border);
    border-radius: 8px;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
</style>
