// Geometry and labels for the Timeline view. The server lays out the lanes
// and derives every interval (GET /api/timeline); these only place them on
// a time scale, pick its ticks, and name what is drawn.
import type { TimelineData, TimelineRun, TimelineSegment } from './types';

export type RangePreset = '6h' | 'today' | '7d' | 'custom';

export const RANGE_PRESETS: { value: RangePreset; label: string }[] = [
  { value: '6h', label: 'Last 6 hours' },
  { value: 'today', label: 'Today' },
  { value: '7d', label: '7 days' },
  { value: 'custom', label: 'Custom' },
];

const HOUR = 3600_000;
const DAY = 24 * HOUR;

// timelineQuery is the query string GET /api/timeline gets for a range: a
// preset ends now (no until, so the window stays open), a custom range is
// from and to as <input type="datetime-local"> gives them, in local time.
// It is null when a custom range is not a valid one.
export function timelineQuery(preset: RangePreset, now: Date, from = '', to = ''): string | null {
  let since: Date;
  let until: Date | null = null;
  switch (preset) {
    case '6h':
      since = new Date(now.getTime() - 6 * HOUR);
      break;
    case 'today':
      since = new Date(now.getFullYear(), now.getMonth(), now.getDate());
      break;
    case '7d':
      since = new Date(now.getTime() - 7 * DAY);
      break;
    case 'custom': {
      since = new Date(from);
      until = to ? new Date(to) : null;
      if (isNaN(since.getTime()) || (until && (isNaN(until.getTime()) || until <= since))) return null;
      break;
    }
  }
  const q = new URLSearchParams({ since: since.toISOString() });
  if (until) q.set('until', until.toISOString());
  return q.toString();
}

// viewEnd is where the drawn window ends: now while the window is open,
// else its end.
export function viewEnd(data: TimelineData, nowMs: number): number {
  const until = Date.parse(data.window.until);
  return data.open ? Math.max(until, nowMs) : until;
}

// segmentEnd is where an interval ends as drawn: a live one, in an open
// window, grows to now.
export function segmentEnd(end: string, live: boolean | undefined, data: TimelineData, nowMs: number): number {
  const e = Date.parse(end);
  return live && data.open ? Math.max(e, nowMs) : e;
}

// runEnd is where a run's bar ends: its end, or for a live run now, within
// the drawn window.
export function runEnd(run: TimelineRun, data: TimelineData, nowMs: number): number {
  const end = viewEnd(data, nowMs);
  if (!run.ended) return data.open ? end : Math.min(Date.parse(data.now), end);
  return Math.min(Date.parse(run.ended), end);
}

// Scale maps times in [since, until] to x in [x0, x1].
export interface Scale {
  x(ms: number): number;
  since: number;
  until: number;
}

export function timeScale(since: number, until: number, x0: number, x1: number): Scale {
  const span = Math.max(until - since, 1);
  return {
    since,
    until,
    x: (ms) => x0 + ((Math.min(Math.max(ms, since), until) - since) / span) * (x1 - x0),
  };
}

const TICK_STEPS = [5, 10, 15, 30, 60, 120, 180, 360, 720, 1440].map((m) => m * 60_000);

// timeTicks are the tick times for [since, until], at most max of them, on
// round local times; a day step labels dates, a shorter one clock times.
export function timeTicks(since: number, until: number, max: number): { t: number; label: string }[] {
  const span = until - since;
  if (span <= 0 || max < 1) return [];
  const step = TICK_STEPS.find((s) => span / s <= max) ?? Math.ceil(span / max / DAY) * DAY;
  const offset = new Date(since).getTimezoneOffset() * 60_000;
  let t = Math.ceil((since - offset) / step) * step + offset;
  const out: { t: number; label: string }[] = [];
  for (; t <= until; t += step) {
    const d = new Date(t);
    const label = step >= DAY
      ? d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
      : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false });
    out.push({ t, label });
  }
  return out;
}

// percent is seconds as a whole percentage of total, "0%" for none.
export function percent(seconds: number, total: number): string {
  if (total <= 0 || seconds <= 0) return '0%';
  const p = (seconds / total) * 100;
  return p < 1 ? '<1%' : `${Math.round(p)}%`;
}

// formatSpan is a duration in seconds, to the minute: "45s", "12m", "3h 05m",
// "2d 4h".
export function formatSpan(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${String(m % 60).padStart(2, '0')}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

// kindClass is the bar class for a run's kind, colored by it.
export function kindClass(kind: string): string {
  return ['implement', 'verify', 'discuss', 'plan'].includes(kind) ? `tl-kind-${kind}` : 'tl-kind-other';
}

// runFailed is whether a run's bar is outlined: it failed or was revoked.
export function runFailed(run: TimelineRun): boolean {
  return run.status === 'failed' || run.status === 'revoked';
}

// runTarget is a run's issue or PR, "#7 !9".
export function runTarget(run: { issue?: number; pr?: number }): string {
  return [run.issue ? `#${run.issue}` : '', run.pr ? `!${run.pr}` : ''].filter(Boolean).join(' ');
}

// runLabel names a run for its bar's tooltip and accessible name.
export function runLabel(run: TimelineRun): string {
  const parts = [`${run.kind} by ${run.actor}`];
  const target = runTarget(run);
  if (target) parts.push(target);
  parts.push(run.ended ? (run.status || 'ended') : 'running');
  if (run.outcome) parts.push(run.outcome.replace(/_/g, ' '));
  if (run.interactive) parts.push('interactive');
  return `${parts.join(' · ')} (${run.id})`;
}

// segmentLabel names a flow lane's check, owner wait or gap.
export function segmentLabel(seg: TimelineSegment): string {
  const span = formatSpan((Date.parse(seg.end) - Date.parse(seg.start)) / 1000);
  switch (seg.kind) {
    case 'e2e': {
      const state = seg.state === 'success' ? 'passed' : seg.state === 'failure' ? 'failed' : 'running';
      const sha = seg.sha ? ` at ${seg.sha.slice(0, 8)}` : '';
      if (!seg.live && seg.start === seg.end) return `Checks ${state}${sha}; no duration recorded`;
      return `Checks ${state}${sha}, ${span}${seg.derived ? ' (start derived from the status)' : ''}`;
    }
    case 'owner_wait': {
      const ended = seg.live ? 'still waiting' : seg.ended_by ? `until the ${endedBy[seg.ended_by] ?? seg.ended_by}` : '';
      return `Waiting on the owner: ${seg.reason || 'stopped'}, ${span}${ended ? `, ${ended}` : ''}`;
    }
    case 'overhead':
      return `Flow overhead, ${span}: between steps (sync, fetches, waits for an actor)`;
    default:
      return span;
  }
}

const endedBy: Record<string, string> = { run: 'next run', merge: 'merge', close: 'close', stop: 'next stop' };

// laneRowsHeight is the height of a lane of n rows.
export const ROW_H = 18;
export const ROW_GAP = 4;
export const LANE_PAD = 6;

export function laneHeight(rows: number): number {
  const n = Math.max(rows, 1);
  return n * ROW_H + (n - 1) * ROW_GAP + 2 * LANE_PAD;
}

// concurrencyPath is the step area of the concurrency strip, from steps
// until end, peak at the top of [y0, y1].
export function concurrencyPath(
  steps: { t: string; n: number }[],
  end: number,
  peak: number,
  scale: Scale,
  y0: number,
  y1: number,
): string {
  if (steps.length === 0 || peak <= 0) return '';
  const y = (n: number) => y1 - (n / peak) * (y1 - y0);
  let d = `M ${scale.x(Date.parse(steps[0].t)).toFixed(1)} ${y1}`;
  steps.forEach((s, i) => {
    const x0 = scale.x(Date.parse(s.t));
    const x1 = scale.x(i + 1 < steps.length ? Date.parse(steps[i + 1].t) : end);
    d += ` L ${x0.toFixed(1)} ${y(s.n).toFixed(1)} L ${x1.toFixed(1)} ${y(s.n).toFixed(1)}`;
  });
  return `${d} L ${scale.x(end).toFixed(1)} ${y1} Z`;
}
