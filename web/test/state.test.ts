// State, polling-cadence, and action helpers for the web UI. Run with
// `npm test`: node --test, with Node stripping the types.
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  aggregateThreadUsage,
  buildFlowActionPayload,
  buildFlowConstituentRuns,
  buildFlowTimeline,
  buildKeyedLogLines,
  buildRunActionPayload,
  computePreservedScrollTop,
  detectTrimmedLineCount,
  filterHistoryRuns,
  filterHistoryThreads,
  formatThreadMainLine,
  groupHistoryThreads,
  groupLiveFlows,
  isSameFamilyPair,
  isScrolledToBottom,
  nextPollDelayMs,
  parseIssueNumber,
  parseLivePollSnapshot,
  reconcileExpandedThread,
  resolveInboxAction,
  runOutcome,
  selectDefaultActors,
  standaloneLiveRuns,
  toggleExpandedThread,
  updateLogTailFollow,
} from '../src/state.ts';

const actors = [
  { name: 'claude', family: 'anthropic', harness: 'claude' },
  { name: 'claude-2', family: 'anthropic', harness: 'claude' },
  { name: 'agy', family: 'google', harness: 'agy' },
];

test('selectDefaultActors picks cross-family verifier and preserves existing selections', () => {
  assert.deepEqual(
    selectDefaultActors(actors, { flowImplement: '', flowVerify: '', runActor: '' }),
    { flowImplement: 'claude', flowVerify: 'agy', runActor: 'claude' },
  );

  assert.deepEqual(
    selectDefaultActors(actors, { flowImplement: 'agy', flowVerify: 'claude', runActor: 'agy' }),
    { flowImplement: 'agy', flowVerify: 'claude', runActor: 'agy' },
  );
});

test('isSameFamilyPair flags matching families and allows cross-family pairs', () => {
  assert.equal(isSameFamilyPair(actors, 'claude', 'claude-2'), true);
  assert.equal(isSameFamilyPair(actors, 'claude', 'agy'), false);
  assert.equal(isSameFamilyPair(actors, 'claude', ''), false);
});

test('groupLiveFlows pairs runs with active flows and falls back to run flow metadata', () => {
  const activeFlows = [
    { issue: 10, implement: 'claude', verify: 'agy', round: 1, max_rounds: 3, step: 'implement', label: '', waiting: false },
    { issue: 11, implement: 'agy', verify: 'claude', round: 2, max_rounds: 3, step: 'verify', label: '', waiting: true },
  ];
  const liveRuns = [
    { id: 'r-1', flow: { issue: 10, round: 1, max_rounds: 3, step: 'implement', label: '' } },
    { id: 'r-solo' },
  ] as any[];

  const grouped = groupLiveFlows(activeFlows, liveRuns);
  assert.equal(grouped.length, 2);
  assert.deepEqual(grouped[0].runs.map((r) => r.id), ['r-1']);
  assert.deepEqual(grouped[1].runs, []);

  const fallback = groupLiveFlows([], liveRuns);
  assert.equal(fallback.length, 1);
  assert.equal(fallback[0].flow.issue, 10);
  assert.deepEqual(fallback[0].runs.map((r) => r.id), ['r-1']);

  assert.deepEqual(standaloneLiveRuns(liveRuns).map((r) => r.id), ['r-solo']);
});

test('filterHistoryRuns filters by actor, kind, status, age window, and search text', () => {
  const now = Date.parse('2026-11-01T20:00:00Z');
  const runs = [
    {
      id: 'r-recent',
      target: '#42',
      kind: 'implement',
      actor: 'claude',
      status: 'succeeded',
      created: '2026-11-01T19:30:00Z',
      ended: '2026-11-01T19:45:00Z',
      summary: 'Implemented widget parser',
    },
    {
      id: 'r-old',
      target: '!43',
      kind: 'verify',
      actor: 'agy',
      status: 'failed',
      created: '2026-10-20T10:00:00Z',
      ended: '2026-10-20T10:15:00Z',
      summary: 'Requested changes on edge case',
    },
  ] as any[];

  assert.deepEqual(filterHistoryRuns(runs, { actor: 'claude' }, now).map((r) => r.id), ['r-recent']);
  assert.deepEqual(filterHistoryRuns(runs, { kind: 'verify' }, now).map((r) => r.id), ['r-old']);
  assert.deepEqual(filterHistoryRuns(runs, { status: 'failed' }, now).map((r) => r.id), ['r-old']);
  assert.deepEqual(filterHistoryRuns(runs, { date: '1h' }, now).map((r) => r.id), ['r-recent']);
  assert.deepEqual(filterHistoryRuns(runs, { search: 'WIDGET' }, now).map((r) => r.id), ['r-recent']);
  assert.deepEqual(filterHistoryRuns(runs, { search: '!43' }, now).map((r) => r.id), ['r-old']);
});

test('parseLivePollSnapshot and nextPollDelayMs handle active and idle polling states', () => {
  const fromArray = parseLivePollSnapshot([{ id: 'r-1' }]);
  assert.equal(fromArray.liveRuns.length, 1);
  assert.equal(fromArray.activeFlows.length, 0);
  assert.equal(nextPollDelayMs(fromArray.liveRuns, fromArray.activeFlows), 2500);

  const fromObject = parseLivePollSnapshot({ live_runs: [], active_flows: [] });
  assert.equal(nextPollDelayMs(fromObject.liveRuns, fromObject.activeFlows), 10000);
});

test('buildFlowActionPayload and buildRunActionPayload normalize action requests', () => {
  assert.deepEqual(
    buildFlowActionPayload({
      issue: '15',
      implement: 'claude',
      verify: 'agy',
      rounds: 0,
      timeout: '',
    }),
    {
      issue: 15,
      implement: 'claude',
      verify: 'agy',
      rounds: 3,
      timeout: '60m',
    },
  );

  assert.deepEqual(
    buildRunActionPayload({
      kind: 'implement',
      target: '15',
      actor: 'claude',
      question: 'ignored for implement',
      allowSameFamily: true,
    }),
    {
      kind: 'implement',
      target: 15,
      actor: 'claude',
      question: undefined,
      allow_same_family: undefined,
    },
  );

  assert.deepEqual(
    buildRunActionPayload({
      kind: 'verify',
      target: '16',
      actor: 'agy',
      question: 'ignored for verify',
      allowSameFamily: true,
    }),
    {
      kind: 'verify',
      target: 16,
      actor: 'agy',
      question: undefined,
      allow_same_family: true,
    },
  );

  assert.deepEqual(
    buildRunActionPayload({
      kind: 'discuss',
      target: '17',
      actor: 'claude',
      question: 'Should we split this package?',
      allowSameFamily: true,
    }),
    {
      kind: 'discuss',
      target: 17,
      actor: 'claude',
      question: 'Should we split this package?',
      allow_same_family: undefined,
    },
  );
});

test('resolveInboxAction and parseIssueNumber route inbox and timeline actions', () => {
  assert.deepEqual(
    resolveInboxAction({ target: 'r-123', action: { label: 'Clean up', command: 'bottown run gc --failed' } }),
    { modal: 'gc', includeFailed: true },
  );
  assert.deepEqual(
    resolveInboxAction({ target: 'r-123', action: { label: 'Clean up', command: 'bottown run gc' } }),
    { modal: 'gc', includeFailed: false },
  );
  assert.deepEqual(
    resolveInboxAction({ target: 'r-456', action: { label: 'Stop', command: 'bottown run stop r-456' } }),
    { modal: 'stop', stopRunId: 'r-456' },
  );
  assert.equal(resolveInboxAction({ target: '#10', action: undefined }), null);

  assert.equal(parseIssueNumber('#42'), 42);
  assert.equal(parseIssueNumber('!19'), 19);
  assert.equal(parseIssueNumber('invalid'), null);
});

test('groupHistoryThreads groups implement/verify loops by (issue, pr), separates distinct PRs, and keeps discuss separate', () => {
  const runs = [
    // Issue #10, PR !11: initial pre-PR failed attempt, retry that opened !11, verify requesting changes, resume implement, final verify
    {
      id: 'r-10-pre',
      target: '#10',
      target_url: 'http://forge/issues/10',
      issue: 10,
      issue_title: 'Threads in Run History',
      milestone: 'Iteration 2',
      thread_key: 'flow:issue:10',
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      status: 'failed',
      created: '2026-11-01T10:00:00Z',
      ended: '2026-11-01T10:05:00Z',
      duration: '5m0s',
      summary: 'Interrupted before PR',
      denials: [],
    },
    {
      id: 'r-10-imp1',
      target: '#10',
      target_url: 'http://forge/issues/10',
      issue: 10,
      pr: 11,
      pr_url: 'http://forge/pulls/11',
      milestone: 'Iteration 2',
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      status: 'succeeded',
      created: '2026-11-01T10:10:00Z',
      ended: '2026-11-01T10:25:00Z',
      duration: '15m0s',
      summary: 'Opened !11',
      denials: [],
    },
    {
      id: 'r-10-ver1',
      target: '!11',
      target_url: 'http://forge/pulls/11',
      issue: 10,
      pr: 11,
      pr_url: 'http://forge/pulls/11',
      milestone: 'Iteration 2',
      kind: 'verify',
      actor: 'agy',
      family: 'google',
      status: 'succeeded',
      created: '2026-11-01T10:30:00Z',
      ended: '2026-11-01T10:40:00Z',
      duration: '10m0s',
      summary: 'Requested changes on !11',
      denials: [{ time: '2026-11-01T10:35:00Z', op: 'git push', rule: 'verifier-no-push' }],
    },
    {
      id: 'r-10-imp2',
      target: '#10',
      target_url: 'http://forge/issues/10',
      issue: 10,
      pr: 11,
      pr_url: 'http://forge/pulls/11',
      milestone: 'Iteration 2',
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      status: 'succeeded',
      created: '2026-11-01T10:45:00Z',
      ended: '2026-11-01T10:55:00Z',
      duration: '10m0s',
      summary: 'Addressed review feedback',
      denials: [],
    },
    {
      id: 'r-10-ver2',
      target: '!11',
      target_url: 'http://forge/pulls/11',
      issue: 10,
      pr: 11,
      pr_url: 'http://forge/pulls/11',
      milestone: 'Iteration 2',
      kind: 'verify',
      actor: 'agy',
      family: 'google',
      status: 'succeeded',
      created: '2026-11-01T11:00:00Z',
      ended: '2026-11-01T11:10:00Z',
      duration: '10m0s',
      summary: 'Approved !11',
      denials: [],
    },
    // Standalone discuss run on #10
    {
      id: 'r-10-disc',
      target: '#10',
      target_url: 'http://forge/issues/10',
      issue: 10,
      milestone: 'Iteration 2',
      kind: 'discuss',
      actor: 'claude',
      family: 'anthropic',
      status: 'succeeded',
      created: '2026-11-01T11:30:00Z',
      ended: '2026-11-01T11:35:00Z',
      duration: '5m0s',
      summary: 'Answered design question',
      denials: [],
    },
    // Second PR !12 on the same issue #10 -> must form a separate flow thread
    {
      id: 'r-10-pr12-imp',
      target: '#10',
      target_url: 'http://forge/issues/10',
      issue: 10,
      pr: 12,
      pr_url: 'http://forge/pulls/12',
      milestone: 'Iteration 2',
      kind: 'implement',
      actor: 'agy',
      family: 'google',
      status: 'succeeded',
      created: '2026-11-01T12:00:00Z',
      ended: '2026-11-01T12:20:00Z',
      duration: '20m0s',
      summary: 'Follow-up PR !12',
      denials: [],
    },
    {
      id: 'r-10-pr12-ver',
      target: '!12',
      target_url: 'http://forge/pulls/12',
      issue: 10,
      pr: 12,
      pr_url: 'http://forge/pulls/12',
      milestone: 'Iteration 2',
      kind: 'verify',
      actor: 'claude',
      family: 'anthropic',
      status: 'succeeded',
      created: '2026-11-01T12:25:00Z',
      ended: '2026-11-01T12:35:00Z',
      duration: '10m0s',
      summary: 'Approved !12',
      denials: [],
    },
  ] as any[];

  const threads = groupHistoryThreads(runs);
  assert.equal(threads.length, 3);

  // Ordered newest activity first: flow !12, discuss, flow !11
  const pr12Thread = threads.find((t) => t.key === 'flow:issue:10:pr:12')!;
  const discussThread = threads.find((t) => t.kind === 'discuss')!;
  const pr11Thread = threads.find((t) => t.key === 'flow:issue:10:pr:11')!;

  assert.ok(pr12Thread);
  assert.equal(pr12Thread.run_count, 2);
  assert.deepEqual(pr12Thread.runs.map((r) => r.id), ['r-10-pr12-imp', 'r-10-pr12-ver']);
  assert.deepEqual(pr12Thread.actors, ['agy', 'claude']);

  assert.ok(discussThread);
  assert.equal(discussThread.key, 'discuss:r-10-disc');
  assert.equal(discussThread.run_count, 1);
  assert.deepEqual(discussThread.runs.map((r) => r.id), ['r-10-disc']);

  assert.ok(pr11Thread);
  assert.equal(pr11Thread.run_count, 5);
  assert.deepEqual(
    pr11Thread.runs.map((r) => r.id),
    ['r-10-pre', 'r-10-imp1', 'r-10-ver1', 'r-10-imp2', 'r-10-ver2'],
  );
  assert.equal(pr11Thread.total_denials, 1);
  assert.equal(pr11Thread.duration, '50m0s');
});

test('pre-PR runs group under issue before PR exists and preserve membership + selection when PR is created', () => {
  const prePRHistory = [
    {
      id: 'r-pre',
      target: '#20',
      target_url: 'http://forge/issues/20',
      issue: 20,
      thread_key: 'flow:issue:20',
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      status: 'failed',
      created: '2026-11-01T14:00:00Z',
      ended: '2026-11-01T14:10:00Z',
      duration: '10m0s',
      summary: 'First attempt failed',
      denials: [],
    },
  ] as any[];

  const prePRLive = [
    {
      id: 'r-open',
      target: '#20',
      target_url: 'http://forge/issues/20',
      issue: 20,
      thread_key: 'flow:issue:20',
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      harness: 'claude',
      interactive: false,
      created: '2026-11-01T14:15:00Z',
      expires: '2026-11-01T15:15:00Z',
      elapsed: '2m0s',
      timeout: '60m',
      log_tail: [],
      denials: [],
    },
  ] as any[];

  const beforePRThreads = groupHistoryThreads(prePRHistory, prePRLive);
  assert.equal(beforePRThreads.length, 1);
  assert.equal(beforePRThreads[0].key, 'flow:issue:20');
  assert.equal(beforePRThreads[0].active, true);
  assert.deepEqual(beforePRThreads[0].runs.map((r) => r.id), ['r-pre', 'r-open']);

  // User expands the pre-PR flow
  let expandedKey = toggleExpandedThread(null, beforePRThreads[0].key);
  assert.equal(expandedKey, 'flow:issue:20');

  // Live pre-PR -> PR transition: r-open opens PR !21 while still running, while
  // historyRuns still carries r-pre with backend thread_key "flow:issue:20".
  const liveAfterPROpened = [
    {
      ...prePRLive[0],
      pr: 21,
      pr_url: 'http://forge/pulls/21',
      thread_key: 'flow:issue:20:pr:21',
    },
  ] as any[];

  const livePRThreads = groupHistoryThreads(prePRHistory, liveAfterPROpened);
  assert.equal(livePRThreads.length, 1);
  assert.equal(livePRThreads[0].key, 'flow:issue:20:pr:21');
  assert.equal(livePRThreads[0].pr, 21);
  assert.equal(livePRThreads[0].active, true);
  assert.deepEqual(livePRThreads[0].runs.map((r) => r.id), ['r-pre', 'r-open']);

  // Selection immediately moves to the PR thread during the live poll
  expandedKey = reconcileExpandedThread(expandedKey, beforePRThreads, livePRThreads);
  assert.equal(expandedKey, 'flow:issue:20:pr:21');

  // Now r-open ends and verify starts
  const afterPRRuns = [
    prePRHistory[0],
    {
      id: 'r-open',
      target: '#20',
      target_url: 'http://forge/issues/20',
      issue: 20,
      pr: 21,
      pr_url: 'http://forge/pulls/21',
      thread_key: 'flow:issue:20:pr:21',
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      status: 'succeeded',
      active: false,
      created: '2026-11-01T14:15:00Z',
      ended: '2026-11-01T14:25:00Z',
      duration: '10m0s',
      summary: 'Opened !21',
      denials: [],
    },
    {
      id: 'r-20-verify1',
      target: '!21',
      target_url: 'http://forge/pulls/21',
      issue: 20,
      pr: 21,
      pr_url: 'http://forge/pulls/21',
      thread_key: 'flow:issue:20:pr:21',
      kind: 'verify',
      actor: 'agy',
      family: 'google',
      status: 'running',
      active: true,
      created: '2026-11-01T14:26:00Z',
      duration: '1m0s',
      summary: '',
      denials: [],
    },
  ] as any[];

  const afterPRThreads = groupHistoryThreads(afterPRRuns);
  assert.equal(afterPRThreads.length, 1);
  assert.equal(afterPRThreads[0].key, 'flow:issue:20:pr:21');
  assert.equal(afterPRThreads[0].pr, 21);
  assert.equal(afterPRThreads[0].active, true);
  assert.deepEqual(
    afterPRThreads[0].runs.map((r) => r.id),
    ['r-pre', 'r-open', 'r-20-verify1'],
  );

  // Selection stays on the PR thread
  expandedKey = reconcileExpandedThread(expandedKey, livePRThreads, afterPRThreads);
  assert.equal(expandedKey, 'flow:issue:20:pr:21');
});

test('toggleExpandedThread enforces at-most-one expanded flow andformatThreadMainLine formats issue, PR, and optional milestone', () => {
  let expanded: string | null = null;
  expanded = toggleExpandedThread(expanded, 'flow:issue:10:pr:11');
  assert.equal(expanded, 'flow:issue:10:pr:11');

  // Clicking another flow opens it and closes the previous flow
  expanded = toggleExpandedThread(expanded, 'flow:issue:10:pr:12');
  assert.equal(expanded, 'flow:issue:10:pr:12');

  // Clicking the open flow collapses it
  expanded = toggleExpandedThread(expanded, 'flow:issue:10:pr:12');
  assert.equal(expanded, null);

  // Main line with milestone assigned
  const withMilestone = formatThreadMainLine({
    key: 'flow:issue:10:pr:11',
    kind: 'flow',
    issue: 10,
    issue_title: 'Threads in Run History',
    pr: 11,
    milestone: 'Iteration 2',
    active: false,
    status: 'succeeded',
    run_count: 2,
    last_activity: '2026-11-01T11:00:00Z',
    actors: ['claude', 'agy'],
    duration: '20m0s',
    summary: 'Done',
    total_denials: 0,
    runs: [],
  });
  assert.equal(withMilestone.issueLabel, '#10');
  assert.equal(withMilestone.prLabel, '!11');
  assert.equal(withMilestone.milestone, 'Iteration 2');
  assert.ok(withMilestone.text.includes('#10'));
  assert.ok(withMilestone.text.includes('!11'));
  assert.ok(withMilestone.text.includes('Iteration 2'));

  // Main line when no milestone is assigned: milestone is omitted
  const withoutMilestone = formatThreadMainLine({
    key: 'flow:issue:30:pr:31',
    kind: 'flow',
    issue: 30,
    pr: 31,
    milestone: '',
    active: true,
    status: 'running',
    run_count: 1,
    last_activity: '2026-11-01T12:00:00Z',
    actors: ['claude'],
    duration: '5m0s',
    summary: '',
    total_denials: 0,
    runs: [],
  });
  assert.equal(withoutMilestone.issueLabel, '#30');
  assert.equal(withoutMilestone.prLabel, '!31');
  assert.equal(withoutMilestone.milestone, undefined);
  assert.equal(withoutMilestone.text, '#30 · !31');
});

test('buildFlowTimeline consumes backend type:"run" events by run_id, deduplicating ended runs and creating explicit boundaries for live runs', () => {
  const endedRuns = [
    {
      id: 'r-imp-1',
      target: '#10',
      target_url: 'http://forge/issues/10',
      issue: 10,
      pr: 11,
      pr_url: 'http://forge/pulls/11',
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      status: 'succeeded',
      created: '2026-11-01T10:10:00Z',
      ended: '2026-11-01T10:25:00Z',
      duration: '15m 00s',
      summary: 'Opened !11',
      denials: [],
    },
    {
      id: 'r-ver-1',
      target: '!11',
      target_url: 'http://forge/pulls/11',
      issue: 10,
      pr: 11,
      pr_url: 'http://forge/pulls/11',
      kind: 'verify',
      actor: 'agy',
      family: 'google',
      status: 'succeeded',
      created: '2026-11-01T10:30:00Z',
      ended: '2026-11-01T10:40:00Z',
      duration: '10m 00s',
      summary: 'Requested changes on !11',
      denials: [],
    },
  ] as any[];

  // Exact payload shape emitted by ComputeFlowTimeline in internal/webui/runs.go:
  // each ended or live run is emitted as `type: "run"` with `run_id`, `kind`, `status`, etc.
  const issueTimeline = {
    issue: 10,
    pr: 11,
    pr_url: 'http://forge/pulls/11',
    pr_title: 'Threads in Run History',
    milestone: 'Iteration 2',
    thread_key: 'flow:issue:10:pr:11',
    title: 'Threads in Run History',
    state: 'open',
    url: 'http://forge/issues/10',
    events: [
      {
        type: 'issue_created',
        time: '2026-11-01T10:00:00Z',
        title: 'Issue #10 opened',
        detail: 'Threads in Run History',
        actor: 'rlupi',
        url: 'http://forge/issues/10',
      },
      {
        type: 'run',
        time: '2026-11-01T10:10:00Z',
        ended: '2026-11-01T10:25:00Z',
        duration: '15m 00s',
        kind: 'implement',
        title: 'Run r-imp-1: implement by claude',
        detail: 'Opened !11',
        actor: 'claude',
        status: 'succeeded',
        run_id: 'r-imp-1',
        url: 'http://forge/issues/10',
      },
      {
        type: 'pr_opened',
        time: '2026-11-01T10:20:00Z',
        title: 'Pull request !11 opened',
        detail: 'Threads in Run History',
        actor: 'claude',
        url: 'http://forge/pulls/11',
      },
      {
        type: 'comment',
        time: '2026-11-01T10:27:00Z',
        title: 'Comment by rlupi',
        detail: 'Please verify carefully',
        actor: 'rlupi',
        url: 'http://forge/issues/10',
      },
      {
        type: 'run',
        time: '2026-11-01T10:30:00Z',
        ended: '2026-11-01T10:40:00Z',
        duration: '10m 00s',
        kind: 'verify',
        title: 'Run r-ver-1: verify by agy',
        detail: 'Requested changes on !11',
        actor: 'agy',
        status: 'succeeded',
        run_id: 'r-ver-1',
        url: 'http://forge/pulls/11',
      },
      {
        type: 'review',
        time: '2026-11-01T10:38:00Z',
        title: 'Review on !11: REQUEST_CHANGES',
        detail: 'Fix timeline events',
        actor: 'agy',
        status: 'REQUEST_CHANGES',
        url: 'http://forge/pulls/11',
      },
      {
        type: 'run',
        time: '2026-11-01T10:45:00Z',
        duration: '',
        kind: 'implement',
        title: 'Run r-imp-2: implement by claude',
        detail: 'Run r-imp-2 (implement)',
        actor: 'claude',
        status: 'live',
        run_id: 'r-imp-2',
        url: 'http://forge/issues/10',
      },
      {
        type: 'comment',
        time: '2026-11-01T10:48:00Z',
        title: 'Comment by claude',
        detail: 'Addressing review feedback',
        actor: 'claude',
        url: 'http://forge/issues/10',
      },
    ],
  };

  // Case 1: thread built from endedRuns only; live run arrives via ComputeFlowTimeline `type: "run"` event.
  const [threadFromEnded] = groupHistoryThreads(endedRuns);
  const entries = buildFlowTimeline(threadFromEnded, issueTimeline);

  // Expect:
  // 0: standalone issue_created
  // 1: run_boundary for r-imp-1 (containing ONLY run_started, pr_opened, run_ended — no extra `run` event)
  // 2: standalone comment between runs
  // 3: run_boundary for r-ver-1 (containing ONLY run_started, review, run_ended — no extra `run` event)
  // 4: run_boundary for live run r-imp-2 (status: 'running', ended: undefined, containing run_started and comment)
  assert.equal(entries.length, 5);
  assert.equal(entries[0].entry_type, 'event');
  if (entries[0].entry_type === 'event') {
    assert.equal(entries[0].event.type, 'issue_created');
  }

  assert.equal(entries[1].entry_type, 'run_boundary');
  if (entries[1].entry_type === 'run_boundary') {
    assert.equal(entries[1].run.id, 'r-imp-1');
    assert.deepEqual(
      entries[1].events.map((e) => e.type),
      ['run_started', 'pr_opened', 'run_ended'],
    );
  }

  assert.equal(entries[2].entry_type, 'event');
  if (entries[2].entry_type === 'event') {
    assert.equal(entries[2].event.type, 'comment');
  }

  assert.equal(entries[3].entry_type, 'run_boundary');
  if (entries[3].entry_type === 'run_boundary') {
    assert.equal(entries[3].run.id, 'r-ver-1');
    assert.deepEqual(
      entries[3].events.map((e) => e.type),
      ['run_started', 'review', 'run_ended'],
    );
  }

  assert.equal(entries[4].entry_type, 'run_boundary');
  if (entries[4].entry_type === 'run_boundary') {
    assert.equal(entries[4].run.id, 'r-imp-2');
    assert.equal(entries[4].status, 'running');
    assert.equal(entries[4].ended, undefined);
    assert.equal(entries[4].run.actor, 'claude');
    assert.equal(entries[4].run.family, 'anthropic');
    assert.deepEqual(
      entries[4].events.map((e) => e.type),
      ['run_started', 'comment'],
    );
  }

  // Constituent runs helper includes the live run from the timeline boundaries
  const constituentRuns = buildFlowConstituentRuns(threadFromEnded, entries);
  assert.deepEqual(
    constituentRuns.map((r) => r.id),
    ['r-imp-1', 'r-ver-1', 'r-imp-2'],
  );

  // Case 2: groupHistoryThreads also incorporates liveRuns directly into thread.runs,
  // and buildFlowTimeline deduplicates the live run by run_id against issueTimeline.
  const liveRuns = [
    {
      id: 'r-imp-2',
      target: '#10',
      target_url: 'http://forge/issues/10',
      issue: 10,
      pr: 11,
      pr_url: 'http://forge/pulls/11',
      thread_key: 'flow:issue:10:pr:11',
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      harness: 'claude',
      interactive: false,
      created: '2026-11-01T10:45:00Z',
      expires: '2026-11-01T11:45:00Z',
      elapsed: '4m 10s',
      timeout: '60m',
      log_tail: [],
      denials: [],
    },
  ] as any[];

  const [threadWithLive] = groupHistoryThreads(endedRuns, liveRuns);
  assert.equal(threadWithLive.active, true);
  assert.equal(threadWithLive.run_count, 3);
  assert.deepEqual(
    threadWithLive.runs.map((r) => r.id),
    ['r-imp-1', 'r-ver-1', 'r-imp-2'],
  );

  const entriesWithLive = buildFlowTimeline(threadWithLive, issueTimeline);
  assert.equal(entriesWithLive.length, 5);
  assert.equal(entriesWithLive[4].entry_type, 'run_boundary');
  if (entriesWithLive[4].entry_type === 'run_boundary') {
    assert.equal(entriesWithLive[4].run.id, 'r-imp-2');
    assert.equal(entriesWithLive[4].duration, '4m 10s');
    assert.deepEqual(
      entriesWithLive[4].events.map((e) => e.type),
      ['run_started', 'comment'],
    );
  }

  // Filtering threads preserves constituent runs
  const now = Date.parse('2026-11-01T11:00:00Z');
  const filtered = filterHistoryThreads([threadWithLive], { actor: 'agy', kind: 'verify', search: '!11' }, now);
  assert.equal(filtered.length, 1);
  assert.equal(filtered[0].runs.length, 3);
});

test('first-ever live flow preserves issue milestone and issue/PR titles when historyRuns is empty', () => {
  // Pre-PR live-only run on an issue with an issue-assigned milestone
  const prePRLiveOnly = [
    {
      id: 'r-live-pre',
      target: '#70',
      target_url: 'http://forge/issues/70',
      issue: 70,
      issue_url: 'http://forge/issues/70',
      issue_title: 'First-ever flow issue',
      milestone: 'Milestone Beta',
      thread_key: 'flow:issue:70',
      kind: 'implement',
      actor: 'agy',
      family: 'google',
      harness: 'agy',
      interactive: false,
      created: '2026-11-02T09:00:00Z',
      expires: '2026-11-02T10:00:00Z',
      elapsed: '1m 30s',
      timeout: '60m',
      log_tail: [],
      denials: [],
    },
  ] as any[];

  const [prePRThread] = groupHistoryThreads([], prePRLiveOnly);
  assert.equal(prePRThread.key, 'flow:issue:70');
  assert.equal(prePRThread.issue, 70);
  assert.equal(prePRThread.issue_title, 'First-ever flow issue');
  assert.equal(prePRThread.milestone, 'Milestone Beta');
  const preMain = formatThreadMainLine(prePRThread);
  assert.equal(preMain.milestone, 'Milestone Beta');
  assert.equal(preMain.text, '#70 First-ever flow issue · Milestone Beta');

  // Post-PR live-only run where the issue has a milestone and the PR has none (resolved from issue on backend)
  const postPRLiveOnly = [
    {
      ...prePRLiveOnly[0],
      pr: 71,
      pr_url: 'http://forge/pulls/71',
      pr_title: 'Implement #70',
      thread_key: 'flow:issue:70:pr:71',
    },
  ] as any[];

  const [postPRThread] = groupHistoryThreads([], postPRLiveOnly);
  assert.equal(postPRThread.key, 'flow:issue:70:pr:71');
  assert.equal(postPRThread.issue, 70);
  assert.equal(postPRThread.issue_title, 'First-ever flow issue');
  assert.equal(postPRThread.pr, 71);
  assert.equal(postPRThread.pr_title, 'Implement #70');
  assert.equal(postPRThread.milestone, 'Milestone Beta');
  const postMain = formatThreadMainLine(postPRThread);
  assert.equal(postMain.milestone, 'Milestone Beta');
  assert.equal(postMain.text, '#70 First-ever flow issue · !71 Implement #70 · Milestone Beta');
});

test('aggregateThreadUsage preserves undefined for unknown usage fields on partial usage metadata', () => {
  // Duration-only usage (e.g. harness did not report tokens, cost, or turns):
  // unknown fields must remain undefined, never fabricated zeroes.
  const durationOnlyRun = [
    {
      id: 'r-dur-only',
      target: '#80',
      target_url: 'http://forge/issues/80',
      issue: 80,
      kind: 'implement',
      actor: 'agy',
      family: 'google',
      harness: 'agy',
      mode: 'batch',
      status: 'succeeded',
      created: '2026-11-02T10:00:00Z',
      ended: '2026-11-02T10:01:00Z',
      duration: '1m 0s',
      summary: 'Implemented',
      usage: { duration_ms: 60000 },
      purged: false,
      denials: [],
    },
  ] as any[];

  const durationOnlyUsage = aggregateThreadUsage(durationOnlyRun);
  assert.deepEqual(durationOnlyUsage, { duration_ms: 60000 });
  assert.equal(durationOnlyUsage?.input_tokens, undefined);
  assert.equal(durationOnlyUsage?.output_tokens, undefined);
  assert.equal(durationOnlyUsage?.cache_read_tokens, undefined);
  assert.equal(durationOnlyUsage?.cache_write_tokens, undefined);
  assert.equal(durationOnlyUsage?.cost_usd, undefined);
  assert.equal(durationOnlyUsage?.turns, undefined);

  const [durationOnlyThread] = groupHistoryThreads(durationOnlyRun);
  assert.equal(durationOnlyThread.usage?.cost_usd, undefined);
  assert.equal(durationOnlyThread.usage?.input_tokens, undefined);
  assert.equal(durationOnlyThread.usage?.duration_ms, 60000);

  // Mixed partial usage across multiple runs sums reported fields and leaves unreported fields undefined
  const mixedRuns = [
    ...durationOnlyRun,
    {
      ...durationOnlyRun[0],
      id: 'r-tok-turns',
      kind: 'verify',
      actor: 'claude',
      family: 'anthropic',
      harness: 'claude',
      created: '2026-11-02T10:02:00Z',
      ended: '2026-11-02T10:03:00Z',
      usage: { input_tokens: 1500, output_tokens: 500, turns: 2, duration_ms: 45000 },
    },
  ] as any[];

  const mixedUsage = aggregateThreadUsage(mixedRuns);
  assert.deepEqual(mixedUsage, {
    input_tokens: 1500,
    output_tokens: 500,
    turns: 2,
    duration_ms: 105000,
  });
  assert.equal(mixedUsage?.cost_usd, undefined);
  assert.equal(mixedUsage?.cache_read_tokens, undefined);
  assert.equal(mixedUsage?.cache_write_tokens, undefined);

  // Empty usage object returns undefined
  assert.equal(
    aggregateThreadUsage([{ ...durationOnlyRun[0], usage: {} } as any]),
    undefined,
  );
});

test('runOutcome labels recorded outcomes apart from execution status, and keeps missing ones neutral', () => {
  const ended = '2026-11-01T10:40:00Z';
  const approved = runOutcome({ status: 'succeeded', outcome: 'approved', ended });
  const changes = runOutcome({ status: 'succeeded', outcome: 'changes_requested', ended });
  assert.equal(approved?.label, 'Approved');
  assert.equal(changes?.label, 'Changes requested');
  // Distinct in text and glyph, not only in tone.
  assert.notEqual(approved?.icon, changes?.icon);
  assert.notEqual(approved?.tone, changes?.tone);
  assert.match(changes?.title || '', /^Outcome: Changes requested/);
  assert.equal(runOutcome({ status: 'succeeded', outcome: 'pr_opened', ended })?.label, 'PR opened');
  assert.equal(runOutcome({ status: 'succeeded', outcome: 'commented', ended })?.label, 'Commented');

  // A failed run keeps its recorded verdict; status stays separate, and no
  // outcome uses the error tone that marks execution failures.
  assert.equal(runOutcome({ status: 'failed', outcome: 'changes_requested', ended })?.label, 'Changes requested');
  for (const o of ['approved', 'changes_requested', 'commented', 'pr_opened']) {
    assert.notEqual(runOutcome({ status: 'succeeded', outcome: o, ended })?.tone, 'error');
  }

  // No recorded outcome is explicit and neutral, whatever the summary says.
  const none = runOutcome({ status: 'succeeded', ended });
  assert.equal(none?.label, 'None recorded');
  assert.equal(none?.tone, 'neutral');
  assert.equal(runOutcome({ status: 'failed', ended })?.label, 'None recorded');
  // A run still going has no outcome yet.
  assert.equal(runOutcome({ status: 'running' }), null);
  // An unknown recorded value is shown as-is, neutrally.
  const unknown = runOutcome({ status: 'succeeded', outcome: 'merged_later', ended });
  assert.equal(unknown?.label, 'merged later');
  assert.equal(unknown?.tone, 'neutral');
});

test('buildFlowTimeline carries recorded outcomes from history runs and run events', () => {
  const thread = groupHistoryThreads([
    {
      id: 'r-ver-1', target: '!11', target_url: 'http://forge/pulls/11', issue: 10, pr: 11,
      kind: 'verify', actor: 'agy', family: 'google', harness: 'agy', mode: 'batch',
      status: 'succeeded', outcome: 'changes_requested',
      created: '2026-11-01T10:30:00Z', ended: '2026-11-01T10:40:00Z', duration: '10m', summary: '', purged: false, denials: [],
    },
  ] as any[])[0];
  const entries = buildFlowTimeline(thread, {
    issue: 10, pr: 11, title: 't', state: 'open', url: 'http://forge/issues/10',
    events: [
      { type: 'run', time: '2026-11-01T10:30:00Z', ended: '2026-11-01T10:40:00Z', kind: 'verify', title: 'Run r-ver-1', actor: 'agy', status: 'succeeded', outcome: 'changes_requested', run_id: 'r-ver-1' },
      { type: 'run', time: '2026-11-01T10:50:00Z', ended: '2026-11-01T11:00:00Z', kind: 'verify', title: 'Run r-ver-2', actor: 'agy', status: 'succeeded', outcome: 'approved', run_id: 'r-ver-2' },
    ],
  } as any);
  assert.deepEqual(
    buildFlowConstituentRuns(thread, entries).map((r) => [r.id, r.status, r.outcome]),
    [
      ['r-ver-1', 'succeeded', 'changes_requested'],
      ['r-ver-2', 'succeeded', 'approved'],
    ],
  );
});

test('detectTrimmedLineCount detects trimmed lines on append, sliding window, and disjoint updates', () => {
  // Empty or initial cases
  assert.equal(detectTrimmedLineCount([], []), 0);
  assert.equal(detectTrimmedLineCount([], ['line 1', 'line 2']), 0);
  assert.equal(detectTrimmedLineCount(['line 1', 'line 2'], []), 2);

  // Appending new output without trimming
  assert.equal(detectTrimmedLineCount(['line 1', 'line 2'], ['line 1', 'line 2']), 0);
  assert.equal(detectTrimmedLineCount(['line 1', 'line 2'], ['line 1', 'line 2', 'line 3']), 0);
  assert.equal(detectTrimmedLineCount(['line 1', 'line 2'], ['line 1', 'line 2', 'line 3', 'line 4']), 0);

  // Trimmed lines from the front
  assert.equal(
    detectTrimmedLineCount(
      ['line 1', 'line 2', 'line 3', 'line 4'],
      ['line 2', 'line 3', 'line 4', 'line 5'],
    ),
    1,
  );
  assert.equal(
    detectTrimmedLineCount(
      ['line 1', 'line 2', 'line 3', 'line 4'],
      ['line 3', 'line 4', 'line 5', 'line 6'],
    ),
    2,
  );
  assert.equal(
    detectTrimmedLineCount(
      ['line 1', 'line 2', 'line 3', 'line 4'],
      ['line 4', 'line 5', 'line 6', 'line 7'],
    ),
    3,
  );

  // Sliding window of 25 lines (e.g. backend retaining 25 lines)
  const window25Old = Array.from({ length: 25 }, (_, i) => `log line ${i}`);
  // 5 new lines arrive, first 5 trimmed
  const window25New = Array.from({ length: 25 }, (_, i) => `log line ${i + 5}`);
  assert.equal(detectTrimmedLineCount(window25Old, window25New), 5);

  // Repeated identical lines
  assert.equal(
    detectTrimmedLineCount(['repeat', 'repeat', 'repeat'], ['repeat', 'repeat', 'repeat', 'new']),
    0,
  );
  assert.equal(
    detectTrimmedLineCount(['repeat', 'repeat', 'repeat'], ['repeat', 'repeat', 'new']),
    1,
  );

  // Disjoint / completely new stream
  assert.equal(
    detectTrimmedLineCount(['old 1', 'old 2'], ['fresh 1', 'fresh 2']),
    2,
  );
});

test('computePreservedScrollTop preserves visual position and clamps to zero', () => {
  assert.equal(computePreservedScrollTop(200, 50), 150);
  assert.equal(computePreservedScrollTop(30, 50), 0);
  assert.equal(computePreservedScrollTop(100, 0), 100);
  assert.equal(computePreservedScrollTop(100, -20), 100);
});

test('isScrolledToBottom detects bottom and scrolled-away states', () => {
  // Content fits entirely within viewport
  assert.equal(isScrolledToBottom(0, 100, 180), true);
  assert.equal(isScrolledToBottom(0, 180, 180), true);

  // Content overflows viewport
  const scrollHeight = 600;
  const clientHeight = 180;
  const maxScroll = scrollHeight - clientHeight; // 420

  // At the exact bottom
  assert.equal(isScrolledToBottom(420, scrollHeight, clientHeight), true);

  // Within default threshold (16px)
  assert.equal(isScrolledToBottom(410, scrollHeight, clientHeight, 16), true);
  assert.equal(isScrolledToBottom(404, scrollHeight, clientHeight, 16), true);

  // Scrolled away
  assert.equal(isScrolledToBottom(400, scrollHeight, clientHeight, 16), false);
  assert.equal(isScrolledToBottom(100, scrollHeight, clientHeight, 16), false);
  assert.equal(isScrolledToBottom(0, scrollHeight, clientHeight, 16), false);
});

test('updateLogTailFollow transitions follow state on scroll and explicit click', () => {
  // Clicking follow always resumes following regardless of current scroll position
  assert.equal(updateLogTailFollow(false, false, 'follow_click'), true);
  assert.equal(updateLogTailFollow(true, false, 'follow_click'), true);

  // On scroll: resumes when at bottom, pauses when scrolled away
  assert.equal(updateLogTailFollow(true, false, 'scroll'), false);
  assert.equal(updateLogTailFollow(false, true, 'scroll'), true);
  assert.equal(updateLogTailFollow(true, true, 'scroll'), true);
});

test('buildKeyedLogLines generates monotonic keys across line trimming', () => {
  const initial = buildKeyedLogLines(['a', 'b', 'c'], 0);
  assert.deepEqual(initial, [
    { id: 0, text: 'a' },
    { id: 1, text: 'b' },
    { id: 2, text: 'c' },
  ]);

  // After trimming 1 line ('a'), baseIndex shifts to 1
  const afterTrim = buildKeyedLogLines(['b', 'c', 'd'], 1);
  assert.deepEqual(afterTrim, [
    { id: 1, text: 'b' },
    { id: 2, text: 'c' },
    { id: 3, text: 'd' },
  ]);

  // Keys for 'b' and 'c' remain stable (1 and 2)
  assert.equal(afterTrim[0].id, initial[1].id);
  assert.equal(afterTrim[1].id, initial[2].id);
});

test('simulated live log session: follows initially, pauses on scroll away, preserves reading position on trim, resumes on return to bottom or follow click', () => {
  // Session simulation
  let following = true;
  let baseIndex = 0;
  let currentLines: string[] = ['line 1', 'line 2', 'line 3'];
  let scrollTop = 0;
  const clientHeight = 100;
  const lineHeight = 20;

  // 1. Initial state: following is true, scrolled to latest line
  let scrollHeight = currentLines.length * lineHeight; // 60
  assert.equal(following, true);
  scrollTop = Math.max(0, scrollHeight - clientHeight); // 0 (fits in viewport)

  // 2. More lines arrive while following: automatically follows to bottom
  const nextBatch = ['line 1', 'line 2', 'line 3', 'line 4', 'line 5', 'line 6', 'line 7', 'line 8'];
  const trim1 = detectTrimmedLineCount(currentLines, nextBatch);
  assert.equal(trim1, 0);
  currentLines = nextBatch;
  scrollHeight = currentLines.length * lineHeight; // 160
  if (following) {
    scrollTop = scrollHeight - clientHeight; // 60
  }
  assert.equal(scrollTop, 60);
  assert.equal(isScrolledToBottom(scrollTop, scrollHeight, clientHeight), true);

  // 3. User scrolls away to inspect line 2
  scrollTop = 20; // Scrolled up to read line 2
  following = updateLogTailFollow(
    following,
    isScrolledToBottom(scrollTop, scrollHeight, clientHeight),
    'scroll',
  );
  assert.equal(following, false); // Paused following

  // 4. Incoming output arrives and older lines are trimmed (e.g. buffer sliding window)
  // New lines: lines 3 through 10 (lines 1 and 2 trimmed)
  const trimmedBatch = ['line 3', 'line 4', 'line 5', 'line 6', 'line 7', 'line 8', 'line 9', 'line 10'];
  const trim2 = detectTrimmedLineCount(currentLines, trimmedBatch);
  assert.equal(trim2, 2); // 2 lines trimmed
  baseIndex += trim2;

  // Since following is false, incoming events must NOT pull the user to bottom
  assert.equal(following, false);
  const trimmedHeight = trim2 * lineHeight; // 40px lost from top
  scrollTop = computePreservedScrollTop(scrollTop, trimmedHeight);
  // User was at scrollTop = 20; since 40px was trimmed above, reading position clamps to 0
  assert.equal(scrollTop, 0);
  assert.equal(following, false); // Still paused!

  // 5. User scrolls back to bottom manually
  scrollHeight = trimmedBatch.length * lineHeight; // 160
  scrollTop = scrollHeight - clientHeight; // 60
  following = updateLogTailFollow(
    following,
    isScrolledToBottom(scrollTop, scrollHeight, clientHeight),
    'scroll',
  );
  assert.equal(following, true); // Automatic following resumed!

  // 6. User scrolls away again, then clicks the Follow control
  scrollTop = 10;
  following = updateLogTailFollow(
    following,
    isScrolledToBottom(scrollTop, scrollHeight, clientHeight),
    'scroll',
  );
  assert.equal(following, false);

  // User explicitly clicks follow control
  following = updateLogTailFollow(following, false, 'follow_click');
  scrollTop = scrollHeight - clientHeight;
  assert.equal(following, true);
  assert.equal(scrollTop, 60);
});



