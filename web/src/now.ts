// The Now view's text (monitor-ui design §3): how a tile, a row and a
// thread that just ended read, the inbox's counts and the header chips.
// What is live, how it groups and what is quiet are the server's (GET
// /api/live); these only word it.
import type { HealthChecklist, InboxItem, InboxItemKind, Live, LiveEnded, LiveItem, LiveRow, UIStatusResponse } from './types';
import { flowRouteHash, runRouteHash } from './routes';

// After this long without a successful live poll the freshness chip reads
// stale.
export const staleAfterMs = 30 * 1000;

// A duration (ms) as the Now view shows it: seconds under a minute,
// minutes under an hour, then hours and minutes ("3h 12m", not "192m").
export function formatSpan(ms: number): string {
  const sec = Math.max(0, Math.floor(ms / 1000));
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m`;
  return `${Math.floor(min / 60)}h ${min % 60}m`;
}

function ms(iso: string | undefined): number {
  const t = iso ? new Date(iso).getTime() : NaN;
  return Number.isNaN(t) ? 0 : t;
}

// What a tile says about its last output: the time since it last wrote
// (since its start when it has written nothing yet), as "no output for 6m"
// when the server counts it quiet.
export function lastOutputText(item: Pick<LiveItem, 'started' | 'last_output' | 'quiet'>, now: number): string {
  const span = formatSpan(now - (ms(item.last_output) || ms(item.started)));
  return item.quiet ? `no output for ${span}` : span;
}

// A flow's round, "1/3", or nothing when it has not reported one.
function roundText(flow: LiveItem['flow']): string {
  return flow?.round ? `${flow.round}/${flow.max_rounds || '?'}` : '';
}

// A tile's second header line: the kind with the flow's round ("verify
// 2/3"), or a check group's PR and head; the actor; elapsed time against
// the limit.
export function tileMeta(item: LiveItem, now: number): string {
  const parts: string[] = [];
  if (item.kind === 'check' || item.kind === 'e2e') {
    parts.push(item.kind === 'check' ? 'checks' : 'checks e2e');
    if (item.pr) parts.push(`!${item.pr}`);
    if (item.sha) parts.push(item.sha.slice(0, 7));
  } else {
    parts.push([item.kind, roundText(item.flow)].filter(Boolean).join(' '));
    if (item.actor) parts.push(item.actor);
  }
  const elapsed = formatSpan(now - ms(item.started));
  parts.push(item.limit ? `${elapsed}/${formatSpan(item.limit * 1000)}` : elapsed);
  return parts.join(' · ');
}

// Where a row links: a flow's page, or the interactive run's.
export function rowHref(row: LiveRow): string {
  return row.kind === 'flow' && row.issue ? flowRouteHash(row.issue) : runRouteHash(row.id ?? row.key);
}

// A row's text after its target and title: "flow 1/3 · waiting for actor ·
// <detail> · 6m", or "implement · claude · interactive · 9m".
export function rowText(row: LiveRow, now: number): string {
  const since = row.since ? formatSpan(now - ms(row.since)) : '';
  const parts =
    row.kind === 'flow'
      ? [['flow', roundText(row.flow)].filter(Boolean).join(' '), row.what, row.detail, since]
      : [row.detail, row.what, since];
  return parts.filter(Boolean).join(' · ');
}

// Where a thread that just ended links: its flow's page, or its last run's.
export function endedHref(e: LiveEnded): string {
  return e.kind === 'flow' && e.issue ? flowRouteHash(e.issue, e.pr) : runRouteHash(e.run_id);
}

// The inbox's kinds, in the order the Needs-you line names them, with the
// label of one and of several.
export const inboxKinds: { kind: InboxItemKind; one: string; many: string; tone: string }[] = [
  { kind: 'ready_to_land', one: 'ready to land', many: 'ready to land', tone: 'success' },
  { kind: 'stopped_flow', one: 'stopped flow', many: 'stopped flows', tone: 'warning' },
  { kind: 'conflicts_with_base', one: 'conflict', many: 'conflicts', tone: 'error' },
  { kind: 'broken_run', one: 'broken run', many: 'broken runs', tone: 'error' },
  { kind: 'discuss_answer', one: 'discuss answer', many: 'discuss answers', tone: 'accent' },
  { kind: 'actor_filed_issue', one: 'actor-filed issue', many: 'actor-filed issues', tone: 'neutral' },
];

// The inbox's counts by kind, in inboxKinds' order, omitting kinds with
// no item.
export function inboxCounts(items: InboxItem[] | undefined): { kind: InboxItemKind; count: number; label: string; tone: string }[] {
  return inboxKinds
    .map((k) => {
      const count = (items ?? []).filter((i) => i.kind === k.kind).length;
      return { kind: k.kind, count, label: `${count} ${count === 1 ? k.one : k.many}`, tone: k.tone };
    })
    .filter((c) => c.count > 0);
}

// Whether Needs you is expanded, kept in localStorage; storage that is
// missing or refuses (private windows, a full quota) leaves it collapsed.
const inboxExpandedKey = 'bottown.now.inboxExpanded';

export function loadInboxExpanded(): boolean {
  try {
    return globalThis.localStorage?.getItem(inboxExpandedKey) === '1';
  } catch {
    return false;
  }
}

export function saveInboxExpanded(expanded: boolean): void {
  try {
    globalThis.localStorage?.setItem(inboxExpandedKey, expanded ? '1' : '0');
  } catch {
    // Not remembered; the view still toggles.
  }
}

// The health checklist's failing items, as the Repository view marks them
// with a cross or a warning: the problems the header's health chip counts.
export function healthProblems(health: HealthChecklist | undefined): string[] {
  if (!health) return [];
  const problems: string[] = [];
  if (!health.agent_instructions?.found) problems.push('no agent instructions');
  if (health.protected_main?.checked && !health.protected_main.protected) problems.push('main is not protected');
  if (health.repo_settings?.error) problems.push('repository settings: ' + health.repo_settings.error);
  if (health.version && !health.version.matches) problems.push('CLI and server versions differ');
  for (const hc of health.harness_commands ?? []) {
    if (!hc.current) problems.push(`${hc.harness} commands outdated`);
  }
  return problems;
}

// The upstream chip: the default branch against origin.
export function upstreamText(status: Pick<UIStatusResponse, 'upstream' | 'forge'>): { text: string; tone: string } {
  const branch = status.forge?.default_branch || 'main';
  const up = status.upstream;
  if (!up?.has_upstream) return { text: `${branch} · no upstream`, tone: 'neutral' };
  if (up.error) return { text: `${branch} · upstream unknown`, tone: 'warning' };
  const parts: string[] = [];
  if (up.ahead_count > 0) parts.push(`${up.ahead_count} ahead`);
  if (up.behind_count > 0) parts.push(`${up.behind_count} behind`);
  if (parts.length === 0) return { text: `${branch} · up to date`, tone: 'success' };
  return { text: `${branch} · ${parts.join(', ')}`, tone: up.behind_count > 0 ? 'warning' : 'accent' };
}

// The freshness chip: how long since the last successful live poll, and
// stale after staleAfterMs without one. Before any, it is loading.
export function freshness(lastOk: number, now: number): { text: string; stale: boolean } {
  if (!lastOk) return { text: 'connecting…', stale: false };
  const since = Math.max(0, now - lastOk);
  if (since >= staleAfterMs) return { text: `stale · ${formatSpan(since)}`, stale: true };
  return { text: `live · ${formatSpan(since)} ago`, stale: false };
}

// emptyLive is the live view before the first poll answers.
export const emptyLive = (): Live => ({
  items: [],
  rows: [],
  ended: [],
  summary: { running: 0, quiet: 0, waiting: 0, interactive: 0, inbox: {} },
});
