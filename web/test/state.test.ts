// State, polling-cadence, and action helpers for the web UI. Run with
// `npm test`: node --test, with Node stripping the types.
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildFlowActionPayload,
  buildFlowTimeline,
  buildRunActionPayload,
  filterHistoryRuns,
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
  selectDefaultActors,
  standaloneLiveRuns,
  toggleExpandedThread,
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
  const prePRRuns = [
    {
      id: 'r-20-attempt1',
      target: '#20',
      target_url: 'http://forge/issues/20',
      issue: 20,
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
    {
      id: 'r-20-attempt2',
      target: '#20',
      target_url: 'http://forge/issues/20',
      issue: 20,
      kind: 'implement',
      actor: 'claude',
      family: 'anthropic',
      status: 'running',
      active: true,
      created: '2026-11-01T14:15:00Z',
      duration: '2m0s',
      summary: '',
      denials: [],
    },
  ] as any[];

  const beforePRThreads = groupHistoryThreads(prePRRuns);
  assert.equal(beforePRThreads.length, 1);
  assert.equal(beforePRThreads[0].key, 'flow:issue:20');
  assert.equal(beforePRThreads[0].active, true);
  assert.deepEqual(beforePRThreads[0].runs.map((r) => r.id), ['r-20-attempt1', 'r-20-attempt2']);

  // User expands the pre-PR flow
  let expandedKey = toggleExpandedThread(null, beforePRThreads[0].key);
  assert.equal(expandedKey, 'flow:issue:20');

  // Now PR !21 is created and verify starts
  const afterPRRuns = [
    prePRRuns[0],
    {
      ...prePRRuns[1],
      pr: 21,
      pr_url: 'http://forge/pulls/21',
      status: 'succeeded',
      active: false,
      ended: '2026-11-01T14:25:00Z',
      summary: 'Opened !21',
    },
    {
      id: 'r-20-verify1',
      target: '!21',
      target_url: 'http://forge/pulls/21',
      issue: 20,
      pr: 21,
      pr_url: 'http://forge/pulls/21',
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
    ['r-20-attempt1', 'r-20-attempt2', 'r-20-verify1'],
  );

  // Selection is preserved across the pre-PR -> PR transition
  expandedKey = reconcileExpandedThread(expandedKey, beforePRThreads, afterPRThreads);
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

test('buildFlowTimeline combines all flow runs with explicit run boundaries and forge events', () => {
  const runs = [
    {
      id: 'r-imp-1',
      target: '#10',
      target_url: 'http://forge/issues/10',
      issue: 10,
      pr: 11,
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
      id: 'r-ver-1',
      target: '!11',
      target_url: 'http://forge/pulls/11',
      issue: 10,
      pr: 11,
      kind: 'verify',
      actor: 'agy',
      family: 'google',
      status: 'succeeded',
      created: '2026-11-01T10:30:00Z',
      ended: '2026-11-01T10:40:00Z',
      duration: '10m0s',
      summary: 'Approved !11',
      denials: [],
    },
  ] as any[];

  const [thread] = groupHistoryThreads(runs);
  const issueTimeline = {
    issue: 10,
    pr: 11,
    title: 'Threads in Run History',
    state: 'closed',
    url: 'http://forge/issues/10',
    events: [
      { type: 'issue_created', time: '2026-11-01T10:00:00Z', title: 'Issue #10 opened', actor: 'owner' },
      { type: 'run_started', time: '2026-11-01T10:10:00Z', title: 'implement run started (claude)', run_id: 'r-imp-1' },
      { type: 'pr_opened', time: '2026-11-01T10:20:00Z', title: 'PR !11 opened', actor: 'claude' },
      { type: 'run_ended', time: '2026-11-01T10:25:00Z', title: 'implement run succeeded (claude)', run_id: 'r-imp-1' },
      { type: 'comment', time: '2026-11-01T10:27:00Z', title: 'Comment by owner', detail: 'Please verify carefully', actor: 'owner' },
      { type: 'run_started', time: '2026-11-01T10:30:00Z', title: 'verify run started (agy)', run_id: 'r-ver-1' },
      { type: 'review', time: '2026-11-01T10:38:00Z', title: 'Review APPROVED on !11', detail: 'LGTM', actor: 'agy' },
      { type: 'run_ended', time: '2026-11-01T10:40:00Z', title: 'verify run succeeded (agy)', run_id: 'r-ver-1' },
      { type: 'pr_merged', time: '2026-11-01T10:45:00Z', title: 'PR !11 merged', actor: 'owner' },
    ],
  };

  const entries = buildFlowTimeline(thread, issueTimeline);
  // Expect:
  // 0: standalone issue_created
  // 1: run_boundary for r-imp-1 (containing run_started, pr_opened, run_ended)
  // 2: standalone comment between runs
  // 3: run_boundary for r-ver-1 (containing run_started, review, run_ended)
  // 4: standalone pr_merged
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

  assert.equal(entries[4].entry_type, 'event');
  if (entries[4].entry_type === 'event') {
    assert.equal(entries[4].event.type, 'pr_merged');
  }

  // Filtering threads preserves constituent runs
  const now = Date.parse('2026-11-01T11:00:00Z');
  const filtered = filterHistoryThreads([thread], { actor: 'agy', kind: 'verify', search: '!11' }, now);
  assert.equal(filtered.length, 1);
  assert.equal(filtered[0].runs.length, 2);
});

