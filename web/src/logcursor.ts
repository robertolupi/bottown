// The run page's log, loaded once and then in increments (design §4,
// "Loading the log"). GET /api/runs/ID/log answers with the rendered lines
// and the length of their stable prefix (stable) with its digest
// (stable_sum). A poll sends them back as after and sum: the answer is the
// lines from that position on, which replace everything after the kept
// prefix, or, with reset, the whole log, which replaces everything.
import type { LogLine } from './types';

export interface LogResponse {
  lines?: LogLine[];
  stable?: number;
  stable_sum?: string;
  reset?: boolean;
}

// The page's lines, and where its next poll starts: the stable prefix of
// the last answer. An answer without one (an older server) leaves no
// cursor, so the next poll loads the whole log.
export interface LogCursor {
  lines: LogLine[];
  stable?: number;
  sum?: string;
}

// logURL is the request for run id's log: the whole log without a cursor
// (or with full set, as when the run has ended), else the lines after it.
export function logURL(id: string, cursor: LogCursor | null, full = false): string {
  const base = `/api/runs/${encodeURIComponent(id)}/log`;
  const after = full ? undefined : cursorAt(cursor);
  return after === undefined ? base : `${base}?after=${after}&sum=${encodeURIComponent(cursor!.sum!)}`;
}

// applyLog is the page's log after the answer to the request logURL made
// from cursor (with the same full).
export function applyLog(cursor: LogCursor | null, res: LogResponse, full = false): LogCursor {
  const got = res.lines ?? [];
  const after = full || res.reset ? undefined : cursorAt(cursor);
  const lines = after === undefined ? got : cursor!.lines.slice(0, after).concat(got);
  return { lines, stable: res.stable, sum: res.stable_sum };
}

function cursorAt(cursor: LogCursor | null): number | undefined {
  return cursor && cursor.sum !== undefined ? cursor.stable : undefined;
}
