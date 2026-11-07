// Labels and geometry for the Stats view. The server derives every number
// (GET /api/stats); these only name and format them, and place the burn
// charts on a time scale.
import type { StatsBurnPoint, StatsIssue, StatsMeasure } from './types';
import { formatSpan } from './timeline';

export type StatsRangePreset = '7d' | '30d' | '90d' | 'custom';

export const STATS_RANGE_PRESETS: { value: StatsRangePreset; label: string }[] = [
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
  { value: '90d', label: 'Last 90 days' },
  { value: 'custom', label: 'Custom' },
];

const DAY = 24 * 3600_000;

// statsQuery is the query string GET /api/stats gets for a range: a preset
// ends now (no until), a custom range is from and to as
// <input type="datetime-local"> gives them, in local time. It is null when
// a custom range is not a valid one.
export function statsQuery(preset: StatsRangePreset, now: Date, from = '', to = ''): string | null {
  let since: Date;
  let until: Date | null = null;
  switch (preset) {
    case '7d':
      since = new Date(now.getTime() - 7 * DAY);
      break;
    case '30d':
      since = new Date(now.getTime() - 30 * DAY);
      break;
    case '90d':
      since = new Date(now.getTime() - 90 * DAY);
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

// target is an issue/PR reference, "#7 !9".
export function target(v: { issue?: number; pr?: number }): string {
  return [v.issue ? `#${v.issue}` : '', v.pr ? `!${v.pr}` : ''].filter(Boolean).join(' ');
}

// measureText formats a measure as "median (min–max), n N", or "no data"
// when nothing reported it. A single value shows as just that value.
export function measureText(m: StatsMeasure, format: (x: number) => string): string {
  if (m.n === 0 || m.median === undefined) return 'no data';
  if (m.min !== undefined && m.max !== undefined && m.min !== m.max) {
    return `${format(m.median)} (${format(m.min)}–${format(m.max)}), n ${m.n}`;
  }
  return `${format(m.median)}, n ${m.n}`;
}

// durationMeasure is measureText with a duration format.
export function durationMeasure(m: StatsMeasure): string {
  return measureText(m, formatSpan);
}

// countMeasure is measureText for a whole count.
export function countMeasure(m: StatsMeasure): string {
  return measureText(m, (x) => String(Math.round(x)));
}

// BREAKDOWN_LABELS names each breakdown dimension.
export const BREAKDOWN_LABELS: Record<string, string> = {
  actor: 'Implementer',
  family: 'Implementer family',
  verifier_family: 'Verifier family',
  pair: 'Pair (implementer/verifier family)',
  size: 'Size',
  milestone: 'Milestone',
};

// breakdownKey names a cell's key, including the ones the dimension does
// not cover.
export function breakdownKey(by: string, key: string): string {
  if (key !== '') return key;
  switch (by) {
    case 'size':
      return 'unlabelled';
    case 'milestone':
      return 'no milestone';
    case 'verifier_family':
      return 'no verifier';
    case 'pair':
      return 'incomplete pair';
    default:
      return 'unknown';
  }
}

// agentByKindText is a thread's agent time by kind, "implement 2h 00m · verify 30m".
export function agentByKindText(v: StatsIssue): string {
  const parts = Object.entries(v.agent_by_kind ?? {}).map(([k, s]) => `${k} ${formatSpan(s)}`);
  return parts.join(' · ');
}

// usageText is one family's usage, "12.3k in, 4.5k out, $1.20 (3 of 5 runs
// reported)"; a figure the family did not report is left out.
export function usageText(u: { runs: number; reported: number; input_tokens?: number; output_tokens?: number; cost_usd?: number }): string {
  const parts: string[] = [];
  if (u.input_tokens !== undefined) parts.push(`${tokens(u.input_tokens)} in`);
  if (u.output_tokens !== undefined) parts.push(`${tokens(u.output_tokens)} out`);
  if (u.cost_usd !== undefined) parts.push(`$${u.cost_usd.toFixed(2)}`);
  parts.push(`${u.reported} of ${u.runs} runs reported`);
  return parts.join(', ');
}

// tokens abbreviates a token count: 950, 12.3k, 1.4M.
export function tokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

// burnMax is the chart's top: the largest scope drawn, in the units the
// points carry (weighted or counts).
export function burnMax(points: StatsBurnPoint[], weighted: boolean): number {
  let max = 0;
  for (const p of points) {
    max = Math.max(max, weighted ? p.total_weight ?? p.total : p.total);
  }
  return Math.max(max, 1);
}

// burnValue is a point's scope and closed values in the chart's units.
export function burnValue(p: StatsBurnPoint, weighted: boolean): { total: number; closed: number } {
  return {
    total: weighted ? p.total_weight ?? p.total : p.total,
    closed: weighted ? (p.total_weight ?? p.total) - (p.open_weight ?? 0) : p.closed,
  };
}

// burnPath is the polyline of a value picked from each point, in the chart
// box [x0, x1] × [y0, y1] with top maxY.
export function burnPath(
  points: StatsBurnPoint[],
  pick: (p: StatsBurnPoint) => number,
  since: number,
  until: number,
  x0: number,
  x1: number,
  y0: number,
  y1: number,
  maxY: number,
): string {
  if (points.length === 0) return '';
  const span = Math.max(until - since, 1);
  const x = (t: number) => x0 + ((Math.min(Math.max(t, since), until) - since) / span) * (x1 - x0);
  const y = (v: number) => y1 - (v / Math.max(maxY, 1)) * (y1 - y0);
  return points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${x(Date.parse(p.t)).toFixed(1)} ${y(pick(p)).toFixed(1)}`).join(' ');
}
