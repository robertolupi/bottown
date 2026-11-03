<script lang="ts">
  // The web UI's layout and data: it loads status and history, polls live
  // runs, holds the state the views and dialogs share, and posts the
  // owner's actions. The views and dialogs are their own components.
  import { onMount } from 'svelte';
  import type { UIStatusResponse, IssueTimeline } from './types';
  import { lifecycleChange } from './lifecycle';
  import { postAction, type FlowForm, type RunForm } from './actions';
  import { RunHistoryState } from './history.svelte';
  import AfterPullModal from './AfterPullModal.svelte';
  import AppHeader from './AppHeader.svelte';
  import FlowModal from './FlowModal.svelte';
  import GCModal from './GCModal.svelte';
  import HealthChecklist from './HealthChecklist.svelte';
  import LiveRuns from './LiveRuns.svelte';
  import Milestones from './Milestones.svelte';
  import OwnerInbox from './OwnerInbox.svelte';
  import PushModal from './PushModal.svelte';
  import RunHistory from './RunHistory.svelte';
  import RunModal from './RunModal.svelte';
  import StopModal from './StopModal.svelte';
  import TimelineModal from './TimelineModal.svelte';
  import UpstreamForge from './UpstreamForge.svelte';
  import ViewTabs from './ViewTabs.svelte';
  import {
    buildFlowActionPayload,
    buildRunActionPayload,
    isSameFamilyPair,
    nextPollDelayMs,
    parseIssueNumber,
    parseLivePollSnapshot,
    selectDefaultActors,
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

  // The flow and run forms' values, kept between openings
  let flowForm = $state<FlowForm>({ issue: '', implement: '', verify: '', rounds: 3, timeout: '60m' });
  let runForm = $state<RunForm>({ kind: 'implement', target: '', actor: '', question: '', allowSameFamily: false });

  // Timer & Polling
  let nowTime = $state<number>(Date.now());

  // Run history: runs, filters, the expanded flow and logs
  const runHistory = new RunHistoryState();
  const live = () => ({ liveRuns: status?.live_runs, activeFlows: status?.active_flows });
  const loadHistory = (background = false) => runHistory.load(live, background);
  let historyThreads = $derived(runHistory.threads(status?.live_runs, status?.active_flows));

  // Legacy modal timeline state
  let timelineData = $state<IssueTimeline | null>(null);
  let timelineLoading = $state<boolean>(false);

  let flowSameFamily = $derived(isSameFamilyPair(status?.actors, flowForm.implement, flowForm.verify));

  // Views: the overview (inbox, runs, health) and Milestones, kept in the
  // URL's fragment so a reload stays on the same one.
  let view = $state<'overview' | 'milestones'>(location.hash === '#milestones' ? 'milestones' : 'overview');
  let milestonesRefresh = $state<number>(0);

  function selectView(next: 'overview' | 'milestones') {
    view = next;
    history.replaceState(null, '', next === 'milestones' ? '#milestones' : location.pathname + location.search);
  }

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

    try {
      const res = await fetch('/api/status');
      if (!res.ok) {
        throw new Error(`Failed to load status: ${res.statusText}`);
      }
      status = await res.json();
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

  function handleConfirmStop() {
    if (!stopRunId) return;
    return act(async () => {
      const data = await postAction('/api/actions/stop', { id: stopRunId }, 'Stop run failed');
      actionMessage = { type: 'success', text: data.output || `Run ${stopRunId} stopped` };
      showStopModal = false;
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
            const prevThreads = runHistory.threads(status.live_runs, status.active_flows);
            const change = lifecycleChange(status.live_runs, status.active_flows, liveRuns, activeFlows);

            status.live_runs = liveRuns;
            status.active_flows = activeFlows;

            runHistory.reconcile(prevThreads, runHistory.threads(liveRuns, activeFlows));

            // A run or flow started or ended: refresh status and history
            // in the background without altering the expanded flow selection.
            if (change.status) await loadStatus(true);
            if (change.status || change.history) await loadHistory(true);

            // Live-update the selected active flow's timeline.
            if (runHistory.expandedThreadKey) {
              const currentThreads = runHistory.threads(status?.live_runs, status?.active_flows);
              const selected = currentThreads.find((t) => t.key === runHistory.expandedThreadKey);
              if (selected && selected.kind === 'flow' && (selected.active || change.status || change.history)) {
                await runHistory.loadTimeline(selected, true);
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

<main class="container">
  <AppHeader
    {status}
    {refreshing}
    onRefresh={() => { loadStatus(true); loadHistory(); milestonesRefresh++; }}
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

  <ViewTabs {view} onSelect={selectView} />

  {#if view === 'milestones'}
    <Milestones refreshKey={milestonesRefresh} onFlow={openFlowFor} onRun={openRunFor} />
  {:else if loading && !status}
    <div class="loading-state">Loading status...</div>
  {:else if status}
    <div class="grid">
      <OwnerInbox inbox={status.inbox} inboxError={status.inbox_error} onGC={openGC} onStop={openStop} />

      <LiveRuns
        liveRuns={status.live_runs}
        activeFlows={status.active_flows}
        issuesLink={status.links.issues}
        now={nowTime}
        onStartFlow={() => (showFlowModal = true)}
        onStartRun={() => (showRunModal = true)}
        onGC={() => (showGCModal = true)}
        onStop={openStop}
      />

      <RunHistory
        history={runHistory}
        threads={historyThreads}
        actors={status.actors}
        now={nowTime}
      />

      <UpstreamForge
        {status}
        {actionLoading}
        onPull={handlePull}
        onPush={() => (showPushModal = true)}
        onAfterPull={openAfterPull}
      />

      <HealthChecklist health={status.health} />
    </div>
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

{#if showTimelineModal}
  <TimelineModal timeline={timelineData} loading={timelineLoading} onclose={() => (showTimelineModal = false)} />
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
