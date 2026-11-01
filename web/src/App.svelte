<script lang="ts">
  import { onMount } from 'svelte';
  import type { UIStatusResponse, ActionResult } from './types';

  let status = $state<UIStatusResponse | null>(null);
  let loading = $state<boolean>(true);
  let refreshing = $state<boolean>(false);
  let errorMessage = $state<string | null>(null);
  let actionMessage = $state<{ type: 'success' | 'error'; text: string } | null>(null);

  // Push confirmation modal
  let showPushModal = $state<boolean>(false);
  let actionLoading = $state<boolean>(false);

  async function loadStatus(isRefresh = false) {
    if (isRefresh) refreshing = true;
    else loading = true;
    errorMessage = null;

    try {
      const res = await fetch('/api/status');
      if (!res.ok) {
        throw new Error(`Failed to load status: ${res.statusText}`);
      }
      status = await res.json();
    } catch (e: any) {
      errorMessage = e.message || 'Unknown error loading status';
    } finally {
      loading = false;
      refreshing = false;
    }
  }

  async function handlePull() {
    if (actionLoading) return;
    actionLoading = true;
    actionMessage = null;

    try {
      const res = await fetch('/api/actions/pull', { method: 'POST' });
      const data: ActionResult = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(data.error || data.output || 'Pull failed');
      }
      actionMessage = { type: 'success', text: `Pull succeeded: ${data.output || 'up to date'}` };
      await loadStatus(true);
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
    } finally {
      actionLoading = false;
    }
  }

  async function handleConfirmPush() {
    if (actionLoading) return;
    actionLoading = true;
    actionMessage = null;

    try {
      const res = await fetch('/api/actions/push', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ head: status?.upstream?.head_sha }),
      });
      const data: ActionResult = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(data.error || data.output || 'Push failed');
      }
      actionMessage = { type: 'success', text: `Push succeeded: ${data.output || 'pushed to origin'}` };
      showPushModal = false;
      await loadStatus(true);
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
    } finally {
      actionLoading = false;
    }
  }

  function formatTime(isoStr?: string): string {
    if (!isoStr) return '';
    try {
      const d = new Date(isoStr);
      return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch {
      return isoStr;
    }
  }

  onMount(() => {
    loadStatus();
  });
</script>

<main class="container">
  <header class="header">
    <div class="header-left">
      <h1 class="logo">bottown</h1>
      {#if status}
        <span class="badge repo-badge">{status.repo}</span>
        <span class="badge instance-badge">{status.instance}</span>
      {/if}
    </div>
    <div class="header-right">
      {#if status?.upstream?.fetched_at}
        <span class="muted text-sm">origin as of {formatTime(status.upstream.fetched_at)}</span>
      {/if}
      <button class="btn btn-secondary" onclick={() => loadStatus(true)} disabled={refreshing}>
        {#if refreshing}Refreshing...{:else}Refresh{/if}
      </button>
      {#if status?.links?.repo}
        <a class="btn btn-primary" href={status.links.repo} target="_blank" rel="noreferrer">
          Forgejo ↗
        </a>
      {/if}
    </div>
  </header>

  {#if actionMessage}
    <div class="banner banner-{actionMessage.type}">
      <span>{actionMessage.text}</span>
      <button class="close-btn" onclick={() => (actionMessage = null)}>✕</button>
    </div>
  {/if}

  {#if errorMessage}
    <div class="banner banner-error">
      <span>{errorMessage}</span>
      <button class="btn btn-sm btn-secondary" onclick={() => loadStatus()}>Retry</button>
    </div>
  {/if}

  {#if loading && !status}
    <div class="loading-state">Loading status...</div>
  {:else if status}
    <div class="grid">
      <!-- 1. Upstream Section -->
      <section class="card">
        <div class="card-header">
          <h2>Upstream</h2>
          {#if !status.upstream.has_upstream}
            <span class="badge badge-neutral">no upstream</span>
          {:else if status.upstream.ahead_count > 0}
            <span class="badge badge-warning">{status.upstream.ahead_count} unpushed commit{status.upstream.ahead_count > 1 ? 's' : ''}</span>
          {:else}
            <span class="badge badge-success">up to date</span>
          {/if}
        </div>

        <div class="card-body">
          {#if !status.upstream.has_upstream}
            <p class="muted">No external <code>origin</code> remote configured for this checkout.</p>
          {:else}
            <div class="info-row">
              <span class="label">Remote:</span>
              <span class="value"><code>{status.upstream.origin_url}</code></span>
            </div>
            <div class="info-row">
              <span class="label">Status:</span>
              <span class="value">
                {status.upstream.ahead_count} ahead, {status.upstream.behind_count} behind <code>origin/main</code>
              </span>
            </div>

            {#if status.upstream.ahead_count > 0}
              <div class="commits-list">
                <h3>Unpushed commits to origin:</h3>
                <ul>
                  {#each status.upstream.unpushed_commits as c}
                    <li class="commit-item">
                      <span class="commit-sha">{c.short_sha}</span>
                      <span class="commit-subject">{c.subject}</span>
                      {#if c.pr_number}
                        <a
                          class="badge pr-badge"
                          href="{status.forge.forge_url}/{status.forge.repo}/pulls/{c.pr_number}"
                          target="_blank"
                          rel="noreferrer"
                        >
                          !{c.pr_number}
                        </a>
                      {/if}
                      <span class="commit-meta">{c.author} · {c.date}</span>
                    </li>
                  {/each}
                </ul>
              </div>

              <div class="actions-row">
                <button
                  class="btn btn-accent"
                  onclick={() => (showPushModal = true)}
                  disabled={actionLoading}
                >
                  Push main to origin
                </button>
              </div>
            {/if}
          {/if}
        </div>
      </section>

      <!-- 2. Checkout vs Forge Section -->
      <section class="card">
        <div class="card-header">
          <h2>Forge & Checkout</h2>
          {#if status.forge.behind_count > 0}
            <span class="badge badge-warning">{status.forge.behind_count} commit{status.forge.behind_count > 1 ? 's' : ''} behind forge</span>
          {:else}
            <span class="badge badge-success">in sync with forge</span>
          {/if}
        </div>

        <div class="card-body">
          <div class="info-row">
            <span class="label">Default Branch:</span>
            <span class="value"><code>{status.forge.default_branch}</code></span>
          </div>
          <div class="info-row">
            <span class="label">Checkout HEAD:</span>
            <span class="value"><code>{status.forge.checkout_sha}</code></span>
          </div>
          <div class="info-row">
            <span class="label">Forge HEAD:</span>
            <span class="value"><code>{status.forge.forge_sha}</code></span>
          </div>

          {#if status.forge.behind_count > 0}
            <p class="warning-text">
              The local checkout is behind the forge's <code>main</code> (e.g. PR was merged on the forge).
            </p>
            <div class="actions-row">
              <button class="btn btn-primary" onclick={handlePull} disabled={actionLoading}>
                {actionLoading ? 'Pulling...' : 'Pull (git pull --ff-only bottown main)'}
              </button>
            </div>
          {/if}
        </div>
      </section>

      <!-- 3. Health Checklist -->
      <section class="card full-width">
        <div class="card-header">
          <h2>Health Checklist</h2>
        </div>

        <div class="card-body">
          <ul class="checklist">
            <li class="check-item">
              <span class="check-icon {status.health.agent_instructions.found ? 'icon-success' : 'icon-error'}">
                {status.health.agent_instructions.found ? '✓' : '✗'}
              </span>
              <div class="check-content">
                <strong>Agent Instructions</strong>
                <span class="muted text-sm">
                  {status.health.agent_instructions.found
                    ? `Found ${status.health.agent_instructions.filename} on main`
                    : 'Missing AGENTS.md or README.md on main'}
                </span>
              </div>
            </li>

            <li class="check-item">
              <span class="check-icon {status.health.protected_main.protected ? 'icon-success' : 'icon-error'}">
                {status.health.protected_main.protected ? '✓' : '✗'}
              </span>
              <div class="check-content">
                <strong>Protected <code>main</code></strong>
                <span class="muted text-sm">
                  {status.health.protected_main.protected
                    ? "The forge's main branch is protected against direct pushes"
                    : (status.health.protected_main.error || "main branch is not protected on the forge")}
                </span>
              </div>
            </li>

            <li class="check-item">
              <span class="check-icon {status.health.version.matches ? 'icon-success' : 'icon-warning'}">
                {status.health.version.matches ? '✓' : '!'}
              </span>
              <div class="check-content">
                <strong>CLI and Server Versions</strong>
                <span class="muted text-sm">
                  CLI: <code>{status.health.version.cli_version}</code> · Server: <code>{status.health.version.server_version}</code>
                  {status.health.version.matches ? ' (versions match)' : ' (versions differ)'}
                </span>
              </div>
            </li>

            <li class="check-item">
              <span class="check-icon {status.health.harness_commands.every(h => h.current) ? 'icon-success' : 'icon-warning'}">
                {status.health.harness_commands.every(h => h.current) ? '✓' : '!'}
              </span>
              <div class="check-content">
                <strong>Harness Slash Commands</strong>
                <div class="harness-badges">
                  {#each status.health.harness_commands as h}
                    <span class="badge {h.current ? 'badge-success' : h.installed ? 'badge-warning' : 'badge-neutral'}">
                      {h.harness}: {h.current ? 'current' : h.installed ? 'outdated' : 'not installed'}
                    </span>
                  {/each}
                </div>
              </div>
            </li>
          </ul>

          <div class="worktrees-section">
            <h3>Actor Worktrees</h3>
            {#if status.health.worktrees.length === 0}
              <p class="muted">No actor worktrees registered.</p>
            {:else}
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
                    {#each status.health.worktrees as wt}
                      <tr>
                        <td><strong>{wt.actor}</strong></td>
                        <td><code>{wt.head}</code></td>
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
            {/if}
          </div>
        </div>
      </section>
    </div>
  {/if}
</main>

{#if showPushModal}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={() => (showPushModal = false)}
    onkeydown={(e) => { if (e.key === 'Escape') showPushModal = false; }}
  >
    <div
      class="modal-card"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
    >
      <h3>Confirm Push to Origin</h3>
      <p>The following commits will be pushed to <code>origin/main</code>:</p>
      <ul class="modal-commits-list">
        {#each status?.upstream?.unpushed_commits || [] as c}
          <li>
            <code>{c.short_sha}</code> {c.subject}
            {#if c.pr_number}<span class="badge pr-badge">!{c.pr_number}</span>{/if}
          </li>
        {/each}
      </ul>
      <div class="modal-actions">
        <button class="btn btn-secondary" onclick={() => (showPushModal = false)} disabled={actionLoading}>
          Cancel
        </button>
        <button class="btn btn-accent" onclick={handleConfirmPush} disabled={actionLoading}>
          {actionLoading ? 'Pushing...' : 'Confirm & Push to Origin'}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  :root {
    --bg: #f8fafc;
    --card-bg: #ffffff;
    --border: #e2e8f0;
    --text: #0f172a;
    --text-muted: #64748b;
    --primary: #0284c7;
    --primary-hover: #0369a1;
    --accent: #2563eb;
    --accent-hover: #1d4ed8;
    --success: #16a34a;
    --success-bg: #dcfce7;
    --warning: #d97706;
    --warning-bg: #fef3c7;
    --error: #dc2626;
    --error-bg: #fee2e2;
  }

  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0b0f17;
      --card-bg: #151d2c;
      --border: #26334a;
      --text: #f1f5f9;
      --text-muted: #94a3b8;
      --primary: #38bdf8;
      --primary-hover: #7dd3fc;
      --accent: #3b82f6;
      --accent-hover: #60a5fa;
      --success: #22c55e;
      --success-bg: #052e16;
      --warning: #f59e0b;
      --warning-bg: #451a03;
      --error: #ef4444;
      --error-bg: #450a0a;
    }
  }

  :global(body) {
    margin: 0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    background-color: var(--bg);
    color: var(--text);
    line-height: 1.5;
  }

  :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.875em;
    padding: 0.15em 0.35em;
    background: rgba(125, 125, 125, 0.1);
    border-radius: 4px;
  }

  .container {
    max-width: 1080px;
    margin: 0 auto;
    padding: 24px 16px 64px;
  }

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 24px;
    padding-bottom: 16px;
    border-bottom: 1px solid var(--border);
  }

  .header-left, .header-right {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .logo {
    margin: 0;
    font-size: 24px;
    font-weight: 700;
    letter-spacing: -0.5px;
  }

  .badge {
    display: inline-flex;
    align-items: center;
    padding: 2px 8px;
    border-radius: 9999px;
    font-size: 12px;
    font-weight: 500;
  }

  .repo-badge {
    background: rgba(56, 189, 248, 0.15);
    color: var(--primary);
  }

  .instance-badge {
    background: rgba(125, 125, 125, 0.15);
    color: var(--text-muted);
  }

  .badge-success { background: var(--success-bg); color: var(--success); }
  .badge-warning { background: var(--warning-bg); color: var(--warning); }
  .badge-error { background: var(--error-bg); color: var(--error); }
  .badge-neutral { background: rgba(125, 125, 125, 0.15); color: var(--text-muted); }
  .badge-accent { background: rgba(37, 99, 235, 0.15); color: var(--accent); }

  .pr-badge {
    background: rgba(99, 102, 241, 0.15);
    color: #818cf8;
    text-decoration: none;
  }
  .pr-badge:hover { text-decoration: underline; }

  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 6px 14px;
    border-radius: 6px;
    font-size: 14px;
    font-weight: 500;
    cursor: pointer;
    text-decoration: none;
    border: 1px solid transparent;
    transition: all 0.15s ease;
  }

  .btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-primary { background: var(--primary); color: #fff; }
  .btn-primary:hover:not(:disabled) { background: var(--primary-hover); }

  .btn-secondary {
    background: var(--card-bg);
    border-color: var(--border);
    color: var(--text);
  }
  .btn-secondary:hover:not(:disabled) { border-color: var(--text-muted); }

  .btn-accent { background: var(--accent); color: #fff; }
  .btn-accent:hover:not(:disabled) { background: var(--accent-hover); }

  .btn-sm { padding: 4px 8px; font-size: 12px; }

  .banner {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    border-radius: 8px;
    margin-bottom: 20px;
    font-size: 14px;
  }

  .banner-success { background: var(--success-bg); color: var(--success); }
  .banner-error { background: var(--error-bg); color: var(--error); }

  .close-btn {
    background: none;
    border: none;
    color: inherit;
    cursor: pointer;
    font-size: 16px;
  }

  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 20px;
  }

  .card {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 10px;
    overflow: hidden;
  }

  .full-width {
    grid-column: 1 / -1;
  }

  .card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 14px 18px;
    border-bottom: 1px solid var(--border);
  }

  .card-header h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
  }

  .card-body {
    padding: 16px 18px;
  }

  .info-row {
    display: flex;
    margin-bottom: 10px;
    font-size: 14px;
  }

  .info-row .label {
    width: 140px;
    color: var(--text-muted);
  }

  .info-row .value {
    flex: 1;
  }

  .commits-list {
    margin-top: 16px;
    border-top: 1px dashed var(--border);
    padding-top: 12px;
  }

  .commits-list h3 {
    font-size: 13px;
    margin: 0 0 8px;
    color: var(--text-muted);
  }

  .commits-list ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }

  .commit-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 0;
    font-size: 13px;
    border-bottom: 1px solid rgba(125, 125, 125, 0.08);
  }

  .commit-sha {
    font-family: ui-monospace, monospace;
    color: var(--primary);
  }

  .commit-subject {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .commit-meta {
    color: var(--text-muted);
    font-size: 12px;
  }

  .actions-row {
    margin-top: 16px;
    display: flex;
    gap: 12px;
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
    border-collapse: collapse;
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

  .modal-backdrop {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(0, 0, 0, 0.6);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .modal-card {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    max-width: 520px;
    width: 90%;
    padding: 24px;
    box-shadow: 0 10px 25px rgba(0, 0, 0, 0.2);
  }

  .modal-card h3 {
    margin-top: 0;
  }

  .modal-commits-list {
    max-height: 240px;
    overflow-y: auto;
    padding-left: 20px;
    margin: 16px 0 24px;
    font-size: 13px;
  }

  .modal-commits-list li {
    margin-bottom: 6px;
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 12px;
  }

  .muted { color: var(--text-muted); }
  .text-sm { font-size: 12px; }
  .warning-text { color: var(--warning); font-size: 13px; margin: 12px 0; }
  .loading-state { text-align: center; padding: 64px; color: var(--text-muted); }
</style>
