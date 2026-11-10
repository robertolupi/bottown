import type { UsageInfo } from './types';

// A run's elapsed time since createdIso, as of now (ms): "7m 05s", and
// hours past an hour: "3h 12m".
export function formatElapsed(createdIso: string, now: number): string {
  if (!createdIso) return '';
  const start = new Date(createdIso).getTime();
  const diffSec = Math.max(0, Math.floor((now - start) / 1000));
  const m = Math.floor(diffSec / 60);
  const s = diffSec % 60;
  if (m >= 60) return `${Math.floor(m / 60)}h ${m % 60}m`;
  return `${m}m ${s < 10 ? '0' : ''}${s}s`;
}

// The local time of day of an ISO timestamp, to the second.
export function formatTime(isoStr?: string): string {
  if (!isoStr) return '';
  try {
    const d = new Date(isoStr);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  } catch {
    return isoStr;
  }
}

// A run's or thread's usage: tokens, cost and turns, or "—" when none.
export function formatUsage(u?: UsageInfo): string {
  if (!u) return '—';
  const parts: string[] = [];
  if (u.input_tokens || u.output_tokens) {
    const inTok = u.input_tokens ? `${(u.input_tokens / 1000).toFixed(1)}k in` : '';
    const outTok = u.output_tokens ? `${(u.output_tokens / 1000).toFixed(1)}k out` : '';
    parts.push([inTok, outTok].filter(Boolean).join(', '));
  }
  if (u.cost_usd !== undefined && u.cost_usd !== null) {
    parts.push(`$${u.cost_usd.toFixed(3)}`);
  }
  if (u.turns) {
    parts.push(`${u.turns} turn${u.turns > 1 ? 's' : ''}`);
  }
  return parts.join(' · ') || '—';
}

// The badge tone of a run's or thread's execution status.
export function statusTone(status: string): string {
  return status === 'succeeded' ? 'success' : status === 'failed' ? 'error' : status === 'running' ? 'accent' : 'warning';
}

// A timestamp's exact local date and time, for a title that reveals what a
// relative age hides.
export function formatDateTime(isoStr?: string): string {
  if (!isoStr) return '';
  const d = new Date(isoStr);
  return isNaN(d.getTime()) ? isoStr : d.toLocaleString();
}

// A timestamp as a relative age ("just now", "12m ago", "3d ago"), as of
// now (ms). An unparseable timestamp is shown as it is.
export function formatRelativeTime(isoStr: string | undefined, now: number): string {
  if (!isoStr) return '';
  const t = new Date(isoStr).getTime();
  if (isNaN(t)) return isoStr;
  const s = Math.floor(Math.max(0, now - t) / 1000);
  if (s < 45) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${Math.max(1, m)}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d}d ago`;
  const mo = Math.floor(d / 30);
  if (mo < 12) return `${mo}mo ago`;
  return `${Math.floor(mo / 12)}y ago`;
}

// A thread's total cost, for the Duration · cost column, or '' when none
// was recorded (the token counts live on the run page).
export function formatCost(u?: UsageInfo): string {
  if (!u || u.cost_usd === undefined || u.cost_usd === null) return '';
  return `$${u.cost_usd.toFixed(2)}`;
}
