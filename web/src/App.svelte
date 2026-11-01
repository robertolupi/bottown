<script lang="ts">
  import { onMount } from 'svelte';
  import type { UIStatusResponse, ActionResult, HistoryRunInfo, IssueTimeline, ActorDetail } from './types';

  let status = $state<UIStatusResponse | null>(null);
  let loading = $state<boolean>(true);
  let refreshing = $state<boolean>(false);
  let errorMessage = $state<string | null>(null);
  let actionMessage = $state<{ type: 'success' | 'error'; text: string } | null>(null);

  // Modals
  let showPushModal = $state<boolean>(false);
  let showFlowModal = $state<boolean>(false);
  let showRunModal = $state<boolean>(false);
  let showStopModal = $state<boolean>(false);
  let showGCModal = $state<boolean>(false);
  let showTimelineModal = $state<boolean>(false);
  // After-pull confirmation modal (§4.4): the steps from main's .bottown.toml
  let showAfterPullModal = $state<boolean>(false);
  let afterPullOutput = $state<string | null>(null);
  let actionLoading = $state<boolean>(false);

  // Stop modal state
  let stopRunId = $state<string>('');

  // GC modal state
  let gcIncludeFailed = $state<boolean>(false);

  // Flow form state
  let flowIssue = $state<number | string>('');
  let flowImplement = $state<string>('');
  let flowVerify = $state<string>('');
  let flowRounds = $state<number>(3);
  let flowTimeout = $state<string>('60m');

  // Run form state
  let runKind = $state<'implement' | 'verify' | 'discuss'>('implement');
  let runTarget = $state<number | string>('');
  let runActor = $state<string>('');
  let runQuestion = $state<string>('');
  let runAllowSameFamily = $state<boolean>(false);

  // History state & filters
  let historyRuns = $state<HistoryRunInfo[]>([]);
  let historyLoading = $state<boolean>(false);
  let filterActor = $state<string>('');
  let filterKind = $state<string>('');
  let filterStatus = $state<string>('');
  let filterSearch = $state<string>('');

  // Timeline state
  let timelineData = $state<IssueTimeline | null>(null);
  let timelineLoading = $state<boolean>(false);

  function getActor(name: string): ActorDetail | undefined {
    return status?.actors?.find((a) => a.name === name);
  }

  let flowSameFamily = $derived(
    flowImplement !== '' &&
    flowVerify !== '' &&
    getActor(flowImplement)?.family !== undefined &&
    getActor(flowImplement)?.family === getActor(flowVerify)?.family
  );

  let filteredHistory = $derived(
    historyRuns.filter((h) => {
      if (filterActor && h.actor !== filterActor) return false;
      if (filterKind && h.kind !== filterKind) return false;
      if (filterStatus && h.status !== filterStatus) return false;
      if (filterSearch) {
        const s = filterSearch.toLowerCase();
        return (
          h.target.toLowerCase().includes(s) ||
          h.summary.toLowerCase().includes(s) ||
          h.id.toLowerCase().includes(s) ||
          h.actor.toLowerCase().includes(s)
        );
      }
      return true;
    })
  );

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
      // Set default actor in forms if available and unset
      if (status?.actors && status.actors.length > 0) {
        if (!flowImplement) flowImplement = status.actors[0].name;
        if (!flowVerify && status.actors.length > 1) {
          const diffFamily = status.actors.find((a) => a.family !== status!.actors![0].family);
          flowVerify = diffFamily ? diffFamily.name : status.actors[1].name;
        }
        if (!runActor) runActor = status.actors[0].name;
      }
    } catch (e: any) {
      errorMessage = e.message || 'Unknown error loading status';
    } finally {
      loading = false;
      refreshing = false;
    }
  }

  async function loadHistory() {
    historyLoading = true;
    try {
      const res = await fetch('/api/runs?state=ended');
      if (!res.ok) {
        throw new Error(`Failed to load history: ${res.statusText}`);
      }
      historyRuns = await res.json();
    } catch (e: any) {
      console.error('Failed to load history:', e);
    } finally {
      historyLoading = false;
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
      // Show the declared after-pull steps; they run only on confirmation.
      if (status?.health?.repo_settings?.after_pull?.length) {
        afterPullOutput = null;
        showAfterPullModal = true;
      }
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

  async function handleConfirmAfterPull() {
    if (actionLoading) return;
    actionLoading = true;
    actionMessage = null;
    afterPullOutput = null;

    try {
      const res = await fetch('/api/actions/after-pull', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ steps: status?.health?.repo_settings?.after_pull ?? [] }),
      });
      const data: ActionResult = await res.json();
      afterPullOutput = data.output || null;
      if (!res.ok || !data.success) {
        throw new Error(data.error || 'After-pull steps failed');
      }
      actionMessage = { type: 'success', text: 'After-pull steps succeeded' };
      await loadStatus(true);
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
    } finally {
      actionLoading = false;
    }
  }

  function openAfterPull() {
    afterPullOutput = null;
    showAfterPullModal = true;
  }

  async function handleStartFlow() {
    if (actionLoading || flowSameFamily) return;
    actionLoading = true;
    actionMessage = null;

    try {
      const res = await fetch('/api/actions/flow', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          issue: Number(flowIssue),
          implement: flowImplement,
          verify: flowVerify,
          rounds: Number(flowRounds) || 3,
          timeout: flowTimeout || '60m',
        }),
      });
      const data: ActionResult = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(data.error || data.output || 'Start flow failed');
      }
      actionMessage = { type: 'success', text: data.output };
      showFlowModal = false;
      await loadStatus(true);
      await loadHistory();
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
    } finally {
      actionLoading = false;
    }
  }

  async function handleStartRun() {
    if (actionLoading) return;
    actionLoading = true;
    actionMessage = null;

    try {
      const res = await fetch('/api/actions/run', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          kind: runKind,
          target: Number(runTarget),
          actor: runActor,
          question: runKind === 'discuss' ? runQuestion : undefined,
          allow_same_family: runKind === 'verify' ? runAllowSameFamily : undefined,
        }),
      });
      const data: ActionResult = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(data.error || data.output || 'Start run failed');
      }
      actionMessage = { type: 'success', text: data.output };
      showRunModal = false;
      await loadStatus(true);
      await loadHistory();
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
    } finally {
      actionLoading = false;
    }
  }

  async function handleConfirmStop() {
    if (actionLoading || !stopRunId) return;
    actionLoading = true;
    actionMessage = null;

    try {
      const res = await fetch('/api/actions/stop', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: stopRunId }),
      });
      const data: ActionResult = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(data.error || data.output || 'Stop run failed');
      }
      actionMessage = { type: 'success', text: data.output || `Run ${stopRunId} stopped` };
      showStopModal = false;
      await loadStatus(true);
      await loadHistory();
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
    } finally {
      actionLoading = false;
    }
  }

  async function handleConfirmGC() {
    if (actionLoading) return;
    actionLoading = true;
    actionMessage = null;

    try {
      const res = await fetch('/api/actions/gc', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ failed: gcIncludeFailed }),
      });
      const data: ActionResult = await res.json();
      if (!res.ok || !data.success) {
        throw new Error(data.error || data.output || 'GC failed');
      }
      actionMessage = { type: 'success', text: data.output || 'Run cleanup succeeded' };
      showGCModal = false;
      await loadStatus(true);
      await loadHistory();
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
    } finally {
      actionLoading = false;
    }
  }

  async function openTimeline(issueNum: number) {
    timelineLoading = true;
    timelineData = null;
    showTimelineModal = true;

    try {
      const res = await fetch(`/api/issues/${issueNum}/timeline`);
      if (!res.ok) {
        throw new Error(`Failed to load timeline for #${issueNum}`);
      }
      timelineData = await res.json();
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
      showTimelineModal = false;
    } finally {
      timelineLoading = false;
    }
  }

  function extractIssueAndOpenTimeline(target: string) {
    const num = parseInt(target.replace(/^[#!]/, ''), 10);
    if (!isNaN(num) && num > 0) {
      openTimeline(num);
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

  function formatUsage(u?: any): string {
    if (!u) return '—';
    const parts: string[] = [];
    if (u.input_tokens || u.output_tokens) {
      const inTok = u.input_tokens ? `${(u.input_tokens / 1000).toFixed(1)}k in` : '';
      const outTok = u.output_tokens ? `${(u.output_tokens / 1000).toFixed(1)}k out` : '';
      parts.push([inTok, outTok].filter(Boolean).join(', '));
    }
    if (u.cost_usd !== undefined && u.cost_usd !== null) {
      parts.push(`$${u.cost_usd.toFixed(3)}`);
    }
    if (u.turns) {
      parts.push(`${u.turns} turn${u.turns > 1 ? 's' : ''}`);
    }
    return parts.join(' · ') || '—';
  }

  onMount(() => {
    loadStatus();
    loadHistory();
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
      <button class="btn btn-secondary" onclick={() => { loadStatus(true); loadHistory(); }} disabled={refreshing}>
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
      <!-- 0. Owner Inbox Section (§4.1) -->
      <section class="card full-width">
        <div class="card-header">
          <div class="card-header-left">
            <h2>Owner Inbox</h2>
            {#if status.inbox_error}
              <span class="badge badge-error">Error</span>
            {:else if status.inbox && status.inbox.length > 0}
              <span class="badge badge-warning">{status.inbox.length} item{status.inbox.length > 1 ? 's' : ''}</span>
            {:else}
              <span class="badge badge-success">Inbox zero</span>
            {/if}
          </div>
        </div>

        <div class="card-body">
          {#if status.inbox_error}
            <div class="banner banner-error">
              <span>Failed to load inbox: {status.inbox_error}</span>
            </div>
          {:else if !status.inbox || status.inbox.length === 0}
            <div class="inbox-zero">
              <span class="inbox-zero-icon">✓</span>
              <span>Nothing waiting on the owner in this repository.</span>
            </div>
          {:else}
            <div class="inbox-list">
              {#each status.inbox as item (item.id)}
                <div class="inbox-item">
                  <div class="inbox-badge-col">
                    {#if item.kind === 'ready_to_merge'}
                      <span class="badge badge-success">ready to merge</span>
                    {:else if item.kind === 'conflicts_with_main'}
                      <span class="badge badge-error">conflicts with main</span>
                    {:else if item.kind === 'stopped_flow'}
                      <span class="badge badge-warning">stopped flow</span>
                    {:else if item.kind === 'broken_run'}
                      <span class="badge badge-error">broken run</span>
                    {:else if item.kind === 'discuss_answer'}
                      <span class="badge badge-accent">discuss answer</span>
                    {:else if item.kind === 'actor_filed_issue'}
                      <span class="badge badge-neutral">actor filed</span>
                    {/if}
                  </div>

                  <div class="inbox-content-col">
                    <div class="inbox-title-row">
                      <a class="pr-badge" href={item.target_url} target="_blank" rel="noreferrer">
                        {item.target}
                      </a>
                      <span class="inbox-title">{item.title}</span>
                    </div>
                    <div class="inbox-desc muted">{item.description}</div>
                  </div>

                  <div class="inbox-action-col">
                    {#if item.action}
                      <button
                        class="btn btn-sm btn-secondary"
                        onclick={() => {
                          if (item.action?.command.includes('gc')) {
                            gcIncludeFailed = item.action.command.includes('--failed');
                            showGCModal = true;
                          } else if (item.action?.command.includes('stop')) {
                            stopRunId = item.target;
                            showStopModal = true;
                          }
                        }}
                      >
                        {item.action.label}
                      </button>
                    {/if}
                    <a class="btn btn-sm btn-primary" href={item.target_url} target="_blank" rel="noreferrer">
                      View ↗
                    </a>
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </section>

      <!-- 1. Live Runs & Flow Actions (§4.2, §5) -->
      <section class="card full-width">
        <div class="card-header">
          <div class="card-header-left">
            <h2>Live Runs</h2>
            {#if status.live_runs && status.live_runs.length > 0}
              <span class="badge badge-accent">{status.live_runs.length} active</span>
            {:else}
              <span class="badge badge-neutral">0 active</span>
            {/if}
          </div>
          <div class="card-header-right">
            <button class="btn btn-sm btn-primary" onclick={() => (showFlowModal = true)}>
              Start Flow
            </button>
            <button class="btn btn-sm btn-secondary" onclick={() => (showRunModal = true)}>
              Start Run
            </button>
            <button class="btn btn-sm btn-secondary" onclick={() => (showGCModal = true)}>
              Clean up (gc)
            </button>
          </div>
        </div>

        <div class="card-body">
          {#if !status.live_runs || status.live_runs.length === 0}
            <div class="inbox-zero">
              <span class="inbox-zero-icon">💤</span>
              <span>No runs currently active in this repository.</span>
            </div>
          {:else}
            <div class="live-runs-list">
              {#each status.live_runs as run (run.id)}
                <div class="run-card">
                  <div class="run-card-header">
                    <div class="run-card-title">
                      {#if run.flow}
                        <span class="badge badge-accent flow-badge">{run.flow.label}</span>
                      {/if}
                      <a class="pr-badge" href={run.target_url} target="_blank" rel="noreferrer">{run.target}</a>
                      <span class="badge badge-neutral">{run.kind}</span>
                      <span class="badge repo-badge">{run.actor} ({run.family})</span>
                      <span class="badge instance-badge">{run.interactive ? 'interactive' : 'batch'}</span>
                    </div>
                    <div class="run-card-actions">
                      <span class="run-timer">{run.elapsed} / {run.timeout}</span>
                      <button
                        class="btn btn-sm btn-danger"
                        onclick={() => {
                          stopRunId = run.id;
                          showStopModal = true;
                        }}
                      >
                        Stop
                      </button>
                    </div>
                  </div>

                  {#if !run.interactive}
                    <div class="log-tail-box">
                      <div class="log-tail-header">
                        <span>Live Tail (.bottown/runs/{run.id}.log)</span>
                      </div>
                      <div class="log-tail-content">
                        {#if !run.log_tail || run.log_tail.length === 0}
                          <div class="log-line muted">Waiting for agent activity...</div>
                        {:else}
                          {#each run.log_tail as line}
                            <div class="log-line">{line}</div>
                          {/each}
                        {/if}
                      </div>
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </section>

      <!-- 2. Run History Section (§4.2) -->
      <section class="card full-width">
        <div class="card-header">
          <div class="card-header-left">
            <h2>Run History</h2>
            <span class="badge badge-neutral">{historyRuns.length} total</span>
          </div>
          <div class="card-header-right filters-row">
            <input
              type="text"
              class="input input-sm"
              placeholder="Search target or summary..."
              bind:value={filterSearch}
            />
            <select class="input input-sm select-sm" bind:value={filterActor}>
              <option value="">All Actors</option>
              {#if status.actors}
                {#each status.actors as act}
                  <option value={act.name}>{act.name}</option>
                {/each}
              {/if}
            </select>
            <select class="input input-sm select-sm" bind:value={filterKind}>
              <option value="">All Kinds</option>
              <option value="implement">Implement</option>
              <option value="verify">Verify</option>
              <option value="discuss">Discuss</option>
            </select>
            <select class="input input-sm select-sm" bind:value={filterStatus}>
              <option value="">All Statuses</option>
              <option value="succeeded">Succeeded</option>
              <option value="failed">Failed</option>
              <option value="revoked">Revoked</option>
            </select>
          </div>
        </div>

        <div class="card-body">
          {#if historyLoading}
            <div class="loading-state">Loading history...</div>
          {:else if filteredHistory.length === 0}
            <div class="inbox-zero">
              <span>No matching finished runs found.</span>
            </div>
          {:else}
            <div class="table-container">
              <table class="data-table">
                <thead>
                  <tr>
                    <th>Target</th>
                    <th>Kind</th>
                    <th>Actor</th>
                    <th>Status</th>
                    <th>Duration</th>
                    <th>Usage</th>
                    <th>Summary</th>
                    <th>When</th>
                    <th>Timeline</th>
                  </tr>
                </thead>
                <tbody>
                  {#each filteredHistory as h (h.id)}
                    <tr>
                      <td>
                        <a class="pr-badge" href={h.target_url} target="_blank" rel="noreferrer">{h.target}</a>
                      </td>
                      <td><span class="badge badge-neutral">{h.kind}</span></td>
                      <td><span class="badge repo-badge">{h.actor} ({h.family})</span></td>
                      <td>
                        <span class="badge badge-{h.status === 'succeeded' ? 'success' : h.status === 'failed' ? 'error' : 'warning'}">
                          {h.status}
                        </span>
                      </td>
                      <td class="muted">{h.duration || '—'}</td>
                      <td class="muted text-sm">{formatUsage(h.usage)}</td>
                      <td class="summary-cell" title={h.summary}>{h.summary || '—'}</td>
                      <td class="muted text-sm">{formatTime(h.created)}</td>
                      <td>
                        <button class="btn btn-sm btn-secondary" onclick={() => extractIssueAndOpenTimeline(h.target)}>
                          Timeline
                        </button>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        </div>
      </section>

      <!-- 3. Upstream & Forge Status Card (§4.3) -->
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
              <button class="btn btn-primary" onclick={handlePull} disabled={actionLoading}>
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
              <button class="btn btn-accent" onclick={() => (showPushModal = true)} disabled={actionLoading}>
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
              <button class="btn btn-secondary" onclick={openAfterPull} disabled={actionLoading}>
                Run after-pull steps…
              </button>
            </div>
          {/if}
        </div>
      </section>

      <!-- 4. Health Checklist Card (§4.3) -->
      <section class="card">
        <div class="card-header">
          <h2>Health Checklist</h2>
        </div>

        <div class="card-body">
          <ul class="checklist">
            <li class="check-item">
              <span class="check-icon {status.health.agent_instructions.found ? 'icon-success' : 'icon-error'}">
                {status.health.agent_instructions.found ? '✓' : '✕'}
              </span>
              <div class="check-content">
                <strong>Agent Instructions</strong>
                <span class="muted text-sm">
                  {status.health.agent_instructions.found
                    ? status.health.agent_instructions.filename
                    : 'Missing AGENTS.md / README.md'}
                </span>
              </div>
            </li>

            <li class="check-item">
              <span class="check-icon {status.health.protected_main.protected ? 'icon-success' : 'icon-error'}">
                {status.health.protected_main.protected ? '✓' : '✕'}
              </span>
              <div class="check-content">
                <strong>Protected Main Branch</strong>
                <span class="muted text-sm">
                  {status.health.protected_main.protected
                    ? 'Forgejo main is protected'
                    : status.health.protected_main.error || 'Main branch unprotected'}
                </span>
              </div>
            </li>

            <li class="check-item">
              <span class="check-icon {status.health.repo_settings.error ? 'icon-error' : status.health.repo_settings.found ? 'icon-success' : 'icon-warning'}">
                {status.health.repo_settings.error ? '✗' : status.health.repo_settings.found ? '✓' : '!'}
              </span>
              <div class="check-content">
                <strong>Repository Settings</strong>
                <span class="muted text-sm">
                  {#if status.health.repo_settings.error}
                    {status.health.repo_settings.error}
                  {:else if status.health.repo_settings.found}
                    <code>{status.health.repo_settings.file}</code> on {status.health.repo_settings.branch}:
                    {status.health.repo_settings.after_pull.length} after-pull step{status.health.repo_settings.after_pull.length === 1 ? '' : 's'}
                  {:else}
                    No <code>{status.health.repo_settings.file}</code> on {status.health.repo_settings.branch}; using the defaults
                  {/if}
                </span>
              </div>
            </li>

            <li class="check-item">
              <span class="check-icon {status.health.version.matches ? 'icon-success' : 'icon-warning'}">
                {status.health.version.matches ? '✓' : '!'}
              </span>
              <div class="check-content">
                <strong>CLI & Server Versions</strong>
                <span class="muted text-sm">
                  CLI {status.health.version.cli_version} · Server {status.health.version.server_version}
                </span>
              </div>
            </li>

            <li class="check-item">
              <span class="check-icon icon-success">✓</span>
              <div class="check-content">
                <strong>Harness Commands</strong>
                <div class="harness-badges">
                  {#each status.health.harness_commands as hc}
                    <span class="badge {hc.current ? 'badge-success' : 'badge-warning'}">
                      {hc.harness}: {hc.current ? 'current' : 'outdated'}
                    </span>
                  {/each}
                </div>
              </div>
            </li>
          </ul>

          {#if status.health.worktrees.length > 0}
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
                    {#each status.health.worktrees as wt}
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
    </div>
  {/if}
</main>

<!-- Push Confirmation Modal -->
{#if showPushModal && status?.upstream?.unpushed_commits}
  <div class="modal-backdrop">
    <div class="modal-card" role="dialog" aria-modal="true" tabindex="-1">
      <h3>Push main to origin</h3>
      <p class="muted">The following commit(s) will be pushed to origin/main:</p>
      <ul class="modal-commits-list">
        {#each status.upstream.unpushed_commits as c}
          <li>
            <strong class="font-mono">{c.short_sha}</strong> {c.subject}
            {#if c.pr_number}
              <span class="badge pr-badge">!{c.pr_number}</span>
            {/if}
            <span class="muted text-sm">— {c.author}</span>
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

<!-- Start Flow Modal (§5) -->
{#if showFlowModal}
  <div class="modal-backdrop">
    <div class="modal-card" role="dialog" aria-modal="true" tabindex="-1">
      <div class="modal-header">
        <h3>Start an Issue Flow</h3>
        <button class="close-btn" onclick={() => (showFlowModal = false)}>✕</button>
      </div>
      <p class="muted text-sm">
        Supervises an issue through implement and cross-family verify until ready to merge.
      </p>

      <form onsubmit={(e) => { e.preventDefault(); handleStartFlow(); }} class="form-grid">
        <div class="form-group">
          <label for="flow-issue">Issue Number</label>
          <input
            id="flow-issue"
            type="number"
            class="input"
            placeholder="e.g. 51"
            required
            bind:value={flowIssue}
          />
        </div>

        <div class="form-row">
          <div class="form-group">
            <label for="flow-imp">Implementer</label>
            <select id="flow-imp" class="input" bind:value={flowImplement}>
              {#if status?.actors}
                {#each status.actors as act}
                  <option value={act.name}>{act.name} ({act.family})</option>
                {/each}
              {/if}
            </select>
          </div>

          <div class="form-group">
            <label for="flow-ver">Verifier (Cross-Family)</label>
            <select id="flow-ver" class="input" bind:value={flowVerify}>
              {#if status?.actors}
                {#each status.actors as act}
                  <option value={act.name}>{act.name} ({act.family})</option>
                {/each}
              {/if}
            </select>
          </div>
        </div>

        {#if flowSameFamily}
          <div class="banner banner-error">
            <span>Verifier must be from another model family than implementer.</span>
          </div>
        {/if}

        <div class="form-row">
          <div class="form-group">
            <label for="flow-rounds">Rounds</label>
            <input id="flow-rounds" type="number" class="input" min="1" max="10" bind:value={flowRounds} />
          </div>

          <div class="form-group">
            <label for="flow-timeout">Timeout</label>
            <input id="flow-timeout" type="text" class="input" placeholder="60m" bind:value={flowTimeout} />
          </div>
        </div>

        <div class="modal-actions">
          <button type="button" class="btn btn-secondary" onclick={() => (showFlowModal = false)}>
            Cancel
          </button>
          <button type="submit" class="btn btn-primary" disabled={actionLoading || flowSameFamily || !flowIssue}>
            {actionLoading ? 'Starting...' : 'Start Flow'}
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

<!-- Start Run Modal (§5) -->
{#if showRunModal}
  <div class="modal-backdrop">
    <div class="modal-card" role="dialog" aria-modal="true" tabindex="-1">
      <div class="modal-header">
        <h3>Start Single Run</h3>
        <button class="close-btn" onclick={() => (showRunModal = false)}>✕</button>
      </div>

      <form onsubmit={(e) => { e.preventDefault(); handleStartRun(); }} class="form-grid">
        <div class="form-row">
          <div class="form-group">
            <label for="run-kind">Run Kind</label>
            <select id="run-kind" class="input" bind:value={runKind}>
              <option value="implement">Implement (Issue)</option>
              <option value="verify">Verify (PR)</option>
              <option value="discuss">Discuss (Issue/PR)</option>
            </select>
          </div>

          <div class="form-group">
            <label for="run-target">{runKind === 'verify' ? 'PR Number' : 'Issue Number'}</label>
            <input
              id="run-target"
              type="number"
              class="input"
              placeholder="e.g. 51"
              required
              bind:value={runTarget}
            />
          </div>
        </div>

        <div class="form-group">
          <label for="run-act">Actor</label>
          <select id="run-act" class="input" bind:value={runActor}>
            {#if status?.actors}
              {#each status.actors as act}
                <option value={act.name}>{act.name} ({act.family})</option>
              {/each}
            {/if}
          </select>
        </div>

        {#if runKind === 'discuss'}
          <div class="form-group">
            <label for="run-q">Question (optional)</label>
            <input
              id="run-q"
              type="text"
              class="input"
              placeholder="What should the agent answer?"
              bind:value={runQuestion}
            />
          </div>
        {/if}

        {#if runKind === 'verify'}
          <div class="form-checkbox">
            <label>
              <input type="checkbox" bind:checked={runAllowSameFamily} />
              Allow same-family verifier (waiver)
            </label>
          </div>
        {/if}

        <div class="modal-actions">
          <button type="button" class="btn btn-secondary" onclick={() => (showRunModal = false)}>
            Cancel
          </button>
          <button type="submit" class="btn btn-primary" disabled={actionLoading || !runTarget}>
            {actionLoading ? 'Starting...' : 'Start Run'}
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

<!-- Stop Run Confirmation Modal (§5) -->
{#if showStopModal}
  <div class="modal-backdrop">
    <div class="modal-card" role="dialog" aria-modal="true" tabindex="-1">
      <h3>Stop Run & Flow</h3>
      <p class="muted">
        Are you sure you want to stop run <strong>{stopRunId}</strong>?
        This will kill the harness process group and end the run as failed. If this run is part of a flow, the flow will also stop.
      </p>
      <div class="modal-actions">
        <button class="btn btn-secondary" onclick={() => (showStopModal = false)} disabled={actionLoading}>
          Cancel
        </button>
        <button class="btn btn-danger" onclick={handleConfirmStop} disabled={actionLoading}>
          {actionLoading ? 'Stopping...' : 'Confirm & Stop Run'}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Clean up Broken Runs (GC) Modal (§5) -->
{#if showGCModal}
  <div class="modal-backdrop">
    <div class="modal-card" role="dialog" aria-modal="true" tabindex="-1">
      <h3>Clean up Runs (bottown run gc)</h3>
      <p class="muted">
        Remove local run worktrees and purges server records for finished runs.
      </p>
      <div class="form-checkbox">
        <label>
          <input type="checkbox" bind:checked={gcIncludeFailed} />
          Include failed runs with leftover worktrees (--failed)
        </label>
      </div>
      <div class="modal-actions">
        <button class="btn btn-secondary" onclick={() => (showGCModal = false)} disabled={actionLoading}>
          Cancel
        </button>
        <button class="btn btn-accent" onclick={handleConfirmGC} disabled={actionLoading}>
          {actionLoading ? 'Cleaning up...' : 'Confirm & Run GC'}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Issue Timeline Modal (§4.2) -->
{#if showTimelineModal}
  <div class="modal-backdrop">
    <div class="modal-card timeline-modal" role="dialog" aria-modal="true" tabindex="-1">
      <div class="modal-header">
        <h3>Timeline for Issue #{timelineData?.issue}: {timelineData?.title}</h3>
        <button class="close-btn" onclick={() => (showTimelineModal = false)}>✕</button>
      </div>

      {#if timelineLoading}
        <div class="loading-state">Loading timeline...</div>
      {:else if timelineData}
        <div class="timeline-list">
          {#if timelineData.events.length === 0}
            <div class="muted">No events recorded for this issue.</div>
          {:else}
            {#each timelineData.events as ev}
              <div class="timeline-item timeline-{ev.type}">
                <div class="timeline-dot"></div>
                <div class="timeline-body">
                  <div class="timeline-header">
                    <span class="timeline-title">{ev.title}</span>
                    <span class="muted text-sm">{formatTime(ev.time)}</span>
                  </div>
                  {#if ev.detail}
                    <div class="timeline-detail">{ev.detail}</div>
                  {/if}
                  {#if ev.actor}
                    <div class="timeline-meta muted text-sm">by {ev.actor}</div>
                  {/if}
                </div>
              </div>
            {/each}
          {/if}
        </div>
      {/if}

      <div class="modal-actions">
        <button class="btn btn-secondary" onclick={() => (showTimelineModal = false)}>
          Close
        </button>
      </div>
    </div>
  </div>
{/if}

{#if showAfterPullModal}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={() => { if (!actionLoading) showAfterPullModal = false; }}
    onkeydown={(e) => { if (e.key === 'Escape' && !actionLoading) showAfterPullModal = false; }}
  >
    <div
      class="modal-card"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
    >
      <h3>Run the after-pull steps</h3>
      <p>
        <code>{status?.health?.repo_settings?.file}</code> on <code>{status?.health?.repo_settings?.branch}</code>
        ({status?.health?.repo_settings?.commit?.slice(0, 12)}) declares these commands. They run one at a time,
        with <code>sh -c</code>, in the main checkout, stopping at the first that fails:
      </p>
      <ul class="modal-commits-list">
        {#each status?.health?.repo_settings?.after_pull || [] as step}
          <li><code>{step}</code></li>
        {/each}
      </ul>
      {#if afterPullOutput}
        <pre class="modal-output">{afterPullOutput}</pre>
      {/if}
      <div class="modal-actions">
        <button class="btn btn-secondary" onclick={() => (showAfterPullModal = false)} disabled={actionLoading}>
          Close
        </button>
        <button class="btn btn-accent" onclick={handleConfirmAfterPull} disabled={actionLoading}>
          {actionLoading ? 'Running...' : 'Confirm & Run'}
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
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
    background: var(--bg);
    color: var(--text);
  }

  .container {
    max-width: 1200px;
    margin: 0 auto;
    padding: 24px 16px;
  }

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 24px;
    flex-wrap: wrap;
    gap: 16px;
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
    font-family: monospace;
    font-weight: bold;
    padding: 2px 6px;
    border-radius: 4px;
  }
  .pr-badge:hover { text-decoration: underline; }

  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 8px 16px;
    border-radius: 6px;
    font-size: 14px;
    font-weight: 500;
    cursor: pointer;
    border: none;
    transition: background-color 0.15s ease;
    text-decoration: none;
  }

  .btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-primary { background: var(--primary); color: #fff; }
  .btn-primary:hover:not(:disabled) { background: var(--primary-hover); }
  .btn-secondary { background: rgba(125, 125, 125, 0.15); color: var(--text); }
  .btn-secondary:hover:not(:disabled) { background: rgba(125, 125, 125, 0.25); }
  .btn-accent { background: var(--accent); color: #fff; }
  .btn-accent:hover:not(:disabled) { background: var(--accent-hover); }
  .btn-danger { background: var(--error); color: #fff; }
  .btn-danger:hover:not(:disabled) { opacity: 0.9; }

  .btn-sm {
    padding: 4px 10px;
    font-size: 12px;
  }

  .banner {
    padding: 12px 16px;
    border-radius: 8px;
    margin-bottom: 20px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 14px;
  }

  .banner-success { background: var(--success-bg); color: var(--success); }
  .banner-error { background: var(--error-bg); color: var(--error); }

  .close-btn {
    background: none;
    border: none;
    font-size: 16px;
    cursor: pointer;
    color: inherit;
    padding: 0 4px;
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 24px;
  }

  @media (max-width: 900px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }

  .card {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .full-width {
    grid-column: 1 / -1;
  }

  .card-header {
    padding: 16px 20px;
    border-bottom: 1px solid var(--border);
    display: flex;
    justify-content: space-between;
    align-items: center;
    flex-wrap: wrap;
    gap: 12px;
  }

  .card-header-left, .card-header-right {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }

  .card-header h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
  }

  .card-body {
    padding: 20px;
    flex: 1;
  }

  .inbox-zero {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 24px;
    color: var(--text-muted);
    font-size: 14px;
    justify-content: center;
  }

  .inbox-zero-icon {
    font-size: 20px;
    color: var(--success);
  }

  .inbox-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .inbox-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 16px;
    background: rgba(125, 125, 125, 0.05);
    border: 1px solid var(--border);
    border-radius: 8px;
    gap: 16px;
    flex-wrap: wrap;
  }

  .inbox-badge-col {
    min-width: 140px;
  }

  .inbox-content-col {
    flex: 1;
    min-width: 250px;
  }

  .inbox-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
    font-size: 14px;
    margin-bottom: 4px;
  }

  .inbox-desc {
    font-size: 13px;
  }

  .inbox-action-col {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  /* Live Runs styles */
  .live-runs-list {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

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

  .flow-badge {
    font-weight: 600;
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

  .log-tail-box {
    background: #090d16;
    color: #e2e8f0;
    font-family: monospace;
    font-size: 12px;
  }

  .log-tail-header {
    padding: 6px 12px;
    background: #111827;
    color: #94a3b8;
    font-size: 11px;
    border-bottom: 1px solid #1e293b;
  }

  .log-tail-content {
    max-height: 180px;
    overflow-y: auto;
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .log-line {
    white-space: pre-wrap;
    word-break: break-all;
  }

  /* Filters */
  .filters-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .input {
    background: var(--card-bg);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 8px 12px;
    border-radius: 6px;
    font-size: 13px;
  }

  .input-sm {
    padding: 4px 8px;
    font-size: 12px;
  }

  .select-sm {
    cursor: pointer;
  }

  .summary-cell {
    max-width: 280px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* Forms */
  .form-grid {
    display: flex;
    flex-direction: column;
    gap: 16px;
    margin-top: 16px;
  }

  .form-group {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .form-group label {
    font-size: 13px;
    font-weight: 500;
  }

  .form-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }

  .form-checkbox {
    margin: 8px 0;
    font-size: 13px;
  }

  .form-checkbox label {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
  }

  /* Timeline */
  .timeline-modal {
    max-width: 680px;
  }

  .modal-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--border);
    padding-bottom: 12px;
  }

  .modal-header h3 {
    margin: 0;
  }

  .timeline-list {
    margin: 20px 0;
    max-height: 400px;
    overflow-y: auto;
    position: relative;
    padding-left: 20px;
  }

  .timeline-item {
    position: relative;
    padding-bottom: 20px;
    border-left: 2px solid var(--border);
    padding-left: 16px;
  }

  .timeline-item:last-child {
    border-left: 2px solid transparent;
  }

  .timeline-dot {
    position: absolute;
    left: -7px;
    top: 2px;
    width: 12px;
    height: 12px;
    border-radius: 50%;
    background: var(--primary);
    border: 2px solid var(--card-bg);
  }

  .timeline-review .timeline-dot { background: var(--warning); }
  .timeline-pr_merged .timeline-dot { background: var(--success); }

  .timeline-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-weight: 600;
    font-size: 13px;
  }

  .timeline-detail {
    font-size: 13px;
    margin-top: 4px;
    color: var(--text);
  }

  .timeline-meta {
    margin-top: 2px;
  }

  /* Status details */
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

  .modal-output {
    max-height: 240px;
    overflow: auto;
    font-size: 12px;
    padding: 8px;
    margin: 0 0 16px;
    background: rgba(125, 125, 125, 0.1);
    border-radius: 6px;
    white-space: pre-wrap;
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 12px;
    margin-top: 20px;
  }

  .muted { color: var(--text-muted); }
  .text-sm { font-size: 12px; }
  .loading-state { text-align: center; padding: 48px; color: var(--text-muted); }
</style>
