<script lang="ts">
  // The web UI's layout and data: it loads status and history, polls live
  // runs, and holds the state shared by its read-only views.
  import { onMount } from 'svelte';
  import type { UIStatusResponse } from './types';
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
  import KeyboardHelp from './KeyboardHelp.svelte';
  import { parseRoute, runRouteHash, flowRouteHash, routeHash, type View, type Route } from './routes';
  import { keyIntent, getLogCommands } from './keys';
  import {
    nextPollDelayMs,
    parseLivePollSnapshot,
    statusDue,
  } from './state';

  let { pollInterval }: { pollInterval?: number } = $props();

  let status = $state<UIStatusResponse | null>(null);
  // When status was last requested (ms), for the live poll's periodic reload.
  let statusLoadedAt = 0;
  let loading = $state<boolean>(true);
  let refreshing = $state<boolean>(false);
  let errorMessage = $state<string | null>(null);
  // The `?` panel's keys (design §5).
  let helpOpen = $state<boolean>(false);
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
  // The keyboard's selection (design §5): the nav item j/k last moved to.
  // Its highlight is a class on the element itself, so this is not state
  // the view renders.
  let navSelected: HTMLElement | null = null;
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

  // The keyboard (design §5): keyIntent maps a key and these carry it
  // out. The selection is a class on the nav item, so it survives the
  // poll re-rendering it in place.
  function navItems(): HTMLElement[] {
    return Array.from(document.querySelectorAll<HTMLElement>('[data-nav]'));
  }

  function clearNavSelection() {
    navSelected?.classList.remove('nav-selected');
    navSelected = null;
  }

  function selectNav(el: HTMLElement) {
    if (navSelected && navSelected !== el) navSelected.classList.remove('nav-selected');
    navSelected = el;
    el.classList.add('nav-selected');
    el.scrollIntoView?.({ block: 'nearest' });
  }

  // moveSelection moves the selection by delta and returns whether there
  // was anything to move, so a key with no items is left to the page (the
  // arrows scroll a run's log).
  function moveSelection(delta: number): boolean {
    const items = navItems();
    if (items.length === 0) return false;
    const current = navSelected ? items.indexOf(navSelected) : -1;
    const next =
      current === -1
        ? delta > 0
          ? 0
          : items.length - 1
        : Math.min(items.length - 1, Math.max(0, current + delta));
    selectNav(items[next]);
    return true;
  }

  // openSelection returns the nav item Enter should open. Focus decides:
  // a focused nav item wins (Tab reached it), and any other focused
  // control (a button) keeps Enter for itself. The j/k selection is used
  // only when nothing but the body has focus.
  function openSelection(): HTMLElement | null {
    const focused = document.activeElement;
    if (focused instanceof HTMLElement && focused !== document.body) {
      return focused.matches('[data-nav]') ? focused : null;
    }
    return navSelected && document.contains(navSelected) ? navSelected : null;
  }

  function focusHistorySearch() {
    const focus = () => document.querySelector<HTMLInputElement>('[data-history-search]')?.focus();
    if (view === 'history') {
      focus();
      return;
    }
    location.hash = routeHash('history');
    setTimeout(focus, 0);
  }

  // Back: the entry before, or Now when there is none (a page opened by a
  // direct link).
  function goBack() {
    if (typeof window !== 'undefined' && window.history.length > 1) window.history.back();
    else location.hash = '#/now';
  }

  function handleKeydown(e: KeyboardEvent) {
    if (helpOpen && e.key === 'Escape') {
      e.preventDefault();
      helpOpen = false;
      return;
    }
    // A dialog on screen owns the keyboard: its own keys, and Esc, are
    // its to handle, whether or not focus is inside it.
    if (document.querySelector('[aria-modal="true"]')) return;
    const intent = keyIntent(e, route.kind === 'run' || route.kind === 'flow');
    if (!intent) return;
    switch (intent.kind) {
      case 'view':
        e.preventDefault();
        location.hash = routeHash(intent.view);
        break;
      case 'move':
        if (moveSelection(intent.delta)) e.preventDefault();
        break;
      case 'open':
        {
          const item = openSelection();
          if (item) {
            e.preventDefault();
            item.click();
          }
        }
        break;
      case 'back':
        e.preventDefault();
        goBack();
        break;
      case 'follow':
        e.preventDefault();
        getLogCommands()?.toggleFollow();
        break;
      case 'log-top':
        e.preventDefault();
        getLogCommands()?.toTop();
        break;
      case 'log-end':
        e.preventDefault();
        getLogCommands()?.toEnd();
        break;
      case 'search':
        e.preventDefault();
        focusHistorySearch();
        break;
      case 'help':
        e.preventDefault();
        helpOpen = true;
        break;
    }
  }

  onMount(() => {
    const onHashChange = () => {
      const nextRoute = parseRoute(location.hash);
      if (route.kind === 'view') {
        previousView = route.view;
      }
      route = nextRoute;
      clearNavSelection();
    };
    window.addEventListener('hashchange', onHashChange);

    // A nav item Tab lands on becomes the selection, so j/k carry on from
    // where Tab left off.
    const onFocusIn = (e: FocusEvent) => {
      const target = e.target;
      if (target instanceof HTMLElement && target.matches('[data-nav]')) selectNav(target);
    };
    window.addEventListener('focusin', onFocusIn);

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
      window.removeEventListener('focusin', onFocusIn);
      clearInterval(timerInterval);
      clearTimeout(pollTimeout);
    };
  });
</script>

<svelte:window onkeydown={handleKeydown} />

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
            liveRuns={status.live_runs}
            liveChecks={status.live_checks}
            activeFlows={status.active_flows}
            historyRuns={runHistory.runs}
            threads={historyThreads}
            now={nowTime}
          />
        </div>
      {/if}
    {/if}
  {/if}
</main>

{#if helpOpen}
  <KeyboardHelp onclose={() => (helpOpen = false)} />
{/if}

<style>
  /* The full viewport width, less an edge padding that grows with it:
     wide displays give History and the logs the room. */
  .container {
    padding: 24px clamp(16px, 3vw, 48px);
  }
</style>
