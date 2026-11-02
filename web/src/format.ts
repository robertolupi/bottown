import type { UsageInfo } from './types';

// A run's elapsed time since createdIso, as of now (ms): "7m 05s".
export function formatElapsed(createdIso: string, now: number): string {
  if (!createdIso) return '';
  const start = new Date(createdIso).getTime();
  const diffSec = Math.max(0, Math.floor((now - start) / 1000));
  const m = Math.floor(diffSec / 60);
  const s = diffSec % 60;
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
