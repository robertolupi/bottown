<script lang="ts">
  import { onMount } from 'svelte';
  import type {
    UIStatusResponse,
    ActionResult,
    HistoryRunInfo,
    HistoryThread,
    IssueTimeline,
    Denial,
  } from './types';
  import { lifecycleChange } from './lifecycle';
  import IssueReference from './IssueReference.svelte';
  import LiveLogTail from './LiveLogTail.svelte';
  import {
    buildFlowActionPayload,
    buildFlowConstituentRuns,
    buildFlowTimeline,
    buildRunActionPayload,
    filterHistoryThreads,
    formatThreadMainLine,
    groupHistoryThreads,
    groupLiveFlows,
    isSameFamilyPair,
    nextPollDelayMs,
    parseIssueNumber,
    parseLivePollSnapshot,
    reconcileExpandedThread,
    resolveInboxAction,
    runOutcome,
    selectDefaultActors,
    standaloneLiveRuns,
    toggleExpandedThread,
  } from './state';

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

  // Timer, Polling & Logs
  let nowTime = $state<number>(Date.now());
  let expandedLogs = $state<Record<string, { loading: boolean; lines?: string[]; error?: string }>>({});

  // History state & filters
  let historyRuns = $state<HistoryRunInfo[]>([]);
  let historyLoading = $state<boolean>(false);
  let filterActor = $state<string>('');
  let filterKind = $state<string>('');
  let filterStatus = $state<string>('');
  let filterDate = $state<string>('');
  let filterSearch = $state<string>('');

  // Flow expansion & timeline state (at most one flow expanded at a time)
  let expandedThreadKey = $state<string | null>(null);
  let threadTimelines = $state<Record<string, IssueTimeline>>({});
  let threadTimelineLoading = $state<Record<string, boolean>>({});
  let threadTimelineError = $state<Record<string, string>>({});

  // Legacy modal timeline state
  let timelineData = $state<IssueTimeline | null>(null);
  let timelineLoading = $state<boolean>(false);

  let flowSameFamily = $derived(isSameFamilyPair(status?.actors, flowImplement, flowVerify));

  function formatElapsed(createdIso: string): string {
    if (!createdIso) return '';
    const start = new Date(createdIso).getTime();
    const diffSec = Math.max(0, Math.floor((nowTime - start) / 1000));
    const m = Math.floor(diffSec / 60);
    const s = diffSec % 60;
    return `${m}m ${s < 10 ? '0' : ''}${s}s`;
  }

  let flowGroups = $derived(() => groupLiveFlows(status?.active_flows, status?.live_runs));

  let standaloneRuns = $derived(() => standaloneLiveRuns(status?.live_runs));

  async function toggleHistoryLog(id: string) {
    if (expandedLogs[id]) {
      delete expandedLogs[id];
      expandedLogs = { ...expandedLogs };
      return;
    }
    expandedLogs[id] = { loading: true };
    expandedLogs = { ...expandedLogs };
    try {
      const res = await fetch(`/api/runs/${id}/log`);
      if (!res.ok) throw new Error('Failed to load log');
      const data = await res.json();
      expandedLogs[id] = { loading: false, lines: data.lines || [] };
    } catch (e: any) {
      expandedLogs[id] = { loading: false, error: e.message };
    }
    expandedLogs = { ...expandedLogs };
  }

  let historyThreads = $derived(
    groupHistoryThreads(historyRuns, status?.live_runs, status?.active_flows),
  );

  let filteredThreads = $derived(
    filterHistoryThreads(
      historyThreads,
      {
        actor: filterActor,
        kind: filterKind,
        status: filterStatus,
        date: filterDate,
        search: filterSearch,
      },
      nowTime,
    ),
  );

  async function loadThreadTimeline(thread: HistoryThread, background = false) {
    if (!thread.issue) return;
    const key = thread.key;
    if (!background && !threadTimelines[key]) {
      threadTimelineLoading = { ...threadTimelineLoading, [key]: true };
    }
    try {
      const prParam = thread.pr !== undefined ? `?pr=${thread.pr}` : '?pr=0';
      const res = await fetch(`/api/issues/${thread.issue}/timeline${prParam}`);
      if (!res.ok) {
        throw new Error(`Failed to load timeline for #${thread.issue}`);
      }
      const data: IssueTimeline = await res.json();
      threadTimelines = { ...threadTimelines, [key]: data };
      if (threadTimelineError[key]) {
        const nextErr = { ...threadTimelineError };
        delete nextErr[key];
        threadTimelineError = nextErr;
      }
    } catch (e: any) {
      if (!background) {
        threadTimelineError = {
          ...threadTimelineError,
          [key]: e.message || 'Failed to load timeline',
        };
      }
    } finally {
      if (threadTimelineLoading[key]) {
        threadTimelineLoading = { ...threadTimelineLoading, [key]: false };
      }
    }
  }

  function selectFlowThread(thread: HistoryThread) {
    if (thread.kind !== 'flow') return;
    const nextKey = toggleExpandedThread(expandedThreadKey, thread.key);
    expandedThreadKey = nextKey;
    if (nextKey === thread.key) {
      loadThreadTimeline(thread, Boolean(threadTimelines[thread.key]));
    }
  }

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
      ({ flowImplement, flowVerify, runActor } = selectDefaultActors(status?.actors, {
        flowImplement,
        flowVerify,
        runActor,
      }));
    } catch (e: any) {
      errorMessage = e.message || 'Unknown error loading status';
    } finally {
      loading = false;
      refreshing = false;
    }
  }

  async function loadHistory(background = false) {
    if (!background && historyRuns.length === 0) {
      historyLoading = true;
    }
    try {
      const prevThreads = groupHistoryThreads(historyRuns, status?.live_runs, status?.active_flows);
      const res = await fetch('/api/runs?state=ended');
      if (!res.ok) {
        throw new Error(`Failed to load history: ${res.statusText}`);
      }
      const nextRuns: HistoryRunInfo[] = await res.json();
      const nextThreads = groupHistoryThreads(nextRuns, status?.live_runs, status?.active_flows);
      const prevKey = expandedThreadKey;
      const reconciledKey = reconcileExpandedThread(prevKey, prevThreads, nextThreads);
      if (reconciledKey && prevKey && reconciledKey !== prevKey && threadTimelines[prevKey]) {
        threadTimelines = { ...threadTimelines, [reconciledKey]: threadTimelines[prevKey] };
      }
      historyRuns = nextRuns;
      expandedThreadKey = reconciledKey;
      if (reconciledKey) {
        const selected = nextThreads.find((t) => t.key === reconciledKey);
        if (selected && selected.kind === 'flow') {
          await loadThreadTimeline(selected, true);
        }
      }
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
        body: JSON.stringify(
          buildFlowActionPayload({
            issue: flowIssue,
            implement: flowImplement,
            verify: flowVerify,
            rounds: flowRounds,
            timeout: flowTimeout,
          }),
        ),
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
        body: JSON.stringify(
          buildRunActionPayload({
            kind: runKind,
            target: runTarget,
            actor: runActor,
            question: runQuestion,
            allowSameFamily: runAllowSameFamily,
          }),
        ),
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
    const num = parseIssueNumber(target);
    if (num !== null) {
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

    const timerInterval = setInterval(() => {
      nowTime = Date.now();
    }, 1000);

    let pollTimeout: any;
    let destroyed = false;

    async function poll() {
      if (destroyed) return;
      try {
        const res = await fetch('/api/runs?state=live');
        if (res.ok) {
          const data = await res.json();
          const { liveRuns, activeFlows } = parseLivePollSnapshot(data);

          if (status) {
            const prevThreads = groupHistoryThreads(historyRuns, status.live_runs, status.active_flows);
            const change = lifecycleChange(status.live_runs, status.active_flows, liveRuns, activeFlows);

            status.live_runs = liveRuns;
            status.active_flows = activeFlows;

            const nextThreads = groupHistoryThreads(historyRuns, liveRuns, activeFlows);
            const prevKey = expandedThreadKey;
            const reconciledKey = reconcileExpandedThread(prevKey, prevThreads, nextThreads);
            if (reconciledKey && prevKey && reconciledKey !== prevKey && threadTimelines[prevKey]) {
              threadTimelines = { ...threadTimelines, [reconciledKey]: threadTimelines[prevKey] };
            }
            expandedThreadKey = reconciledKey;

            // A run or flow started or ended: refresh status and history
            // in the background without altering the expanded flow selection.
            if (change.status) await loadStatus(true);
            if (change.status || change.history) await loadHistory(true);

            // Live-update the selected active flow's timeline.
            if (expandedThreadKey) {
              const currentThreads = groupHistoryThreads(historyRuns, status?.live_runs, status?.active_flows);
              const selected = currentThreads.find((t) => t.key === expandedThreadKey);
              if (selected && selected.kind === 'flow' && (selected.active || change.status || change.history)) {
                await loadThreadTimeline(selected, true);
              }
            }
          }
        }
      } catch {
        // Ignore background polling errors
      } finally {
        if (!destroyed) {
          const delay = nextPollDelayMs(status?.live_runs, status?.active_flows);
          pollTimeout = setTimeout(poll, delay);
        }
      }
    }

    pollTimeout = setTimeout(poll, 2500);

    return () => {
      destroyed = true;
      clearInterval(timerInterval);
      clearTimeout(pollTimeout);
    };
  });
</script>

{#snippet denialList(denials: Denial[] | undefined)}
  {#if denials && denials.length > 0}
    <div class="denials-box">
      <div class="denials-header">Denied by policy ({denials.length})</div>
      {#each denials as d}
        <div class="denial-line">
          <span class="muted">{formatTime(d.time)}</span>
          <span>{d.method ? `${d.method} ${d.path}` : d.op}</span>
          {#if d.rule}<span class="badge badge-error">{d.rule}</span>{/if}
          {#if d.detail}<span class="muted">{d.detail}</span>{/if}
        </div>
      {/each}
    </div>
  {/if}
{/snippet}

{#snippet outcomeBadge(run: HistoryRunInfo | undefined)}
  {@const o = run ? runOutcome(run) : null}
  {#if o}
    <span class="badge badge-{o.tone} outcome-badge outcome-{run?.outcome || 'none'}" title={o.title}>
      <span class="sr-only">Outcome: </span><span aria-hidden="true">{o.icon}</span>
      {o.label}
    </span>
  {:else}
    <span class="muted" title="No outcome yet">—</span>
  {/if}
{/snippet}

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
                      {#if item.issue}
                        <IssueReference
                          number={item.issue}
                          href={item.target_url}
                          label={item.target}
                          title={item.issue_title}
                          showTitle
                        />
                      {:else}
                        <a class="pr-badge" href={item.target_url} target="_blank" rel="noreferrer">
                          {item.target}
                        </a>
                        <span class="inbox-title">{item.title}</span>
                      {/if}
                    </div>
                    <div class="inbox-desc muted">{item.description}</div>
                  </div>

                  <div class="inbox-action-col">
                    {#if item.action}
                      <button
                        class="btn btn-sm btn-secondary"
                        onclick={() => {
                          const resolved = resolveInboxAction(item);
                          if (resolved?.modal === 'gc') {
                            gcIncludeFailed = resolved.includeFailed;
                            showGCModal = true;
                          } else if (resolved?.modal === 'stop') {
                            stopRunId = resolved.stopRunId;
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
            {#if ((status?.live_runs?.length || 0) + (status?.active_flows?.length || 0)) > 0}
              <span class="badge badge-accent">{(status?.live_runs?.length || 0) + (status?.active_flows?.length || 0)} active</span>
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
          {#if (!status?.live_runs || status.live_runs.length === 0) && (!status?.active_flows || status.active_flows.length === 0)}
            <div class="inbox-zero">
              <span class="inbox-zero-icon">💤</span>
              <span>No runs or flows currently active in this repository.</span>
            </div>
          {:else}
            <div class="live-runs-list">
              <!-- Active Flows -->
              {#each flowGroups() as fg (fg.flow.issue)}
                <div class="flow-group-card">
                  <div class="flow-group-header">
                    <div class="flow-group-title">
                      <span class="badge badge-accent flow-badge">
                        Flow for
                        <IssueReference
                          number={fg.flow.issue}
                          href={fg.runs[0]?.issue_url || `${status.links.issues}/${fg.flow.issue}`}
                          label={`#${fg.flow.issue}`}
                          title={fg.runs.find((run) => run.issue_title)?.issue_title}
                          showTitle
                        />
                      </span>
                      <strong>Round {fg.flow.round}/{fg.flow.max_rounds}</strong>
                      <span class="muted text-sm">— {fg.flow.step}</span>
                    </div>
                    <div class="flow-group-actions">
                      <button
                        class="btn btn-sm btn-danger"
                        onclick={() => {
                          stopRunId = `flow-${fg.flow.issue}`;
                          showStopModal = true;
                        }}
                      >
                        Stop Flow
                      </button>
                    </div>
                  </div>

                  <div class="flow-group-runs">
                    {#if fg.runs.length === 0}
                      <div class="flow-waiting-state">
                        <span class="muted text-sm">Waiting between rounds… supervisor will start the next actor.</span>
                      </div>
                    {:else}
                      {#each fg.runs as run (run.id)}
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
                              <a class="pr-badge" href={run.target_url} target="_blank" rel="noreferrer">{run.target}</a>
                            {/if}
                            <span class="badge badge-neutral">{run.kind}</span>
                            <span class="badge repo-badge">{run.actor} ({run.family})</span>
                            {#if run.spec}<span class="badge instance-badge" title="model">model {run.spec}</span>{/if}
                            <span class="badge instance-badge">{run.interactive ? 'interactive' : 'batch'}</span>
                          </div>
                          <div class="run-card-actions">
                            <span class="run-timer">{formatElapsed(run.created)} / {run.timeout}</span>
                            <button
                              class="btn btn-sm btn-danger"
                              onclick={() => {
                                stopRunId = run.id;
                                showStopModal = true;
                              }}
                            >
                              Stop Run
                            </button>
                          </div>
                        </div>

                        {#if !run.interactive}
                          <LiveLogTail runId={run.id} lines={run.log_tail} />
                        {/if}
                        {@render denialList(run.denials)}
                      </div>
                    {/each}
                    {/if}
                  </div>
                </div>
              {/each}

              <!-- Standalone Runs -->
              {#each standaloneRuns() as run (run.id)}
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
                        <a class="pr-badge" href={run.target_url} target="_blank" rel="noreferrer">{run.target}</a>
                      {/if}
                      <span class="badge badge-neutral">{run.kind}</span>
                      <span class="badge repo-badge">{run.actor} ({run.family})</span>
                      {#if run.spec}<span class="badge instance-badge" title="model">model {run.spec}</span>{/if}
                      <span class="badge instance-badge">{run.interactive ? 'interactive' : 'batch'}</span>
                    </div>
                    <div class="run-card-actions">
                      <span class="run-timer">{formatElapsed(run.created)} / {run.timeout}</span>
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
                    <LiveLogTail runId={run.id} lines={run.log_tail} />
                  {/if}
                  {@render denialList(run.denials)}
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
            <h2 id="run-history-heading">Run History</h2>
            <span class="badge badge-neutral">
              {historyThreads.length} {historyThreads.length === 1 ? 'thread' : 'threads'} · {historyRuns.length} {historyRuns.length === 1 ? 'run' : 'runs'}
            </span>
          </div>
          <div class="card-header-right filters-row">
            <input
              type="text"
              class="input input-sm"
              placeholder="Search issue, PR, milestone, or summary..."
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
              <option value="plan">Plan</option>
            </select>
            <select class="input input-sm select-sm" bind:value={filterStatus}>
              <option value="">All Statuses</option>
              <option value="succeeded">Succeeded</option>
              <option value="failed">Failed</option>
              <option value="revoked">Revoked</option>
            </select>
            <select class="input input-sm select-sm" bind:value={filterDate}>
              <option value="">All Time</option>
              <option value="1h">Past hour</option>
              <option value="24h">Past 24 hours</option>
              <option value="7d">Past 7 days</option>
              <option value="30d">Past 30 days</option>
            </select>
          </div>
        </div>

        <div class="card-body">
          {#if historyLoading}
            <div class="loading-state">Loading history...</div>
          {:else if filteredThreads.length === 0}
            <div class="inbox-zero">
              <span>No matching history threads found.</span>
            </div>
          {:else}
            <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
            <div
              class="table-container history-table-container"
              tabindex="0"
              role="region"
              aria-labelledby="run-history-heading"
            >
              <table class="data-table">
                <thead>
                  <tr>
                    <th>Flow / Target</th>
                    <th>Kind</th>
                    <th>Actors</th>
                    <th>Status</th>
                    <th>Outcome</th>
                    <th>Duration</th>
                    <th>Usage</th>
                    <th>Summary</th>
                    <th>Last Activity</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {#each filteredThreads as thread (thread.key)}
                    {@const mainLine = formatThreadMainLine(thread)}
                    {#if thread.kind === 'discuss'}
                      {@const h = thread.runs[0]}
                      <tr class="discuss-thread-row" class:flow-row-active={thread.active}>
                        <td>
                          <div class="thread-main-line">
                            {#if mainLine.issueLabel}
                              <IssueReference
                                number={thread.issue || 0}
                                href={mainLine.issueUrl || h.target_url}
                                label={mainLine.issueLabel}
                                title={mainLine.issueTitle}
                              />
                            {:else}
                              <a class="pr-badge" href={h.target_url} target="_blank" rel="noreferrer">{h.target}</a>
                            {/if}
                            {#if mainLine.milestone}
                              <span class="badge milestone-badge" title="Milestone">{mainLine.milestone}</span>
                            {/if}
                          </div>
                        </td>
                        <td><span class="badge badge-neutral">discuss</span></td>
                        <td><span class="badge repo-badge">{h.actor} ({h.family})</span></td>
                        <td>
                          {#if thread.active}
                            <span class="badge badge-accent flow-live-badge">active</span>
                          {/if}
                          <span class="badge badge-{h.status === 'succeeded' ? 'success' : h.status === 'failed' ? 'error' : 'warning'}">
                            {h.status}
                          </span>
                          {#if h.denials && h.denials.length > 0}
                            <span class="badge badge-error" title="Operations the server's policy refused this run; expand the log to see them">
                              {h.denials.length} denied
                            </span>
                          {/if}
                        </td>
                        <td>{@render outcomeBadge(h)}</td>
                        <td class="muted">{h.duration || '—'}</td>
                        <td class="muted text-sm">{formatUsage(h.usage)}</td>
                        <td class="summary-cell" title={h.summary}>{h.summary || '—'}</td>
                        <td class="muted text-sm">{formatTime(thread.last_activity)}</td>
                        <td>
                          <div class="row-actions">
                            <button
                              class="btn btn-sm btn-secondary"
                              onclick={() => toggleHistoryLog(h.id)}
                              title="Expand parsed run log"
                            >
                              {expandedLogs[h.id] ? 'Hide Log' : 'Log'}
                            </button>
                          </div>
                        </td>
                      </tr>
                      {#if expandedLogs[h.id]}
                        {@const logState = expandedLogs[h.id]}
                        <tr class="log-expand-row">
                          <td colspan="10">
                            {@render denialList(h.denials)}
                            <div class="log-tail-box history-log-box">
                              <div class="log-tail-header">
                                <span>Run Log ({h.id})</span>
                                {#if logState.lines}
                                  <span class="muted text-sm">{logState.lines.length} lines</span>
                                {/if}
                              </div>
                              <div class="log-tail-content">
                                {#if logState.loading}
                                  <div class="log-line muted">Loading log...</div>
                                {:else if logState.error}
                                  <div class="log-line error">{logState.error}</div>
                                {:else if !logState.lines || logState.lines.length === 0}
                                  <div class="log-line muted">No log lines recorded.</div>
                                {:else}
                                  {#each logState.lines as line}
                                    <div class="log-line">{line}</div>
                                  {/each}
                                {/if}
                              </div>
                            </div>
                          </td>
                        </tr>
                      {/if}
                    {:else}
                      {@const isExpanded = expandedThreadKey === thread.key}
                      <tr
                        class="flow-thread-row"
                        class:flow-row-active={thread.active}
                        class:flow-row-expanded={isExpanded}
                        onclick={() => selectFlowThread(thread)}
                      >
                        <td>
                          <div class="thread-main-line">
                            <span class="thread-chevron" aria-hidden="true">{isExpanded ? '▾' : '▸'}</span>
                            {#if mainLine.issueLabel}
                              <IssueReference
                                number={thread.issue || 0}
                                href={mainLine.issueUrl || thread.runs[thread.runs.length - 1].target_url}
                                label={mainLine.issueLabel}
                                title={mainLine.issueTitle}
                              />
                            {/if}
                            {#if mainLine.prLabel}
                              <a
                                class="pr-badge"
                                href={mainLine.prUrl}
                                target="_blank"
                                rel="noreferrer"
                                onclick={(e) => e.stopPropagation()}
                              >
                                {mainLine.prLabel}
                              </a>
                            {/if}
                            {#if !mainLine.issueLabel && !mainLine.prLabel && thread.runs.length > 0}
                              <a
                                class="pr-badge"
                                href={thread.runs[thread.runs.length - 1].target_url}
                                target="_blank"
                                rel="noreferrer"
                                onclick={(e) => e.stopPropagation()}
                              >
                                {thread.runs[thread.runs.length - 1].target}
                              </a>
                            {/if}
                            {#if mainLine.milestone}
                              <span class="badge milestone-badge" title="Milestone">{mainLine.milestone}</span>
                            {/if}
                          </div>
                        </td>
                        <td>
                          <span class="badge badge-neutral">
                            flow · {thread.run_count} {thread.run_count === 1 ? 'run' : 'runs'}
                          </span>
                        </td>
                        <td>
                          <div class="thread-actors">
                            {#each thread.actors as act}
                              <span class="badge repo-badge">{act}</span>
                            {/each}
                          </div>
                        </td>
                        <td>
                          <div class="thread-status-badges">
                            {#if thread.active}
                              <span class="badge badge-accent flow-live-badge">
                                {thread.active_label ? `active · ${thread.active_label}` : 'active'}
                              </span>
                            {/if}
                            <span class="badge badge-{thread.status === 'succeeded' ? 'success' : thread.status === 'failed' ? 'error' : thread.status === 'running' ? 'accent' : 'warning'}">
                              {thread.status}
                            </span>
                            {#if thread.total_denials > 0}
                              <span class="badge badge-error" title="Operations refused by server policy in this flow">
                                {thread.total_denials} denied
                              </span>
                            {/if}
                          </div>
                        </td>
                        <td>{@render outcomeBadge(thread.runs[thread.runs.length - 1])}</td>
                        <td class="muted">{thread.duration || '—'}</td>
                        <td class="muted text-sm">{formatUsage(thread.usage)}</td>
                        <td class="summary-cell" title={thread.summary}>{thread.summary || '—'}</td>
                        <td class="muted text-sm">{formatTime(thread.last_activity)}</td>
                        <td>
                          <div class="row-actions">
                            <button
                              class="btn btn-sm btn-secondary"
                              aria-expanded={isExpanded}
                              onclick={(e) => {
                                e.stopPropagation();
                                selectFlowThread(thread);
                              }}
                            >
                              {isExpanded ? 'Collapse' : `Runs (${thread.run_count})`}
                            </button>
                          </div>
                        </td>
                      </tr>

                      {#if isExpanded}
                        {@const flowTimelineEntries = buildFlowTimeline(thread, threadTimelines[thread.key])}
                        {@const constituentRuns = buildFlowConstituentRuns(thread, flowTimelineEntries)}
                        <tr class="flow-expand-row">
                          <td colspan="10">
                            <div class="flow-expanded-panel">
                              <!-- 1. Combined Flow Timeline at the top of the expanded flow -->
                              <div class="flow-timeline-section">
                                <div class="flow-section-header">
                                  <div class="flow-section-title">
                                    <strong>Combined Flow Timeline</strong>
                                    {#if mainLine.issueLabel}
                                      <span class="badge badge-neutral">{mainLine.issueLabel}</span>
                                    {/if}
                                    {#if mainLine.prLabel}
                                      <span class="badge badge-neutral">{mainLine.prLabel}</span>
                                    {/if}
                                    {#if mainLine.milestone}
                                      <span class="badge milestone-badge">{mainLine.milestone}</span>
                                    {/if}
                                    {#if thread.active}
                                      <span class="badge badge-accent">Live updating</span>
                                    {/if}
                                  </div>
                                  {#if threadTimelineLoading[thread.key]}
                                    <span class="muted text-sm">Refreshing timeline…</span>
                                  {:else if threadTimelineError[thread.key]}
                                    <span class="muted text-sm">{threadTimelineError[thread.key]}</span>
                                  {/if}
                                </div>

                                <div class="flow-timeline-list">
                                  {#each flowTimelineEntries as entry}
                                    {#if entry.entry_type === 'run_boundary'}
                                      <div class="timeline-run-boundary timeline-run-{entry.status}">
                                        <div class="timeline-run-boundary-header">
                                          <div class="timeline-run-boundary-meta">
                                            <span class="badge badge-neutral">{entry.run.kind}</span>
                                            <span class="badge repo-badge">{entry.run.family ? `${entry.run.actor} (${entry.run.family})` : entry.run.actor}</span>
                                            <code class="text-sm muted">{entry.run.id}</code>
                                            <span class="badge badge-{entry.status === 'succeeded' ? 'success' : entry.status === 'failed' ? 'error' : entry.status === 'running' ? 'accent' : 'warning'}">
                                              {entry.status}
                                            </span>
                                            {#if runOutcome(entry.run)}
                                              {@render outcomeBadge(entry.run)}
                                            {/if}
                                            {#if entry.duration && entry.duration !== '-'}
                                              <span class="muted text-sm">{entry.duration}</span>
                                            {/if}
                                          </div>
                                          <div class="timeline-run-boundary-actions">
                                            <span class="muted text-sm">
                                              {formatTime(entry.time)}{entry.ended ? ` → ${formatTime(entry.ended)}` : ' (in progress)'}
                                            </span>
                                            <button
                                              class="btn btn-sm btn-secondary"
                                              onclick={() => toggleHistoryLog(entry.run.id)}
                                            >
                                              {expandedLogs[entry.run.id] ? 'Hide Run Details' : 'Run Details & Log'}
                                            </button>
                                          </div>
                                        </div>

                                        <div class="timeline-run-boundary-events">
                                          {#each entry.events as ev}
                                            <div class="timeline-item timeline-{ev.type}">
                                              <div class="timeline-dot"></div>
                                              <div class="timeline-body">
                                                <div class="timeline-header">
                                                  <span class="timeline-title">
                                                    {#if ev.url}
                                                      <a href={ev.url} target="_blank" rel="noreferrer">{ev.title}</a>
                                                    {:else}
                                                      {ev.title}
                                                    {/if}
                                                  </span>
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
                                        </div>
                                      </div>
                                    {:else}
                                      {@const ev = entry.event}
                                      <div class="timeline-item timeline-standalone timeline-{ev.type}">
                                        <div class="timeline-dot"></div>
                                        <div class="timeline-body">
                                          <div class="timeline-header">
                                            <span class="timeline-title">
                                              {#if ev.url}
                                                <a href={ev.url} target="_blank" rel="noreferrer">{ev.title}</a>
                                              {:else}
                                                {ev.title}
                                              {/if}
                                            </span>
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
                                    {/if}
                                  {/each}
                                </div>
                              </div>

                              <!-- 2. Constituent Runs in this Flow -->
                              <div class="flow-runs-section">
                                <div class="flow-section-header">
                                  <strong>Constituent Runs ({constituentRuns.length})</strong>
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
                                    {#each constituentRuns as r (r.id)}
                                      <tr>
                                        <td class="font-mono text-sm">{r.id}</td>
                                        <td>
                                          <a class="pr-badge" href={r.target_url} target="_blank" rel="noreferrer">{r.target}</a>
                                        </td>
                                        <td><span class="badge badge-neutral">{r.kind}</span></td>
                                        <td><span class="badge repo-badge">{r.family ? `${r.actor} (${r.family})` : r.actor}</span>{#if r.spec} <span class="badge instance-badge" title="model">model {r.spec}</span>{/if}</td>
                                        <td>
                                          <span class="badge badge-{r.status === 'succeeded' ? 'success' : r.status === 'failed' ? 'error' : r.status === 'running' ? 'accent' : 'warning'}">
                                            {r.status}
                                          </span>
                                          {#if r.denials && r.denials.length > 0}
                                            <span class="badge badge-error" title="Operations the server's policy refused this run; expand the log to see them">
                                              {r.denials.length} denied
                                            </span>
                                          {/if}
                                        </td>
                                        <td>{@render outcomeBadge(r)}</td>
                                        <td class="muted">{r.duration || '—'}</td>
                                        <td class="muted text-sm">{formatUsage(r.usage)}</td>
                                        <td class="summary-cell" title={r.summary}>{r.summary || '—'}</td>
                                        <td class="muted text-sm">{formatTime(r.created)}</td>
                                        <td>
                                          <button
                                            class="btn btn-sm btn-secondary"
                                            onclick={() => toggleHistoryLog(r.id)}
                                            title="Expand parsed run log and policy denials"
                                          >
                                            {expandedLogs[r.id] ? 'Hide Log' : 'Log'}
                                          </button>
                                        </td>
                                      </tr>
                                      {#if expandedLogs[r.id]}
                                        {@const logState = expandedLogs[r.id]}
                                        <tr class="log-expand-row">
                                          <td colspan="11">
                                            {@render denialList(r.denials)}
                                            <div class="log-tail-box history-log-box">
                                              <div class="log-tail-header">
                                                <span>Run Log ({r.id} · {r.kind} · {r.actor})</span>
                                                {#if logState.lines}
                                                  <span class="muted text-sm">{logState.lines.length} lines</span>
                                                {/if}
                                              </div>
                                              <div class="log-tail-content">
                                                {#if logState.loading}
                                                  <div class="log-line muted">Loading log...</div>
                                                {:else if logState.error}
                                                  <div class="log-line error">{logState.error}</div>
                                                {:else if !logState.lines || logState.lines.length === 0}
                                                  <div class="log-line muted">No log lines recorded.</div>
                                                {:else}
                                                  {#each logState.lines as line}
                                                    <div class="log-line">{line}</div>
                                                  {/each}
                                                {/if}
                                              </div>
                                            </div>
                                          </td>
                                        </tr>
                                      {/if}
                                    {/each}
                                  </tbody>
                                </table>
                              </div>
                            </div>
                          </td>
                        </tr>
                      {/if}
                    {/if}
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
                    {status.health.repo_settings.after_pull.length} after-pull step{status.health.repo_settings.after_pull.length === 1 ? '' : 's'}{#if status.health.repo_settings.milestones_dir}; milestone plans in <code>{status.health.repo_settings.milestones_dir}</code>{/if}
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
              <option value="plan">Plan (Kickoff issue)</option>
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

  /* An outcome is outlined and carries a glyph, so it reads apart from the
     filled execution-status badge even without color. */
  .outcome-badge {
    gap: 4px;
    white-space: nowrap;
    border: 1px solid currentColor;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

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

  .flow-waiting-state {
    padding: 10px 12px;
    background: rgba(125, 125, 125, 0.04);
    border-radius: 6px;
    font-style: italic;
  }

  /* Flow Group styles */
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

  .row-actions {
    display: flex;
    gap: 6px;
    align-items: center;
  }

  .log-expand-row td {
    padding: 0 12px 12px;
    background: rgba(125, 125, 125, 0.03);
  }

  .history-log-box {
    margin-top: 6px;
    max-height: 280px;
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

  .denials-box {
    border-top: 1px solid var(--error-bg);
    padding: 8px 12px;
    font-size: 12px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .denials-header {
    color: var(--error);
    font-weight: 600;
  }

  .denial-line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    font-family: monospace;
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

  .history-table-container {
    max-height: 1320px;
    max-block-size: 1320px;
    overflow: auto;
  }

  .history-table-container thead {
    position: sticky;
    inset-block-start: 0;
    z-index: 1;
  }

  .history-table-container th {
    background: var(--card-bg);
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

  /* Flow-centric Run History styles */
  .flow-thread-row {
    cursor: pointer;
    transition: background-color 0.12s ease;
  }

  .flow-thread-row:hover {
    background: rgba(125, 125, 125, 0.06);
  }

  .flow-row-expanded {
    background: rgba(37, 99, 235, 0.06);
  }

  .flow-row-active {
    background: rgba(37, 99, 235, 0.09);
  }

  .flow-row-active td:first-child {
    box-shadow: inset 3px 0 0 var(--accent);
  }

  .thread-main-line {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .thread-chevron {
    display: inline-block;
    width: 14px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .thread-title {
    max-width: 200px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-weight: 500;
  }

  .milestone-badge {
    background: rgba(16, 185, 129, 0.15);
    color: var(--success);
    border: 1px solid rgba(16, 185, 129, 0.3);
  }

  .thread-actors, .thread-status-badges {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .flow-live-badge {
    font-weight: 600;
  }

  .flow-expand-row td {
    padding: 0;
    background: rgba(125, 125, 125, 0.03);
  }

  .flow-expanded-panel {
    padding: 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 20px;
    border-left: 3px solid var(--accent);
  }

  .flow-section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 12px;
    flex-wrap: wrap;
    gap: 8px;
  }

  .flow-section-title {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .flow-timeline-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding-left: 8px;
  }

  .timeline-run-boundary {
    border: 1px solid var(--border);
    border-left: 3px solid var(--primary);
    border-radius: 8px;
    background: var(--card-bg);
    overflow: hidden;
  }

  .timeline-run-succeeded {
    border-left-color: var(--success);
  }

  .timeline-run-failed {
    border-left-color: var(--error);
  }

  .timeline-run-running {
    border-left-color: var(--accent);
  }

  .timeline-run-boundary-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 8px 12px;
    background: rgba(125, 125, 125, 0.06);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
    gap: 8px;
  }

  .timeline-run-boundary-meta, .timeline-run-boundary-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .timeline-run-boundary-events {
    padding: 12px 16px 4px 24px;
  }

  .timeline-standalone {
    margin-left: 16px;
  }

  .constituent-runs-table {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
  }
</style>
