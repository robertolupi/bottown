<script lang="ts">
  // The web UI's layout and data: it loads status and history, polls live
  // runs, and holds the state shared by its read-only views.
  import { onMount } from 'svelte';
  import type { Live, UIStatusResponse } from './types';
  import { emptyLive } from './now';
  import { lifecycleChange } from './lifecycle';
  import { RunHistoryState } from './history.svelte';
  import AppHeader from './AppHeader.svelte';
  import HealthChecklist from './HealthChecklist.svelte';
  import LiveRuns from './LiveRuns.svelte';
  import Milestones from './Milestones.svelte';
  import OwnerInbox from './OwnerInbox.svelte';
  import RunHistory from './RunHistory.svelte';
  import RunFlowPage from './RunFlowPage.svelte';
  import Stats from './Stats.svelte';
  import Timeline from './Timeline.svelte';
  import UpstreamForge from './UpstreamForge.svelte';
  import ViewTabs from './ViewTabs.svelte';
  import { parseRoute, runRouteHash, flowRouteHash, type View, type Route } from './routes';
  import {
    nextPollDelayMs,
    parseLivePollSnapshot,
    statusDue,
  } from './state';

  let { pollInterval }: { pollInterval?: number } = $props();

  let status = $state<UIStatusResponse | null>(null);
  // Now's live view, as the server makes it (GET /api/live).
  let liveView = $state<Live>(emptyLive());
  // When status was last requested (ms), for the live poll's periodic reload.
  let statusLoadedAt = 0;
  let loading = $state<boolean>(true);
  let refreshing = $state<boolean>(false);
  let errorMessage = $state<string | null>(null);
  // Timer & Polling
  let nowTime = $state<number>(Date.now());
  // When live state last arrived (a live poll or a status load), for the
  // header's freshness chip, and the last poll's error while polls fail.
  let lastLiveOk = $state<number>(0);
  let liveError = $state<string | null>(null);

  // Run history: runs, filters, and threads
  const runHistory = new RunHistoryState();
  const live = () => ({ liveRuns: status?.live_runs, activeFlows: status?.active_flows });
  const loadHistory = (background = false) => runHistory.load(live, background);
  let historyThreads = $derived(runHistory.threads(status?.live_runs, status?.active_flows));

  // Routes: parsed from URL fragment
  const initialRoute = parseRoute(location.hash);
  let route = $state<Route>(initialRoute);
  let previousView = $state<View>(initialRoute.kind === 'view' ? initialRoute.view : 'now');
  let view = $derived<View>(route.kind === 'view' ? route.view : 'now');
  let milestonesRefresh = $state<number>(0);
  // Bumped when a run or flow starts or ends, to reload the Timeline and
  // the Stats.
  let timelineRefresh = $state<number>(0);
  let statsRefresh = $state<number>(0);

  async function loadStatus(isRefresh = false) {
    if (isRefresh) refreshing = true;
    else loading = true;
    errorMessage = null;
    statusLoadedAt = Date.now();

    try {
      const res = await fetch('/api/status');
      if (!res.ok) {
        throw new Error(`Failed to load status: ${res.statusText}`);
      }
      status = await res.json();
      lastLiveOk = Date.now();
    } catch (e: any) {
      errorMessage = e.message || 'Unknown error loading status';
    } finally {
      loading = false;
      refreshing = false;
    }
  }

  // loadLive reads the live view; a failure throws, for the poll to show
  // on the freshness chip.
  async function loadLive() {
    const res = await fetch('/api/live');
    if (!res.ok) throw new Error(`live poll: ${res.status} ${res.statusText}`);
    liveView = await res.json();
  }

  async function handleRefresh() {
    refreshing = true;
    errorMessage = null;
    statusLoadedAt = Date.now();
    try {
      const res = await fetch('/api/actions/refresh', { method: 'POST' });
      if (!res.ok) {
        throw new Error(`Failed to refresh status: ${res.statusText}`);
      }
      status = await res.json();
      await loadHistory();
      milestonesRefresh++;
      statsRefresh++;
    } catch (e: any) {
      errorMessage = e.message || 'Unknown error refreshing';
    } finally {
      refreshing = false;
    }
  }

  onMount(() => {
    const onHashChange = () => {
      const nextRoute = parseRoute(location.hash);
      if (route.kind === 'view') {
        previousView = route.view;
      }
      route = nextRoute;
    };
    window.addEventListener('hashchange', onHashChange);

    loadStatus();
    loadHistory();
    loadLive().catch(() => {});

    const timerInterval = setInterval(() => {
      nowTime = Date.now();
    }, 1000);

    let pollTimeout: any;
    let destroyed = false;

    async function poll() {
      if (destroyed) return;
      try {
        // Now shows the server's live view; History reconciles its threads
        // with the live runs and flows.
        const [res] = await Promise.all([fetch('/api/runs?state=live'), loadLive()]);
        if (!res.ok) throw new Error(`live poll: ${res.status} ${res.statusText}`);
        {
          const data = await res.json();
          const { liveRuns, liveChecks, activeFlows } = parseLivePollSnapshot(data);
          lastLiveOk = Date.now();
          liveError = null;

          if (status) {
            const change = lifecycleChange(status.live_runs, status.active_flows, liveRuns, activeFlows);

            status.live_runs = liveRuns;
            status.live_checks = liveChecks;
            status.active_flows = activeFlows;

            if (change.status || statusDue(statusLoadedAt, Date.now())) await loadStatus(true);
            if (change.status || change.history) {
              timelineRefresh++;
              statsRefresh++;
              await loadHistory(true);
            }
          }
        }
      } catch (e: any) {
        // The header's freshness chip turns stale; the error is its title.
        liveError = e?.message || String(e);
      } finally {
        if (!destroyed) {
          const delay = pollInterval ?? nextPollDelayMs(status?.live_runs, status?.active_flows, status?.live_checks);
          pollTimeout = setTimeout(poll, delay);
        }
      }
    }

    pollTimeout = setTimeout(poll, pollInterval ?? 2500);

    return () => {
      destroyed = true;
      window.removeEventListener('hashchange', onHashChange);
      clearInterval(timerInterval);
      clearTimeout(pollTimeout);
    };
  });
</script>

<main class="container">
  <AppHeader
    {status}
    {refreshing}
    onRefresh={handleRefresh}
    {lastLiveOk}
    {liveError}
    now={nowTime}
  />

  {#if errorMessage}
    <div class="banner banner-error">
      <span>{errorMessage}</span>
      <button class="btn btn-sm btn-secondary" onclick={() => loadStatus()}>Retry</button>
    </div>
  {/if}

  {#if route.kind === 'run' || route.kind === 'flow'}
    <RunFlowPage
      {route}
      {status}
      historyRuns={runHistory.runs}
      now={nowTime}
      backLabel={previousView.charAt(0).toUpperCase() + previousView.slice(1)}
      {pollInterval}
      onBack={() => {
        if (typeof window !== 'undefined' && window.history.length > 1) {
          window.history.back();
        } else {
          location.hash = `#/${previousView}`;
        }
      }}
    />
  {:else}
    <ViewTabs {view} />

    {#if view === 'milestones'}
      <Milestones
        refreshKey={milestonesRefresh}
      />
    {:else if view === 'timeline'}
      <Timeline
        refreshKey={timelineRefresh}
        now={nowTime}
        onRun={(run) => { location.hash = runRouteHash(run.id); }}
        onFlow={(lane) => { location.hash = flowRouteHash(lane.issue ?? 0, lane.pr); }}
      />
    {:else if view === 'stats'}
      <Stats refreshKey={statsRefresh} issuesLink={status?.links.issues ?? ''} pullsLink={status?.links.pulls ?? ''} />
    {:else if loading && !status}
      <div class="loading-state">Loading status...</div>
    {:else if status}
      {#if view === 'history'}
        <div class="grid">
          <RunHistory
            history={runHistory}
            threads={historyThreads}
            actors={status.actors}
            now={nowTime}
          />
        </div>
      {:else if view === 'repo'}
        <div class="grid">
          <UpstreamForge
            {status}
          />

          <HealthChecklist health={status.health} />
        </div>
      {:else}
        <div class="grid">
          <OwnerInbox
            inbox={status.inbox}
            inboxError={status.inbox_error}
          />

          <LiveRuns
            live={liveView}
            now={nowTime}
          />
        </div>
      {/if}
    {/if}
  {/if}
</main>

<style>
  /* The full viewport width, less an edge padding that grows with it:
     wide displays give History and the logs the room. */
  .container {
    padding: 24px clamp(16px, 3vw, 48px);
  }
</style>
