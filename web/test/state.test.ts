// State, polling-cadence, and action helpers for the web UI. Run with
// `npm test`: node --test, with Node stripping the types.
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildFlowActionPayload,
  buildRunActionPayload,
  filterHistoryRuns,
  groupLiveFlows,
  isSameFamilyPair,
  nextPollDelayMs,
  parseIssueNumber,
  parseLivePollSnapshot,
  resolveInboxAction,
  selectDefaultActors,
  standaloneLiveRuns,
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
