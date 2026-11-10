<script lang="ts">
  // The web UI's layout and data: it loads status and history, polls live
  // runs, holds the state the views and dialogs share, and posts the
  // owner's actions. The views and dialogs are their own components.
  import { onMount } from 'svelte';
  import type { Live, UIStatusResponse, IssueTimeline, TimelineFlowLane, TimelineRun } from './types';
  import { emptyLive } from './now';
  import { lifecycleChange } from './lifecycle';
  import { buildMilestoneRunPayload, postAction, type FlowForm, type MilestoneRunForm, type RunForm } from './actions';
  import { RunHistoryState } from './history.svelte';
  import AfterPullModal from './AfterPullModal.svelte';
  import AppHeader from './AppHeader.svelte';
  import FlowModal from './FlowModal.svelte';
  import GCModal from './GCModal.svelte';
  import HealthChecklist from './HealthChecklist.svelte';
  import LiveRuns from './LiveRuns.svelte';
  import Milestones from './Milestones.svelte';
  import MilestoneRunModal from './MilestoneRunModal.svelte';
  import OwnerInbox from './OwnerInbox.svelte';
  import PushModal from './PushModal.svelte';
  import RunHistory from './RunHistory.svelte';
  import RunModal from './RunModal.svelte';
  import RunFlowPage from './RunFlowPage.svelte';
  import Stats from './Stats.svelte';
  import StopModal from './StopModal.svelte';
  import Timeline from './Timeline.svelte';
  import UpstreamForge from './UpstreamForge.svelte';
  import ViewTabs from './ViewTabs.svelte';
  import { parseRoute, runRouteHash, flowRouteHash, type View, type Route } from './routes';
  import {
    buildFlowActionPayload,
    buildRunActionPayload,
    isSameFamilyPair,
    nextPollDelayMs,
    parseIssueNumber,
    parseLivePollSnapshot,
    selectDefaultActors,
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
  let actionMessage = $state<{ type: 'success' | 'error'; text: string } | null>(null);

  // Modals
  let showPushModal = $state<boolean>(false);
  let showFlowModal = $state<boolean>(false);
  let showRunModal = $state<boolean>(false);
  let showStopModal = $state<boolean>(false);
  let showGCModal = $state<boolean>(false);
  let showMilestoneRunModal = $state<boolean>(false);
  // After-pull confirmation modal (§4.4): the steps from main's .bottown.toml
  let showAfterPullModal = $state<boolean>(false);
  let afterPullOutput = $state<string | null>(null);
  let actionLoading = $state<boolean>(false);

  // Stop modal state
  let stopRunId = $state<string>('');

  // GC modal state
  let gcIncludeFailed = $state<boolean>(false);

  // The flow and run forms' values, kept between openings
  let flowForm = $state<FlowForm>({ issue: '', implement: '', implementModel: '', verify: '', verifyModel: '', rounds: 3, timeout: '60m' });
  let runForm = $state<RunForm>({ kind: 'implement', target: '', actor: '', model: '', question: '', allowSameFamily: false });
  // The milestone run form, and the server's refusal shown in it
  let milestoneRunForm = $state<MilestoneRunForm>({ slug: '', agents: '', parallel: 2 });
  let milestoneRunError = $state<string | null>(null);

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

  let flowSameFamily = $derived(isSameFamilyPair(status?.actors, flowForm.implement, flowForm.verify));

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

  // The Milestones view starts flows and runs on an issue with the same
  // dialogs as the Live Runs card.
  function openFlowFor(issue: number) {
    flowForm.issue = issue;
    showFlowModal = true;
  }

  function openRunFor(issue: number) {
    runForm.kind = 'implement';
    runForm.target = issue;
    showRunModal = true;
  }

  function openMilestoneRunFor(slug: string) {
    milestoneRunForm.slug = slug;
    milestoneRunError = null;
    showMilestoneRunModal = true;
  }

  function openStop(id: string) {
    stopRunId = id;
    showStopModal = true;
  }

  function openGC(includeFailed: boolean) {
    gcIncludeFailed = includeFailed;
    showGCModal = true;
  }

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
      // Set default actor in forms if available and unset
      ({
        flowImplement: flowForm.implement,
        flowVerify: flowForm.verify,
        runActor: runForm.actor,
      } = selectDefaultActors(status?.actors, {
        flowImplement: flowForm.implement,
        flowVerify: flowForm.verify,
        runActor: runForm.actor,
      }));
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
      ({
        flowImplement: flowForm.implement,
        flowVerify: flowForm.verify,
        runActor: runForm.actor,
      } = selectDefaultActors(status?.actors, {
        flowImplement: flowForm.implement,
        flowVerify: flowForm.verify,
        runActor: runForm.actor,
      }));
      await loadHistory();
      milestonesRefresh++;
      statsRefresh++;
    } catch (e: any) {
      errorMessage = e.message || 'Unknown error refreshing';
    } finally {
      refreshing = false;
    }
  }

  // Run one owner action: one at a time, its error in the banner.
  async function act(fn: () => Promise<void>) {
    if (actionLoading) return;
    actionLoading = true;
    actionMessage = null;
    try {
      await fn();
    } catch (e: any) {
      actionMessage = { type: 'error', text: e.message };
    } finally {
      actionLoading = false;
    }
  }

  const handlePull = () =>
    act(async () => {
      const data = await postAction('/api/actions/pull', undefined, 'Pull failed');
      actionMessage = { type: 'success', text: `Pull succeeded: ${data.output || 'up to date'}` };
      await loadStatus(true);
      // Show the declared after-pull steps; they run only on confirmation.
      if (status?.health?.repo_settings?.after_pull?.length) {
        afterPullOutput = null;
        showAfterPullModal = true;
      }
    });

  const handleConfirmPush = () =>
    act(async () => {
      const data = await postAction('/api/actions/push', { head: status?.upstream?.head_sha }, 'Push failed');
      actionMessage = { type: 'success', text: `Push succeeded: ${data.output || 'pushed to origin'}` };
      showPushModal = false;
      await loadStatus(true);
    });

  const handleConfirmAfterPull = () =>
    act(async () => {
      afterPullOutput = null;
      const res = await fetch('/api/actions/after-pull', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ steps: status?.health?.repo_settings?.after_pull ?? [] }),
      });
      const data = await res.json();
      afterPullOutput = data.output || null;
      if (!res.ok || !data.success) {
        throw new Error(data.error || 'After-pull steps failed');
      }
      actionMessage = { type: 'success', text: 'After-pull steps succeeded' };
      await loadStatus(true);
    });

  function openAfterPull() {
    afterPullOutput = null;
    showAfterPullModal = true;
  }

  const handleLand = (number: number, headSHA: string) =>
    act(async () => {
      const data = await postAction('/api/actions/land', { number, head_sha: headSHA }, 'Landing failed');
      actionMessage = { type: 'success', text: data.output };
      await loadStatus(true);
      await loadHistory();
    });

  function handleStartFlow() {
    if (flowSameFamily) return;
    return act(async () => {
      const data = await postAction('/api/actions/flow', buildFlowActionPayload(flowForm), 'Start flow failed');
      actionMessage = { type: 'success', text: data.output };
      showFlowModal = false;
      milestonesRefresh++;
      await loadStatus(true);
      await loadHistory();
    });
  }

  const handleStartRun = () =>
    act(async () => {
      const data = await postAction('/api/actions/run', buildRunActionPayload(runForm), 'Start run failed');
      actionMessage = { type: 'success', text: data.output };
      showRunModal = false;
      milestonesRefresh++;
      await loadStatus(true);
      await loadHistory();
    });

  // A refused pool or parallelism stays in the dialog, with the parser's
  // message, for the owner to correct.
  const handleStartMilestoneRun = () =>
    act(async () => {
      milestoneRunError = null;
      let data;
      try {
        data = await postAction('/api/actions/milestone-run', buildMilestoneRunPayload(milestoneRunForm), 'Start milestone run failed');
      } catch (e: any) {
        milestoneRunError = e.message;
        return;
      }
      actionMessage = { type: 'success', text: data.output };
      showMilestoneRunModal = false;
      milestonesRefresh++;
      await loadStatus(true);
    });

  function handleConfirmStop() {
    if (!stopRunId) return;
    return act(async () => {
      const data = await postAction('/api/actions/stop', { id: stopRunId }, 'Stop run failed');
      actionMessage = { type: 'success', text: data.output || `Run ${stopRunId} stopped` };
      showStopModal = false;
      milestonesRefresh++;
      await loadStatus(true);
      await loadHistory();
    });
  }

  const handleConfirmGC = () =>
    act(async () => {
      const data = await postAction('/api/actions/gc', { failed: gcIncludeFailed }, 'GC failed');
      actionMessage = { type: 'success', text: data.output || 'Run cleanup succeeded' };
      showGCModal = false;
      await loadStatus(true);
      await loadHistory();
    });

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
        onFlow={openFlowFor}
        onRun={openRunFor}
        onMilestoneRun={openMilestoneRunFor}
        onMilestoneStop={openStop}
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
            {actionLoading}
            onPull={handlePull}
            onPush={() => (showPushModal = true)}
            onAfterPull={openAfterPull}
          />

          <HealthChecklist health={status.health} />
        </div>
      {:else}
        <div class="grid">
          <OwnerInbox
            inbox={status.inbox}
            inboxError={status.inbox_error}
            onGC={openGC}
            onStop={openStop}
            onLand={handleLand}
          />

          <LiveRuns
            live={liveView}
            now={nowTime}
            onStartFlow={() => (showFlowModal = true)}
            onStartRun={() => (showRunModal = true)}
            onGC={() => (showGCModal = true)}
          />
        </div>
      {/if}
    {/if}
  {/if}
</main>

{#if showPushModal && status?.upstream?.unpushed_commits}
  <PushModal
    commits={status.upstream.unpushed_commits}
    {actionLoading}
    onconfirm={handleConfirmPush}
    onclose={() => (showPushModal = false)}
  />
{/if}

{#if showFlowModal}
  <FlowModal
    actors={status?.actors}
    bind:form={flowForm}
    sameFamily={flowSameFamily}
    {actionLoading}
    onsubmit={handleStartFlow}
    onclose={() => (showFlowModal = false)}
  />
{/if}

{#if showRunModal}
  <RunModal
    actors={status?.actors}
    bind:form={runForm}
    {actionLoading}
    onsubmit={handleStartRun}
    onclose={() => (showRunModal = false)}
  />
{/if}

{#if showMilestoneRunModal}
  <MilestoneRunModal
    actors={status?.actors}
    bind:form={milestoneRunForm}
    error={milestoneRunError}
    {actionLoading}
    onsubmit={handleStartMilestoneRun}
    onclose={() => (showMilestoneRunModal = false)}
  />
{/if}

{#if showStopModal}
  <StopModal runId={stopRunId} {actionLoading} onconfirm={handleConfirmStop} onclose={() => (showStopModal = false)} />
{/if}

{#if showGCModal}
  <GCModal
    bind:includeFailed={gcIncludeFailed}
    {actionLoading}
    onconfirm={handleConfirmGC}
    onclose={() => (showGCModal = false)}
  />
{/if}

{#if showAfterPullModal}
  <AfterPullModal
    settings={status?.health?.repo_settings}
    output={afterPullOutput}
    {actionLoading}
    onconfirm={handleConfirmAfterPull}
    onclose={() => (showAfterPullModal = false)}
  />
{/if}

<style>
  /* The full viewport width, less an edge padding that grows with it:
     wide displays give History and the logs the room. */
  .container {
    padding: 24px clamp(16px, 3vw, 48px);
  }
</style>
