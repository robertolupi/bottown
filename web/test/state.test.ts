// State, polling-cadence, and action helpers for the web UI. Run with
// `npm test`: node --test, with Node stripping the types.
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  actorModelPlaceholder,
  actorSpec,
  buildFlowActionPayload,
  buildFlowConstituentRuns,
  buildFlowTimeline,
  buildKeyedLogLines,
  buildRunActionPayload,
  computePreservedScrollTop,
  detectTrimmedLineCount,
  filterHistoryRuns,
  filterInboxItems,
  filterHistoryThreads,
  groupHistoryThreads,
  groupLiveFlows,
  isSameFamilyPair,
  isScrolledToBottom,
  nextPollDelayMs,
  parseIssueNumber,
  parseLivePollSnapshot,
  resolveInboxAction,
  selectDefaultActors,
  standaloneLiveChecks,
  standaloneLiveRuns,
  statusDue,
  statusRefreshMs,
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

test('groupLiveFlows puts post-implement checks under their flow, and the rest stand alone', () => {
  const activeFlows = [
    { issue: 11, implement: 'agy', verify: 'claude', round: 1, max_rounds: 3, step: 'post-implement checks at 1a2b3c4d5e', label: '', waiting: false },
  ];
  const checks = [
    { id: 'flow-11-1a2b3c4d5e.e2e', issue: 11 },
    { id: 'flow-12-ffffffffff.e2e', issue: 12 },
  ] as any[];
  const grouped = groupLiveFlows(activeFlows, [], checks);
  assert.deepEqual(grouped[0].checks.map((c) => c.id), ['flow-11-1a2b3c4d5e.e2e']);
  assert.deepEqual(standaloneLiveChecks(checks, grouped).map((c) => c.id), ['flow-12-ffffffffff.e2e']);

  // Without active flows (a terminal's flow), a check joins the group its
  // flow's run metadata makes, if any.
  const runs = [{ id: 'r-1', flow: { issue: 12, round: 1, max_rounds: 3, step: 'implement', label: '' } }] as any[];
  const fallback = groupLiveFlows([], runs, checks);
  assert.deepEqual(fallback[0].checks.map((c) => c.id), ['flow-12-ffffffffff.e2e']);
  assert.deepEqual(standaloneLiveChecks(checks, fallback).map((c) => c.id), ['flow-11-1a2b3c4d5e.e2e']);
  assert.deepEqual(standaloneLiveChecks(undefined, fallback), []);
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
  assert.deepEqual(fromObject.liveChecks, []);

  // A flow's checks running alone keep the poll fast.
  const checking = parseLivePollSnapshot({ live_runs: [], live_checks: [{ id: 'flow-1-ab.e2e' }], active_flows: [] });
  assert.equal(checking.liveChecks.length, 1);
  assert.equal(nextPollDelayMs(checking.liveRuns, checking.activeFlows, checking.liveChecks), 2500);
});

test('statusDue reloads status that is never loaded or older than statusRefreshMs', () => {
  const now = 1_000_000;
  assert.equal(statusDue(0, now), true);
  assert.equal(statusDue(now, now), false);
  assert.equal(statusDue(now - statusRefreshMs + 1, now), false);
  assert.equal(statusDue(now - statusRefreshMs, now), true);
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

test('action payloads send a chosen model as ACTOR:SPEC and leave a blank one to the host', () => {
  assert.deepEqual(
    buildFlowActionPayload({
      issue: 15,
      implement: 'claude',
      implementModel: ' opus ',
      verify: 'crush',
      verifyModel: 'openrouter:~deepseek/latest',
      rounds: 2,
      timeout: '30m',
    }),
    {
      issue: 15,
      implement: 'claude:opus',
      verify: 'crush:openrouter:~deepseek/latest',
      rounds: 2,
      timeout: '30m',
    },
  );
  assert.equal(
    buildRunActionPayload({
      kind: 'implement',
      target: 15,
      actor: 'agy',
      model: '   ',
      question: '',
      allowSameFamily: false,
    }).actor,
    'agy',
  );
  assert.equal(
    buildRunActionPayload({
      kind: 'verify',
      target: 16,
      actor: 'agy',
      model: 'gemini-3.8-flash',
      question: '',
      allowSameFamily: false,
    }).actor,
    'agy:gemini-3.8-flash',
  );
  assert.equal(actorSpec('kimi', undefined), 'kimi');
});

test('actorModelPlaceholder shows the host default model or the harness default', () => {
  const withModels = [
    { name: 'agy', family: 'google', harness: 'agy', model: 'gemini-3.8-flash' },
    { name: 'claude', family: 'anthropic', harness: 'claude' },
  ];
  assert.equal(actorModelPlaceholder(withModels, 'agy'), 'default: gemini-3.8-flash');
  assert.equal(actorModelPlaceholder(withModels, 'claude'), 'harness default');
  assert.equal(actorModelPlaceholder(undefined, 'agy'), 'harness default');
});

test('filterInboxItems hides actor-filed issues unless requested', () => {
  const items = [
    { id: 'ready', kind: 'ready_to_land' },
    { id: 'filed', kind: 'actor_filed_issue' },
    { id: 'discuss', kind: 'discuss_answer' },
  ] as any[];

  assert.deepEqual(filterInboxItems(items, false).map((item) => item.id), ['ready', 'discuss']);
  assert.deepEqual(filterInboxItems(items, true).map((item) => item.id), ['ready', 'filed', 'discuss']);
  assert.deepEqual(filterInboxItems(undefined, false), []);
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
  assert.deepEqual(
    resolveInboxAction({
      target: '!19',
      head_sha: 'abc123',
      action: { label: 'Land', command: 'bottown land !19' },
    }),
    { action: 'land', number: 19, headSHA: 'abc123' },
  );
  assert.equal(
    resolveInboxAction({ target: '!19', action: { label: 'Land', command: 'bottown land !19' } }),
    null,
  );
  assert.equal(resolveInboxAction({ target: '#10', action: undefined }), null);

  assert.equal(parseIssueNumber('#42'), 42);
  assert.equal(parseIssueNumber('!19'), 19);
  assert.equal(parseIssueNumber('invalid'), null);
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

test('buildFlowTimeline shows run summaries only at completion without dropping matching events', () => {
  const thread = {
    key: 'flow:issue:10',
    kind: 'flow',
    issue: 10,
    active: true,
    runs: [
      {
        id: 'r-done', kind: 'implement', actor: 'claude', status: 'succeeded',
        created: '2026-11-01T10:00:00Z', ended: '2026-11-01T10:05:00Z',
        duration: '5m', summary: 'Implemented the change',
      },
      {
        id: 'r-failed', kind: 'implement', actor: 'claude', status: 'failed',
        created: '2026-11-01T10:10:00Z', ended: '2026-11-01T10:15:00Z',
        duration: '5m', summary: 'Blocked by failing checks',
        failure: { cause: 'reported', detail: 'the agent ended the run as failed' },
      },
      {
        id: 'r-active', kind: 'verify', actor: 'agy', status: 'running',
        created: '2026-11-01T10:20:00Z', duration: '', summary: '',
      },
    ],
  } as any;
  const issueTimeline = {
    issue: 10,
    events: [
      { type: 'run', time: '2026-11-01T10:00:00Z', ended: '2026-11-01T10:05:00Z', run_id: 'r-done', detail: 'Implemented the change' },
      { type: 'comment', time: '2026-11-01T10:02:00Z', title: 'Comment by owner', detail: 'Implemented the change' },
      { type: 'run', time: '2026-11-01T10:10:00Z', ended: '2026-11-01T10:15:00Z', run_id: 'r-failed', detail: 'Blocked by failing checks' },
      { type: 'run', time: '2026-11-01T10:20:00Z', run_id: 'r-active', status: 'live', detail: 'Run r-active (verify)' },
    ],
  } as any;

  const entries = buildFlowTimeline(thread, issueTimeline);
  const eventsFor = (id: string) => {
    const entry = entries.find((e) => e.entry_type === 'run_boundary' && e.run.id === id);
    if (!entry || entry.entry_type !== 'run_boundary') assert.fail(`missing run boundary ${id}`);
    return entry.events;
  };

  const done = eventsFor('r-done');
  assert.deepEqual(done.map((e) => e.type), ['run_started', 'comment', 'run_ended']);
  assert.equal(done[0].detail, undefined);
  assert.equal(done[1].detail, 'Implemented the change', 'an unrelated matching comment stays distinct');
  assert.equal(done[2].detail, 'Implemented the change');

  const failed = eventsFor('r-failed');
  assert.equal(failed[0].detail, undefined);
  assert.match(failed[1].detail || '', /Agent reported: Blocked by failing checks/);

  const active = eventsFor('r-active');
  assert.deepEqual(active.map((e) => e.type), ['run_started']);
  assert.equal(active[0].detail, undefined);

  assert.deepEqual(buildFlowTimeline(thread, issueTimeline), entries, 'a refresh does not add summary events');
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
    { id: 0, line: 'a' },
    { id: 1, line: 'b' },
    { id: 2, line: 'c' },
  ]);

  // After trimming 1 line ('a'), baseIndex shifts to 1
  const afterTrim = buildKeyedLogLines(['b', 'c', 'd'], 1);
  assert.deepEqual(afterTrim, [
    { id: 1, line: 'b' },
    { id: 2, line: 'c' },
    { id: 3, line: 'd' },
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

