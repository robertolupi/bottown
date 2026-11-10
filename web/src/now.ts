// The Now view's data (monitor-ui design §3): the live tiles and one-line
// rows, each tile's last output and quiet, the inbox's counts, the header
// chips and the threads that just ended. Pure functions of the state the
// app already holds, so the view only lays them out.
import type {
  ActiveFlowInfo,
  HealthChecklist,
  HistoryRunInfo,
  HistoryThread,
  InboxItem,
  InboxItemKind,
  LiveCheckInfo,
  LiveRunInfo,
  UIStatusResponse,
} from './types';
import { flowRouteHash, runRouteHash } from './routes';

// A tile is quiet when its run or check group has written nothing for this
// long (design §7.2). The one place the threshold is set; the live-view
// step moves it to the server.
export const quietAfterMs = 5 * 60 * 1000;

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

// One tile: a live batch run or a live check group.
export type NowTile =
  | { kind: 'run'; id: string; created: string; run: LiveRunInfo }
  | { kind: 'check'; id: string; created: string; check: LiveCheckInfo };

// The tiles: every live batch run and live check group, by start time, so
// the grid keeps its order while the owner watches.
export function nowTiles(liveRuns: LiveRunInfo[] | undefined, liveChecks: LiveCheckInfo[] | undefined): NowTile[] {
  const tiles: NowTile[] = [
    ...(liveRuns ?? []).filter((r) => !r.interactive).map((run) => ({ kind: 'run' as const, id: run.id, created: run.created, run })),
    ...(liveChecks ?? []).map((check) => ({ kind: 'check' as const, id: check.id, created: check.created, check })),
  ];
  return tiles.sort((a, b) => ms(a.created) - ms(b.created) || a.id.localeCompare(b.id));
}

// What a tile says about its last output: the time since it last wrote
// (since its start when it has written nothing yet), and whether that
// makes it quiet.
export function lastOutputStatus(item: { created: string; last_output?: string }, now: number): { text: string; quiet: boolean } {
  const since = now - (ms(item.last_output) || ms(item.created));
  if (since >= quietAfterMs) return { text: `no output for ${formatSpan(since)}`, quiet: true };
  return { text: formatSpan(since), quiet: false };
}

// One line under the grid: a flow with nothing live, or an interactive run.
export interface NowRow {
  key: string;
  href: string;
  label: string;
  title?: string;
  detail: string;
  since?: string; // when it started waiting, or the run started
}

// The rows: active flows with no live run or check, with what they wait
// for and since when (the latest end of a run on their issue, when history
// has one), then interactive runs, each by start time.
export function nowRows(
  activeFlows: ActiveFlowInfo[] | undefined,
  liveRuns: LiveRunInfo[] | undefined,
  liveChecks: LiveCheckInfo[] | undefined,
  historyRuns: HistoryRunInfo[] | undefined,
): NowRow[] {
  const runs = liveRuns ?? [];
  const checks = liveChecks ?? [];
  const rows: NowRow[] = [];
  for (const f of activeFlows ?? []) {
    const live = runs.some((r) => (r.flow?.issue ?? r.issue) === f.issue) || checks.some((c) => c.issue === f.issue);
    if (live) continue;
    const ended = (historyRuns ?? [])
      .filter((h) => h.issue === f.issue && h.ended)
      .map((h) => h.ended!)
      .sort((a, b) => ms(b) - ms(a))[0];
    const title = (historyRuns ?? []).find((h) => h.issue === f.issue && h.issue_title)?.issue_title;
    rows.push({
      key: `flow-${f.issue}`,
      href: flowRouteHash(f.issue),
      label: `#${f.issue}`,
      title,
      detail: `flow ${f.round}/${f.max_rounds} · ${f.step}`,
      since: ended,
    });
  }
  for (const r of [...runs].filter((r) => r.interactive).sort((a, b) => ms(a.created) - ms(b.created))) {
    rows.push({
      key: r.id,
      href: runRouteHash(r.id),
      label: r.target,
      title: r.issue_title || r.pr_title,
      detail: `${r.kind} · ${r.actor} · interactive`,
      since: r.created,
    });
  }
  return rows;
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

// One thread that just ended, as a link.
export interface EndedThread {
  key: string;
  href: string;
  label: string;
  title?: string;
  ended: string;
}

// Up to max threads whose last run ended in the past hour, newest first;
// a thread still active has not ended.
export function justEnded(threads: HistoryThread[] | undefined, now: number, max = 5): EndedThread[] {
  const out: EndedThread[] = [];
  for (const t of threads ?? []) {
    if (t.active || t.runs.length === 0 || t.runs.some((r) => !r.ended)) continue;
    const ended = t.runs.map((r) => r.ended!).sort((a, b) => ms(b) - ms(a))[0];
    const age = now - ms(ended);
    if (age < 0 || age > 60 * 60 * 1000) continue;
    const last = t.runs[t.runs.length - 1];
    const flow = t.kind === 'flow' && t.issue;
    out.push({
      key: t.key,
      href: flow ? flowRouteHash(t.issue!, t.pr) : runRouteHash(last.id),
      label: t.issue ? `#${t.issue}` : t.pr ? `!${t.pr}` : last.target,
      title: t.issue_title || t.pr_title,
      ended,
    });
  }
  return out.sort((a, b) => ms(b.ended) - ms(a.ended)).slice(0, max);
}
