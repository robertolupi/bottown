<script lang="ts">
  // Repository health (§4.3): agent instructions, protected main, the
  // repository settings, versions, harness commands and actor worktrees.
  import type { HealthChecklist } from './types';

  let { health }: { health: HealthChecklist } = $props();
</script>

<section class="card">
  <div class="card-header">
    <h2>Health Checklist</h2>
  </div>

  <div class="card-body">
    <ul class="checklist">
      <li class="check-item">
        <span class="check-icon {health.agent_instructions.found ? 'icon-success' : 'icon-error'}">
          {health.agent_instructions.found ? '✓' : '✕'}
        </span>
        <div class="check-content">
          <strong>Agent Instructions</strong>
          <span class="muted text-sm">
            {health.agent_instructions.found
              ? health.agent_instructions.filename
              : 'Missing AGENTS.md / README.md'}
          </span>
        </div>
      </li>

      <li class="check-item">
        <span class="check-icon {health.protected_main.protected ? 'icon-success' : 'icon-error'}">
          {health.protected_main.protected ? '✓' : '✕'}
        </span>
        <div class="check-content">
          <strong>Protected Main Branch</strong>
          <span class="muted text-sm">
            {health.protected_main.protected
              ? 'Forgejo main is protected'
              : health.protected_main.error || 'Main branch unprotected'}
          </span>
        </div>
      </li>

      <li class="check-item">
        <span class="check-icon {health.repo_settings.error ? 'icon-error' : health.repo_settings.found ? 'icon-success' : 'icon-warning'}">
          {health.repo_settings.error ? '✗' : health.repo_settings.found ? '✓' : '!'}
        </span>
        <div class="check-content">
          <strong>Repository Settings</strong>
          <span class="muted text-sm">
            {#if health.repo_settings.error}
              {health.repo_settings.error}
            {:else if health.repo_settings.found}
              <code>{health.repo_settings.file}</code> on {health.repo_settings.branch}:
              {health.repo_settings.after_pull.length} after-pull step{health.repo_settings.after_pull.length === 1 ? '' : 's'}{#if health.repo_settings.milestones_dir}; milestone plans in <code>{health.repo_settings.milestones_dir}</code>{/if}
            {:else}
              No <code>{health.repo_settings.file}</code> on {health.repo_settings.branch}; using the defaults
            {/if}
          </span>
        </div>
      </li>

      <li class="check-item">
        <span class="check-icon {health.version.matches ? 'icon-success' : 'icon-warning'}">
          {health.version.matches ? '✓' : '!'}
        </span>
        <div class="check-content">
          <strong>CLI & Server Versions</strong>
          <span class="muted text-sm">
            CLI {health.version.cli_version} · Server {health.version.server_version}
          </span>
        </div>
      </li>

      <li class="check-item">
        <span class="check-icon icon-success">✓</span>
        <div class="check-content">
          <strong>Harness Commands</strong>
          <div class="harness-badges">
            {#each health.harness_commands as hc}
              <span class="badge {hc.current ? 'badge-success' : 'badge-warning'}">
                {hc.harness}: {hc.current ? 'current' : 'outdated'}
              </span>
            {/each}
          </div>
        </div>
      </li>
    </ul>

    {#if health.worktrees.length > 0}
      <div class="worktrees-section">
        <h3>Actor Worktrees</h3>
        <div class="table-container">
          <table class="data-table">
            <thead>
              <tr>
                <th>Actor</th>
                <th>HEAD</th>
                <th>Status</th>
                <th>Run</th>
                <th>Path</th>
              </tr>
            </thead>
            <tbody>
              {#each health.worktrees as wt}
                <tr>
                  <td>
                    <strong>{wt.actor}</strong>
                    {#if wt.ephemeral}
                      <span class="badge" title="Worktree of a launched run under .bottown/runs">{wt.kind} {wt.target}</span>
                    {/if}
                  </td>
                  <td class="font-mono text-sm">{wt.head}</td>
                  <td>
                    <span class="badge {wt.is_dirty ? 'badge-warning' : 'badge-success'}">
                      {wt.is_dirty ? 'dirty' : 'clean'}
                    </span>
                  </td>
                  <td>
                    {#if wt.active_run}
                      <span class="badge badge-accent">live: {wt.active_run}</span>
                    {:else if wt.last_run}
                      <span class="text-sm muted">{wt.last_run}</span>
                    {:else}
                      <span class="muted">-</span>
                    {/if}
                  </td>
                  <td class="text-sm muted"><code>{wt.path}</code></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {/if}
  </div>
</section>

<style>
  .font-mono {
    font-family: monospace;
  }

  .checklist {
    list-style: none;
    padding: 0;
    margin: 0 0 24px;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 16px;
  }

  .check-item {
    display: flex;
    align-items: flex-start;
    gap: 10px;
  }

  .check-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    font-size: 12px;
    font-weight: bold;
    flex-shrink: 0;
  }

  .icon-success { background: var(--success-bg); color: var(--success); }
  .icon-warning { background: var(--warning-bg); color: var(--warning); }
  .icon-error { background: var(--error-bg); color: var(--error); }

  .check-content {
    display: flex;
    flex-direction: column;
  }

  .harness-badges {
    display: flex;
    gap: 6px;
    margin-top: 4px;
    flex-wrap: wrap;
  }

  .worktrees-section h3 {
    font-size: 15px;
    margin: 0 0 12px;
  }

  .table-container {
    overflow-x: auto;
  }

  .data-table {
    width: 100%;
    border-collapse: separate;
    border-spacing: 0;
    font-size: 13px;
  }

  .data-table th, .data-table td {
    padding: 8px 12px;
    text-align: left;
    border-bottom: 1px solid var(--border);
  }

  .data-table th {
    color: var(--text-muted);
    font-weight: 500;
  }
</style>
