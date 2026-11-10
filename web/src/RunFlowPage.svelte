<script lang="ts">
  import CommandCopy from './CommandCopy.svelte';
  // One page for a run (`#/run/<id>`) and for a flow (`#/flow/<issue>`),
  // design §4. It shows target, title, kind, round, actor, model, elapsed,
  // Forgejo links, the phase bar, and either the run's full log or the flow's
  // Log, Timeline and Runs tabs.
  import { onMount } from 'svelte';
  import type {
    ActiveFlowInfo,
    Denial,
    FailureDescription,
    HistoryRunInfo,
    HistoryThread,
    IssueTimeline,
    LiveCheckInfo,
    LiveRunInfo,
    LogLine,
    LogLineKind,
    UIStatusResponse,
  } from './types';
  import {
    buildFlowConstituentRuns,
    buildFlowTimeline,
    describeRunFailure,
    formatThreadMainLine,
    groupHistoryThreads,
    parseIssueNumber,
    resolveItemIssueAndPR,
    retroWindowTitle,
    runMatchesFlow,
    updateLogTailFollow,
  } from './state';
  import { formatElapsed, formatTime, formatUsage } from './format';
  import { applyLog, logURL, type LogCursor, type LogResponse } from './logcursor';
  import { flowRouteHash, runRouteHash, type Route } from './routes';
  import DenialList from './DenialList.svelte';
  import FailureReason from './FailureReason.svelte';
  import FlowTimeline from './FlowTimeline.svelte';
  import LogLineView from './LogLineView.svelte';
  import OutcomeBadge from './OutcomeBadge.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import SummaryText from './SummaryText.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    route: Route;
    status: UIStatusResponse | null;
    historyRuns: HistoryRunInfo[];
    now: number;
    backLabel?: string;
    onBack?: () => void;
    pollInterval?: number;
  }

  let { route, status, historyRuns, now, backLabel = 'Now', onBack, pollInterval }: Props = $props();

  // Active tab for flow page: 'log' | 'timeline' | 'runs'
  let activeFlowTab = $state<'log' | 'timeline' | 'runs'>('log');

  // Log state
  let logLines = $state<LogLine[]>([]);
  let loadingLog = $state<boolean>(true);
  let logError = $state<string | null>(null);

  // Filters for log
  let selectedKind = $state<string>('');
  let filterText = $state<string>('');

  // Follow state
  let isFollowing = $state<boolean>(true);
  let programmaticScroll = false;

  // Flow timeline state
  let timelineData = $state<IssueTimeline | null>(null);
  let timelineLoading = $state<boolean>(false);
  let timelineError = $state<string | null>(null);

  // Derive threads
  let allThreads = $derived(groupHistoryThreads(historyRuns, status?.live_runs, status?.active_flows));

  // Resolved run info (for route.kind === 'run')
  let resolvedLiveRun = $derived<LiveRunInfo | undefined>(
    route.kind === 'run' ? status?.live_runs?.find((r) => r.id === route.id) : undefined
  );
  let resolvedLiveCheck = $derived<LiveCheckInfo | undefined>(
    route.kind === 'run' ? status?.live_checks?.find((c) => c.id === route.id) : undefined
  );
  let resolvedHistoryRun = $derived<HistoryRunInfo | undefined>(
    route.kind === 'run' ? historyRuns.find((r) => r.id === route.id) : undefined
  );

  // For flow route: find matching thread and active flow
  let flowThread = $derived.by<HistoryThread | null>(() => {
    if (route.kind !== 'flow') return null;
    const matching = allThreads.filter((t) => t.kind === 'flow' && t.issue === route.issue);
    if (matching.length === 0) return null;

    if (route.pr !== undefined) {
      return matching.find((t) => t.pr === route.pr) || null;
    }

    // route.pr is undefined:
    // If there is an explicit pre-PR thread (no PR), match it:
    const prePr = matching.find((t) => !t.pr);
    if (prePr) return prePr;

    // Otherwise match the active thread, or the latest thread:
    const active = matching.find((t) => t.active);
    if (active) return active;

    return matching[matching.length - 1];
  });

  let addressedPr = $derived<number | undefined>(
    route.kind === 'flow'
      ? (route.pr !== undefined ? route.pr : (flowThread?.pr ?? undefined))
      : undefined
  );

  let activeFlowInfo = $derived.by<ActiveFlowInfo | undefined>(() => {
    if (route.kind !== 'flow') return undefined;
    const flowCandidate = status?.active_flows?.find((f) => f.issue === route.issue);
    if (!flowCandidate) return undefined;

    // Check if any live run or check exists for this issue
    const issueLiveRun = status?.live_runs?.find((r) => resolveItemIssueAndPR(r).issue === route.issue);
    const issueLiveCheck = status?.live_checks?.find((c) => resolveItemIssueAndPR(c).issue === route.issue);

    if (issueLiveRun || issueLiveCheck) {
      const runMatches = issueLiveRun ? runMatchesFlow(issueLiveRun, route.issue, addressedPr) : false;
      const checkMatches = issueLiveCheck ? runMatchesFlow(issueLiveCheck, route.issue, addressedPr) : false;
      return runMatches || checkMatches ? flowCandidate : undefined;
    }

    // No live runs or checks right now (e.g. flow waiting between runs).
    if (flowThread?.active) return flowCandidate;
    if (flowThread && addressedPr === flowThread.pr) {
      const issueThreads = allThreads.filter((t) => t.kind === 'flow' && t.issue === route.issue);
      if (issueThreads.length > 0 && issueThreads[issueThreads.length - 1].key === flowThread.key) {
        return flowCandidate;
      }
    }
    return undefined;
  });

  let fallbackThread = $derived<HistoryThread>(
    route.kind === 'flow'
      ? {
          key: `flow:issue:${route.issue}${addressedPr ? `:pr:${addressedPr}` : ''}`,
          kind: 'flow',
          issue: route.issue,
          pr: addressedPr,
          status: 'running',
          runs: [],
          actors: [],
          total_denials: 0,
          run_count: 0,
          active: Boolean(activeFlowInfo),
          last_activity: '',
          duration: '',
          summary: '',
        }
      : {
          key: 'empty',
          kind: 'flow',
          status: 'running',
          runs: [],
          actors: [],
          total_denials: 0,
          run_count: 0,
          active: false,
          last_activity: '',
          duration: '',
          summary: '',
        }
  );

  let effectiveFlowThread = $derived<HistoryThread>(flowThread || fallbackThread);

  // Constituent runs for flow
  let flowTimelineEntries = $derived(
    route.kind === 'flow' ? buildFlowTimeline(effectiveFlowThread, timelineData) : []
  );

  let rawFlowConstituentRuns = $derived<HistoryRunInfo[]>(
    route.kind === 'flow' ? buildFlowConstituentRuns(effectiveFlowThread, flowTimelineEntries) : []
  );

  let flowConstituentRuns = $derived<HistoryRunInfo[]>(
    route.kind === 'flow'
      ? rawFlowConstituentRuns.filter((r) => runMatchesFlow(r, route.issue, addressedPr))
      : []
  );

  // Active run ID for log tab:
  // For run route: route.id.
  // For flow route: live run if any matching this thread, else last run of flow.
  let activeRunId = $derived.by<string | null>(() => {
    if (route.kind === 'run') return route.id;
    if (route.kind === 'flow') {
      const liveForFlow = status?.live_runs?.find((r) => runMatchesFlow(r, route.issue, addressedPr));
      if (liveForFlow) return liveForFlow.id;
      const liveCheckForFlow = status?.live_checks?.find((c) => runMatchesFlow(c, route.issue, addressedPr));
      if (liveCheckForFlow) return liveCheckForFlow.id;
      if (flowConstituentRuns.length > 0) {
        return flowConstituentRuns[flowConstituentRuns.length - 1].id;
      }
      if (flowThread && flowThread.runs.length > 0) {
        return flowThread.runs[flowThread.runs.length - 1].id;
      }
    }
    return null;
  });

  // Resolved run object for current activeRunId
  let currentRunObj = $derived.by<any>(() => {
    if (!activeRunId) return null;
    return (
      status?.live_runs?.find((r) => r.id === activeRunId) ||
      status?.live_checks?.find((c) => c.id === activeRunId) ||
      historyRuns.find((r) => r.id === activeRunId) ||
      flowConstituentRuns.find((r) => r.id === activeRunId) ||
      null
    );
  });

  // Denials for the current active run
  let currentDenials = $derived<Denial[]>(currentRunObj?.denials || []);

  // Liveness derived from the selected activeRunId (for both run and flow routes)
  let isRunLive = $derived.by<boolean>(() => {
    if (!activeRunId) return false;
    return Boolean(
      status?.live_runs?.some((r) => r.id === activeRunId) ||
      status?.live_checks?.some((c) => c.id === activeRunId)
    );
  });

  // Known run / flow detection for empty states
  let isKnownRun = $derived.by<boolean>(() => {
    if (route.kind !== 'run') return true;
    return Boolean(
      resolvedLiveRun ||
      resolvedLiveCheck ||
      resolvedHistoryRun ||
      currentRunObj ||
      logLines.length > 0
    );
  });

  let isKnownFlow = $derived.by<boolean>(() => {
    if (route.kind !== 'flow') return true;
    return Boolean(
      flowThread ||
      activeFlowInfo ||
      (timelineData && timelineData.events && timelineData.events.length > 0)
    );
  });

  // Failure description for the resolved run
  let runFailure = $derived<FailureDescription | null>(
    currentRunObj ? describeRunFailure(currentRunObj) : null
  );

  // Filtered log lines
  let filteredLines = $derived(
    logLines.filter((l) => {
      if (selectedKind && l.kind !== selectedKind) return false;
      if (filterText) {
        const q = filterText.toLowerCase();
        const matchesText = l.text.toLowerCase().includes(q);
        const matchesTool = l.tool?.toLowerCase().includes(q) ?? false;
        if (!matchesText && !matchesTool) return false;
      }
      return true;
    })
  );

  // Target and title
  let headerTarget = $derived.by<string>(() => {
    if (route.kind === 'flow') return `#${route.issue}`;
    if (resolvedLiveRun) return resolvedLiveRun.target;
    if (resolvedLiveCheck) return resolvedLiveCheck.issue ? `#${resolvedLiveCheck.issue}` : resolvedLiveCheck.id;
    if (resolvedHistoryRun) return resolvedHistoryRun.target;
    if (route.kind === 'run') return route.id;
    return '';
  });

  let headerTitle = $derived.by<string>(() => {
    if (route.kind === 'flow') {
      return (
        timelineData?.title ||
        flowThread?.runs.find((r) => r.issue_title)?.issue_title ||
        currentRunObj?.issue_title ||
        status?.live_runs?.find((r) => runMatchesFlow(r, route.issue, addressedPr))?.issue_title ||
        ''
      );
    }
    return (
      resolvedLiveRun?.issue_title ||
      resolvedLiveCheck?.issue_title ||
      resolvedHistoryRun?.issue_title ||
      (resolvedHistoryRun?.window ? retroWindowTitle(resolvedHistoryRun.window) : '') ||
      ''
    );
  });

  let headerKind = $derived.by<string>(() => {
    if (route.kind === 'flow') return 'flow';
    if (resolvedLiveRun) return resolvedLiveRun.kind;
    if (resolvedLiveCheck) return resolvedLiveCheck.group === 'check' ? 'check' : 'checks';
    if (resolvedHistoryRun) return resolvedHistoryRun.kind;
    return 'run';
  });

  let headerRound = $derived.by<string | null>(() => {
    if (route.kind === 'flow') {
      if (activeFlowInfo) return `round ${activeFlowInfo.round}/${activeFlowInfo.max_rounds}`;
      return null;
    }
    if (resolvedLiveRun?.flow) {
      return `round ${resolvedLiveRun.flow.round}/${resolvedLiveRun.flow.max_rounds}`;
    }
    return null;
  });

  let headerActor = $derived.by<string | null>(() => {
    if (route.kind === 'flow') {
      if (activeFlowInfo) return `${activeFlowInfo.implement} → ${activeFlowInfo.verify}`;
      if (flowThread && flowThread.actors.length > 0) return flowThread.actors.join(', ');
      if (currentRunObj?.actor) return currentRunObj.actor;
      return null;
    }
    if (resolvedLiveRun) {
      return resolvedLiveRun.family ? `${resolvedLiveRun.actor} (${resolvedLiveRun.family})` : resolvedLiveRun.actor;
    }
    if (resolvedHistoryRun) {
      return resolvedHistoryRun.family
        ? `${resolvedHistoryRun.actor} (${resolvedHistoryRun.family})`
        : resolvedHistoryRun.actor;
    }
    return null;
  });

  let headerModel = $derived.by<string | null>(() => {
    return currentRunObj?.spec || null;
  });

  let headerMode = $derived.by<string | null>(() => {
    if (!currentRunObj) return null;
    if (typeof currentRunObj.interactive === 'boolean') {
      return currentRunObj.interactive ? 'interactive' : 'batch';
    }
    return currentRunObj.mode || null;
  });

  let headerElapsed = $derived.by<string | null>(() => {
    if (!currentRunObj) return null;
    if (isRunLive && currentRunObj.created) {
      const elapsed = formatElapsed(currentRunObj.created, now);
      return currentRunObj.timeout ? `${elapsed} / ${currentRunObj.timeout}` : elapsed;
    }
    if (currentRunObj.duration) {
      return currentRunObj.duration;
    }
    if (currentRunObj.elapsed) {
      return currentRunObj.timeout ? `${currentRunObj.elapsed} / ${currentRunObj.timeout}` : currentRunObj.elapsed;
    }
    return null;
  });

  // Associated flow issue for run
  let runFlowIssue = $derived.by<number | null>(() => {
    if (route.kind !== 'run') return null;
    if (resolvedLiveRun?.flow?.issue) return resolvedLiveRun.flow.issue;
    if (resolvedLiveRun?.issue) return resolvedLiveRun.issue;
    if (resolvedLiveCheck?.issue) return resolvedLiveCheck.issue;
    if (resolvedHistoryRun?.issue) return resolvedHistoryRun.issue;
    const parsed = parseIssueNumber(headerTarget);
    if (parsed !== null && parsed > 0) return parsed;
    return null;
  });

  let runFlowPr = $derived.by<number | undefined>(() => {
    if (route.kind !== 'run') return undefined;
    return resolvedLiveRun?.pr || resolvedLiveCheck?.pr || resolvedHistoryRun?.pr || undefined;
  });

  // Phase bar computation
  const PHASES = ['implement', 'checks', 'verify', 'land'] as const;

  let showPhaseBar = $derived(route.kind === 'flow' || Boolean(runFlowIssue));

  let currentPhaseIndex = $derived.by<number>(() => {
    const stepText = (
      activeFlowInfo?.step ||
      resolvedLiveRun?.flow?.step ||
      flowThread?.flow_state?.label ||
      ''
    ).toLowerCase();

    const kindText = headerKind.toLowerCase();

    if (stepText.includes('land') || stepText.includes('merge') || kindText === 'land') return 3;
    if (stepText.includes('verif') || kindText === 'verify') return 2;
    if (stepText.includes('check') || kindText.includes('check')) return 1;
    if (stepText.includes('implement') || kindText === 'implement') return 0;

    return 0;
  });

  // Forgejo links
  let issueNumber = $derived.by<number | null>(() => {
    if (route.kind === 'flow') return route.issue;
    return runFlowIssue;
  });

  let prNumber = $derived.by<number | null>(() => {
    if (route.kind === 'flow') return addressedPr ?? null;
    return runFlowPr ?? null;
  });

  let forgeIssueUrl = $derived.by<string | null>(() => {
    if (resolvedLiveRun?.issue_url) return resolvedLiveRun.issue_url;
    if (resolvedLiveCheck?.issue_url) return resolvedLiveCheck.issue_url;
    if (resolvedHistoryRun?.issue_url) return resolvedHistoryRun.issue_url;
    if (timelineData?.url) return timelineData.url;
    if (issueNumber && status?.links?.issues) return `${status.links.issues}/${issueNumber}`;
    return null;
  });

  let forgePrUrl = $derived.by<string | null>(() => {
    if (resolvedLiveRun?.pr_url) return resolvedLiveRun.pr_url;
    if (resolvedLiveCheck?.pr_url) return resolvedLiveCheck.pr_url;
    if (resolvedHistoryRun?.pr_url) return resolvedHistoryRun.pr_url;
    if (prNumber && status?.links?.pulls) return `${status.links.pulls}/${prNumber}`;
    return null;
  });

  // CLI command
  let commandText = $derived.by<string>(() => {
    if (route.kind === 'run') return `bottown run show ${route.id}`;
    if (route.kind === 'flow') return `bottown flow ${route.issue}`;
    return '';
  });

  // Scroll to bottom helper
  function scrollToBottom() {
    if (typeof window === 'undefined') return;
    programmaticScroll = true;
    const target = Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
    window.scrollTo({ top: target });
    setTimeout(() => {
      programmaticScroll = false;
    }, 50);
  }

  function handleFollowClick() {
    if (isFollowing) {
      isFollowing = false;
    } else {
      isFollowing = true;
      scrollToBottom();
    }
  }

  function handleWindowScroll() {
    if (programmaticScroll || typeof window === 'undefined') return;
    const scrollY = window.scrollY ?? window.pageYOffset ?? document.documentElement.scrollTop ?? 0;
    const scrollHeight = document.documentElement.scrollHeight;
    const clientHeight = window.innerHeight;
    const atBottom = scrollY + clientHeight >= scrollHeight - 30;
    isFollowing = updateLogTailFollow(isFollowing, atBottom, 'scroll');
  }

  function goBack() {
    if (onBack) {
      onBack();
    } else if (typeof window !== 'undefined' && window.history.length > 1) {
      window.history.back();
    } else {
      location.hash = '#/now';
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement || e.target instanceof HTMLSelectElement) {
      return;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      goBack();
    } else if (e.key === 'f') {
      e.preventDefault();
      handleFollowClick();
    } else if (e.key === 'g') {
      e.preventDefault();
      window.scrollTo({ top: 0 });
    } else if (e.key === 'G') {
      e.preventDefault();
      window.scrollTo({ top: document.documentElement.scrollHeight });
    }
  }

  // Load log. A poll asks only for the lines after the last stable prefix
  // (logcursor.ts); full loads the whole log, as when the run has ended.
  // Only the latest request's answer is applied.
  let logCursor: { id: string; cursor: LogCursor } | null = null;
  let logSeq = 0;
  async function loadLog(id: string, isPoll = false, full = false) {
    const seq = ++logSeq;
    if (!isPoll) loadingLog = true;
    logError = null;
    const cursor = logCursor?.id === id ? logCursor.cursor : null;
    try {
      const res = await fetch(logURL(id, cursor, full));
      if (seq !== logSeq) return;
      if (!res.ok) {
        // Unknown run id shows empty state, not error
        if (res.status === 404 || res.status === 400) {
          logLines = [];
          logCursor = null;
          return;
        }
        throw new Error(`Failed to load log: ${res.statusText}`);
      }
      const data: LogResponse = await res.json();
      if (seq !== logSeq) return;
      const next = applyLog(cursor, data, full);
      const hadNewLines = next.lines.length > logLines.length;
      logCursor = { id, cursor: next };
      logLines = next.lines;
      if (isFollowing && (hadNewLines || !isPoll)) {
        setTimeout(scrollToBottom, 20);
      }
    } catch {
      // Keep empty lines on network/other failures for unknown runs
      if (!isPoll && seq === logSeq) {
        logLines = [];
        logCursor = null;
      }
    } finally {
      if (seq === logSeq) loadingLog = false;
    }
  }

  // Load timeline for flow. A refresh (isPoll) keeps the current timeline on
  // screen and on failure; only the latest request's answer is applied.
  let timelineSeq = 0;
  async function loadTimelineData(issue: number, pr?: number, isPoll = false) {
    const seq = ++timelineSeq;
    if (!isPoll) {
      timelineLoading = true;
      timelineError = null;
    }
    try {
      const prParam = pr !== undefined ? `?pr=${pr}` : '';
      const res = await fetch(`/api/issues/${issue}/timeline${prParam}`);
      if (seq !== timelineSeq) return;
      if (!res.ok) {
        if (res.status === 404) {
          if (!isPoll) timelineData = null;
          return;
        }
        throw new Error(`Failed to load timeline for #${issue}`);
      }
      const data = await res.json();
      if (seq !== timelineSeq) return;
      timelineData = data;
      timelineError = null;
    } catch (e: any) {
      if (!isPoll && seq === timelineSeq) timelineError = e.message || 'Failed to load timeline';
    } finally {
      if (!isPoll && seq === timelineSeq) timelineLoading = false;
    }
  }

  function refreshTimeline() {
    if (route.kind === 'flow') void loadTimelineData(route.issue, addressedPr ?? 0, true);
  }

  // A flow is live while it is active or its selected run is.
  let isFlowLive = $derived(route.kind === 'flow' && (Boolean(activeFlowInfo) || isRunLive));

  // Reactive load on activeRunId change
  $effect(() => {
    const id = activeRunId;
    if (id) {
      void loadLog(id);
    } else {
      loadingLog = false;
      logLines = [];
    }
  });

  // Reactive load on flow route change. An undefined addressedPr is the
  // pre-PR thread, which the server scopes with pr=0 (no pr means its
  // latest PR).
  $effect(() => {
    if (route.kind === 'flow') {
      void loadTimelineData(route.issue, addressedPr ?? 0);
    }
  });

  // When the selected run ends, the last log poll may have missed its final
  // lines (the status poll can see it end first): fetch its whole log once
  // more, without a cursor, so the final view never rests on a prefix.
  // When a flow stops being live, refresh its timeline for the same reason.
  let lastLive: { id: string | null; live: boolean; flowLive: boolean } = { id: null, live: false, flowLive: false };
  $effect(() => {
    const id = activeRunId;
    const live = isRunLive;
    const flowLive = isFlowLive;
    if (id && id === lastLive.id && lastLive.live && !live) void loadLog(id, true, true);
    if (lastLive.flowLive && !flowLive) refreshTimeline();
    lastLive = { id, live, flowLive };
  });

  onMount(() => {
    window.addEventListener('scroll', handleWindowScroll, { passive: true });
    window.addEventListener('keydown', handleKeydown);

    let destroyed = false;
    let pollTimer: any;

    const interval = pollInterval ?? 2500;

    async function pollLog() {
      if (destroyed) return;
      if (activeRunId && isRunLive && (route.kind === 'run' || activeFlowTab === 'log')) {
        await loadLog(activeRunId, true);
      }
      // A live flow's timeline gains reviews, comments, checks and merges
      // as it goes; the Timeline and Runs tabs both read it.
      if (!destroyed && isFlowLive) refreshTimeline();
      if (!destroyed) {
        pollTimer = setTimeout(pollLog, interval);
      }
    }

    pollTimer = setTimeout(pollLog, interval);

    return () => {
      destroyed = true;
      clearTimeout(pollTimer);
      window.removeEventListener('scroll', handleWindowScroll);
      window.removeEventListener('keydown', handleKeydown);
    };
  });
</script>

<div class="run-flow-page">
  <!-- 1. Header -->
  <div class="run-flow-header card">
    <div class="header-breadcrumbs">
      <button class="btn btn-sm btn-secondary back-btn" onclick={goBack} aria-label="Go back">
        ← {backLabel}
      </button>
      <span class="breadcrumb-sep">/</span>
      <h2 class="header-title">
        {headerTarget}{headerTitle ? ` ${headerTitle}` : ''}
      </h2>
    </div>

    <div class="header-meta">
      <span class="badge badge-neutral">{headerKind}</span>
      {#if headerRound}
        <span class="badge badge-neutral">{headerRound}</span>
      {/if}
      {#if headerActor}
        <span class="badge repo-badge">{headerActor}</span>
      {/if}
      {#if headerModel}
        <span class="badge instance-badge" title="model">model {headerModel}</span>
      {/if}
      {#if headerMode}
        <span class="badge instance-badge">{headerMode}</span>
      {/if}
      {#if currentRunObj && (currentRunObj.status === 'failed' || currentRunObj.status === 'revoked' || currentRunObj.ended)}
        <OutcomeBadge run={currentRunObj} />
      {/if}
      {#if headerElapsed}
        <span class="meta-item muted">{headerElapsed}</span>
      {/if}
    </div>

    {#if showPhaseBar}
      <div class="phase-bar" role="progressbar" aria-label="Flow phases">
        {#each PHASES as phase, i}
          {#if i > 0}
            <div class="phase-connector" class:phase-connector-active={currentPhaseIndex >= i}></div>
          {/if}
          <div
            class="phase-step"
            class:phase-step-completed={currentPhaseIndex > i}
            class:phase-step-active={currentPhaseIndex === i}
          >
            [ {phase} ]
          </div>
        {/each}
      </div>
    {/if}

    <div class="header-actions">
      {#if commandText}
        <CommandCopy command={commandText} />
      {/if}

      <div class="forge-links">
        {#if forgeIssueUrl}
          <a href={forgeIssueUrl} target="_blank" rel="noreferrer" class="forge-link">
            Forgejo #{issueNumber} ↗
          </a>
        {/if}
        {#if prNumber && forgePrUrl}
          <a href={forgePrUrl} target="_blank" rel="noreferrer" class="forge-link">
            !{prNumber} ↗
          </a>
        {:else}
          <span class="muted text-sm">!—</span>
        {/if}
        {#if route.kind === 'run' && runFlowIssue}
          <a href={flowRouteHash(runFlowIssue, runFlowPr)} class="flow-page-link">
            Flow #{runFlowIssue} →
          </a>
        {/if}
      </div>
    </div>
  </div>

  <!-- 2. Failure Details for Run / Flow Log -->
  {#if runFailure && (route.kind === 'run' || activeFlowTab === 'log')}
    <div class="run-failure-section card" role="region" aria-label="Failure details">
      <div class="run-failure-header">
        <span class="badge badge-error">{currentRunObj?.status === 'revoked' ? 'Revoked' : 'Failed'}</span>
        <span class="failure-title">Failure details</span>
      </div>
      <div class="run-failure-body">
        <FailureReason failure={runFailure} expanded={true} />
      </div>
    </div>
  {/if}

  <!-- 3. Denials for Run -->
  {#if currentDenials.length > 0}
    <div class="denials-section">
      <DenialList denials={currentDenials} />
    </div>
  {/if}

  <!-- 4. Flow Tabs (only on Flow route) -->
  {#if route.kind === 'flow'}
    <div class="flow-tabs" role="tablist">
      <button
        role="tab"
        class="flow-tab"
        class:flow-tab-active={activeFlowTab === 'log'}
        aria-selected={activeFlowTab === 'log'}
        onclick={() => {
          activeFlowTab = 'log';
          if (activeRunId) void loadLog(activeRunId, true);
        }}
      >
        Log
      </button>
      <button
        role="tab"
        class="flow-tab"
        class:flow-tab-active={activeFlowTab === 'timeline'}
        aria-selected={activeFlowTab === 'timeline'}
        onclick={() => (activeFlowTab = 'timeline')}
      >
        Timeline
      </button>
      <button
        role="tab"
        class="flow-tab"
        class:flow-tab-active={activeFlowTab === 'runs'}
        aria-selected={activeFlowTab === 'runs'}
        onclick={() => (activeFlowTab = 'runs')}
      >
        Runs ({flowConstituentRuns.length})
      </button>
    </div>
  {/if}

  <!-- 4. Content Area -->
  {#if route.kind === 'flow' && activeFlowTab === 'timeline'}
    <!-- Timeline Tab -->
    <div class="flow-tab-pane card">
      <FlowTimeline
        thread={effectiveFlowThread}
        mainLine={formatThreadMainLine(effectiveFlowThread)}
        entries={flowTimelineEntries}
        loading={timelineLoading}
        error={timelineError ?? undefined}
      />
    </div>
  {:else if route.kind === 'flow' && activeFlowTab === 'runs'}
    <!-- Runs Tab -->
    <div class="flow-tab-pane card">
      <div class="table-container">
        <table class="data-table">
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
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {#if flowConstituentRuns.length === 0}
              <tr>
                <td colspan="11" class="muted text-center">No runs recorded for this flow.</td>
              </tr>
            {:else}
              {#each flowConstituentRuns as r (r.id)}
                {@const failure = describeRunFailure(r)}
                <tr>
                  <td class="font-mono text-sm">
                    <a href={runRouteHash(r.id)} class="run-id-link">
                      {r.id}
                    </a>
                  </td>
                  <td>
                    <TargetLink href={r.target_url} label={r.target} />
                  </td>
                  <td><span class="badge badge-neutral">{r.kind}</span></td>
                  <td>
                    <span class="badge repo-badge">{r.family ? `${r.actor} (${r.family})` : r.actor}</span>
                    {#if r.spec}
                      <span class="badge instance-badge" title="model">model {r.spec}</span>
                    {/if}
                  </td>
                  <td>
                    <StatusBadge status={r.status} />
                    {#if r.denials && r.denials.length > 0}
                      <span class="badge badge-error" title="Refused by server policy">
                        {r.denials.length} denied
                      </span>
                    {/if}
                  </td>
                  <td><OutcomeBadge run={r} /></td>
                  <td class="muted">{r.duration || '—'}</td>
                  <td class="muted text-sm">{formatUsage(r.usage)}</td>
                  {#if failure}
                    <td class="summary-cell">
                      <FailureReason
                        {failure}
                        onDetails={() => {
                          location.hash = runRouteHash(r.id);
                        }}
                      />
                    </td>
                  {:else}
                    <td class="summary-cell"><SummaryText text={r.summary} /></td>
                  {/if}
                  <td class="muted text-sm">{formatTime(r.created)}</td>
                  <td>
                    <a href={runRouteHash(r.id)} class="btn btn-sm btn-secondary">
                      View log
                    </a>
                  </td>
                </tr>
              {/each}
            {/if}
          </tbody>
        </table>
      </div>
    </div>
  {:else}
    <!-- Log View (for Run route, and for Flow 'log' tab) -->
    <div class="log-section">
      <div class="log-toolbar">
        <div class="log-filters">
          <select
            class="input input-sm select-sm"
            bind:value={selectedKind}
            aria-label="Filter lines by kind"
          >
            <option value="">All kinds</option>
            <option value="text">text</option>
            <option value="tool">tool</option>
            <option value="output">output</option>
            <option value="error">error</option>
            <option value="status">status</option>
            <option value="plain">plain</option>
          </select>
          <input
            type="search"
            class="input input-sm search-sm"
            placeholder="filter…"
            aria-label="Filter log text"
            bind:value={filterText}
          />
        </div>

        <div class="log-legend" aria-label="Log colors">
          <span class="lg-text" title="Assistant text">text</span>
          <span class="lg-shell" title="Tool call: shell command">shell</span>
          <span class="lg-read" title="Tool call: read a file or directory">read</span>
          <span class="lg-edit" title="Tool call: edit or write a file">edit</span>
          <span class="lg-search" title="Tool call: search files">search</span>
          <span class="lg-forge" title="Tool call: bottown and the forge">forge</span>
          <span class="lg-browser" title="Tool call: browser">browser</span>
          <span class="lg-output" title="Tool output">output</span>
          <span class="lg-error" title="Denied or failed tool call">error</span>
          <span class="lg-status" title="Session, background task and result events">status</span>
        </div>

        <div class="log-actions">
          {#if activeRunId && (!runFailure || runFailure.hasLog)}
            <a
              class="log-raw-link"
              href="/api/runs/{encodeURIComponent(activeRunId)}/log?raw=1"
              target="_blank"
              rel="noreferrer"
              title="Open the complete raw log with encoded payloads elided"
            >
              Raw log
            </a>
          {/if}
          <button
            type="button"
            class="log-follow-btn"
            class:following={isFollowing}
            class:paused={!isFollowing}
            onclick={handleFollowClick}
            title={isFollowing
              ? 'Automatically following latest output'
              : 'Jump to latest output and resume following'}
            aria-label={isFollowing ? 'Following output' : 'Follow output / Jump to latest'}
            data-action="follow"
          >
            {#if isFollowing}
              <span class="follow-dot" aria-hidden="true">●</span>
              <span>Following</span>
            {:else}
              <span class="follow-arrow" aria-hidden="true">↓</span>
              <span>Follow output<span class="sr-only"> / Jump to latest</span></span>
            {/if}
          </button>
        </div>
      </div>

      <!-- No inner scroll box, no max-height! -->
      <div class="run-page-log">
        {#if loadingLog && logLines.length === 0}
          <div class="log-line muted">Loading log...</div>
        {:else if !isKnownRun && route.kind === 'run'}
          <div class="empty-state">
            <p class="muted">No log or record found for run {route.id}.</p>
          </div>
        {:else if !isKnownFlow && route.kind === 'flow' && !activeRunId}
          <div class="empty-state">
            <p class="muted">No runs or timeline recorded for this flow.</p>
          </div>
        {:else if logLines.length === 0}
          <div class="empty-state">
            <p class="muted">
              {#if route.kind === 'run'}
                {#if runFailure && !runFailure.hasLog}
                  No log was written for run {route.id}.
                {:else if currentRunObj?.purged}
                  No log lines recorded for run {route.id}; run gc may have removed the log.
                {:else}
                  No log lines recorded for run {route.id}.
                {/if}
              {:else}
                No log lines recorded for this flow.
              {/if}
            </p>
          </div>
        {:else if filteredLines.length === 0}
          <div class="log-line muted">No lines match the filter.</div>
        {:else}
          {#each filteredLines as line, idx (idx)}
            <LogLineView {line} />
          {/each}
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .run-flow-page {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  /* Header card */
  .run-flow-header {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 16px 20px;
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    min-width: 0;
    box-sizing: border-box;
  }

  .header-breadcrumbs {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    min-width: 0;
  }

  .breadcrumb-sep {
    color: var(--text-muted);
    font-size: 16px;
  }

  .header-title {
    margin: 0;
    font-size: 18px;
    font-weight: 600;
    color: var(--text);
    word-break: break-word;
    overflow-wrap: anywhere;
    min-width: 0;
  }

  .header-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .meta-item {
    font-size: 13px;
  }

  /* Phase bar */
  .phase-bar {
    display: flex;
    align-items: center;
    gap: 4px;
    margin: 4px 0;
    flex-wrap: wrap;
    font-family: monospace;
    font-size: 13px;
  }

  .phase-step {
    padding: 2px 8px;
    border-radius: 4px;
    color: var(--text-muted);
    background: rgba(125, 125, 125, 0.08);
  }

  .phase-step-active {
    color: var(--primary);
    background: rgba(99, 102, 241, 0.15);
    font-weight: 600;
  }

  .phase-step-completed {
    color: var(--success);
    background: rgba(16, 185, 129, 0.12);
  }

  .phase-connector {
    width: 16px;
    height: 2px;
    background: var(--border);
  }

  .phase-connector-active {
    background: var(--success);
  }

  /* Header actions */
  .header-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    padding-top: 4px;
    border-top: 1px solid var(--border);
  }

  .forge-links {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }

  .forge-link, .flow-page-link {
    font-size: 13px;
    color: var(--primary);
    text-decoration: none;
    font-weight: 500;
  }

  .forge-link:hover, .flow-page-link:hover {
    text-decoration: underline;
  }

  /* Flow tabs */
  .flow-tabs {
    display: flex;
    gap: 6px;
    border-bottom: 1px solid var(--border);
    margin-bottom: 4px;
  }

  .flow-tab {
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    padding: 8px 16px;
    font-size: 14px;
    font-weight: 500;
    color: var(--text-muted);
    cursor: pointer;
    margin-bottom: -1px;
  }

  .flow-tab:hover {
    color: var(--text);
  }

  .flow-tab-active {
    color: var(--primary);
    border-bottom-color: var(--primary);
  }

  .flow-tab-pane {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 16px;
    min-width: 0;
    box-sizing: border-box;
  }

  /* Run failure section */
  .run-failure-section {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 14px 18px;
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-left: 4px solid var(--error);
    border-radius: 8px;
    min-width: 0;
    box-sizing: border-box;
  }

  .run-failure-header {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .failure-title {
    font-weight: 600;
    font-size: 13px;
    color: var(--text);
  }

  .run-failure-body {
    font-size: 13px;
    line-height: 1.5;
    min-width: 0;
  }

  .denials-section {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
  }

  /* Log section */
  .log-section {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    background: #090d16;
    min-width: 0;
    box-sizing: border-box;
  }

  .log-toolbar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 8px 12px;
    background: #111827;
    border-bottom: 1px solid #1e293b;
    gap: 12px;
    flex-wrap: wrap;
    color: #94a3b8;
    font-size: 11px;
  }

  .log-filters {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .search-sm {
    max-width: 120px;
    padding: 2px 6px;
    height: 24px;
    font-size: 11px;
  }

  .select-sm {
    padding: 2px 6px;
    height: 24px;
    font-size: 11px;
  }

  .log-legend {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    cursor: help;
  }

  .lg-text { color: var(--log-text); }
  .lg-shell { color: var(--log-tool-shell); }
  .lg-read { color: var(--log-tool-read); }
  .lg-edit { color: var(--log-tool-edit); }
  .lg-search { color: var(--log-tool-search); }
  .lg-forge { color: var(--log-tool-forge); }
  .lg-browser { color: var(--log-tool-browser); }
  .lg-output { color: var(--log-output); }
  .lg-error { color: var(--log-error); }
  .lg-status { color: var(--log-status); font-style: italic; }

  .log-actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .log-raw-link {
    color: #cbd5e1;
    text-decoration: none;
    font-size: 11px;
  }

  .log-raw-link:hover {
    text-decoration: underline;
  }

  .log-follow-btn {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 4px;
    background: rgba(125, 125, 125, 0.15);
    color: #94a3b8;
    border: 1px solid rgba(255, 255, 255, 0.08);
    display: inline-flex;
    align-items: center;
    gap: 5px;
    cursor: pointer;
    line-height: 1.4;
    font-family: inherit;
    transition: all 0.15s ease;
  }

  .log-follow-btn:hover {
    background: rgba(125, 125, 125, 0.25);
    color: #f1f5f9;
  }

  .log-follow-btn.following {
    background: rgba(16, 185, 129, 0.12);
    color: #34d399;
    border-color: rgba(16, 185, 129, 0.25);
  }

  .log-follow-btn.paused {
    background: rgba(59, 130, 246, 0.15);
    color: #60a5fa;
    border-color: rgba(59, 130, 246, 0.3);
    font-weight: 500;
  }

  .follow-dot {
    font-size: 8px;
    color: #10b981;
  }

  .follow-arrow {
    font-size: 11px;
    line-height: 1;
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

  /* Log content: in page's own scroll, NO inner scroll box, NO max-height */
  .run-page-log {
    background: #090d16;
    color: #e2e8f0;
    font-family: monospace;
    font-size: 12px;
    padding: 12px 16px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    max-height: none;
    overflow: visible;
    min-width: 0;
    box-sizing: border-box;
    word-break: break-all;
    overflow-wrap: anywhere;
  }

  .empty-state {
    padding: 24px;
    text-align: center;
  }

  .run-id-link {
    color: var(--primary);
    text-decoration: none;
    font-weight: 500;
  }

  .run-id-link:hover {
    text-decoration: underline;
  }

  .table-container {
    overflow-x: auto;
    width: 100%;
    min-width: 0;
  }

  .summary-cell {
    max-width: max(280px, 25vw);
  }

  @media (max-width: 600px) {
    .log-toolbar {
      flex-direction: column;
      align-items: flex-start;
    }
    .log-legend {
      width: 100%;
    }
    .log-actions {
      width: 100%;
      justify-content: space-between;
    }
  }
</style>
