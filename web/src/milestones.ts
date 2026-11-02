// Layout and actions for the Milestones view's dependency DAG
// (docs/design/milestones.md §2.7): layers left to right, one column per
// layer, nodes ordered in a column by where their dependencies sit.
import type { MilestoneGroup, MilestoneNode } from './types';

export const NODE_WIDTH = 232;
export const NODE_HEIGHT = 112;
export const PILL_HEIGHT = 36;
export const GAP_X = 72;
export const GAP_Y = 16;
export const PAD = 8;

export interface PlacedNode {
  node: MilestoneNode;
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface PlacedEdge {
  key: string;
  from: number;
  to: number;
  path: string;
  // The dependency is still open: the edge is what blocks its target.
  blocking: boolean;
}

export interface DagLayout {
  nodes: PlacedNode[];
  edges: PlacedEdge[];
  width: number;
  height: number;
}

// layoutDag places a group's nodes in columns by layer. Within a column,
// nodes are ordered by the mean row of their dependencies in earlier
// columns (a one-pass barycenter), then by number, which keeps most edges
// short and uncrossed without a layout library.
export function layoutDag(group: Pick<MilestoneGroup, 'nodes' | 'edges'>): DagLayout {
  const byLayer = new Map<number, MilestoneNode[]>();
  for (const n of group.nodes) {
    const list = byLayer.get(n.layer) ?? [];
    list.push(n);
    byLayer.set(n.layer, list);
  }
  const layers = [...byLayer.keys()].sort((a, b) => a - b);
  const row = new Map<number, number>();
  const deps = new Map<number, number[]>();
  for (const e of group.edges) {
    const list = deps.get(e.to) ?? [];
    list.push(e.from);
    deps.set(e.to, list);
  }

  const placed = new Map<number, PlacedNode>();
  let height = 0;
  layers.forEach((layer, col) => {
    const nodes = byLayer.get(layer)!;
    const center = (n: MilestoneNode): number => {
      const rows = (deps.get(n.number) ?? []).map((d) => row.get(d)).filter((r): r is number => r !== undefined);
      return rows.length ? rows.reduce((a, b) => a + b, 0) / rows.length : Number.POSITIVE_INFINITY;
    };
    nodes.sort((a, b) => {
      const ca = center(a);
      const cb = center(b);
      if (ca !== cb) return ca < cb ? -1 : 1;
      return a.number - b.number;
    });
    let y = PAD;
    nodes.forEach((n, i) => {
      row.set(n.number, i);
      const h = n.external ? PILL_HEIGHT : NODE_HEIGHT;
      placed.set(n.number, { node: n, x: PAD + col * (NODE_WIDTH + GAP_X), y, width: NODE_WIDTH, height: h });
      y += h + GAP_Y;
    });
    height = Math.max(height, y - GAP_Y + PAD);
  });

  const edges: PlacedEdge[] = [];
  for (const e of group.edges) {
    const a = placed.get(e.from);
    const b = placed.get(e.to);
    if (!a || !b) continue;
    edges.push({
      key: `${e.from}-${e.to}`,
      from: e.from,
      to: e.to,
      path: edgePath(a, b),
      blocking: a.node.state !== 'closed',
    });
  }

  const width = layers.length ? PAD * 2 + layers.length * NODE_WIDTH + (layers.length - 1) * GAP_X : 0;
  return { nodes: [...placed.values()], edges, width, height };
}

// edgePath is a horizontal cubic curve from the right edge of a to the
// left edge of b, ending where the arrowhead marker draws.
export function edgePath(a: PlacedNode, b: PlacedNode): string {
  const x1 = a.x + a.width;
  const y1 = a.y + a.height / 2;
  const x2 = b.x - 2;
  const y2 = b.y + b.height / 2;
  const dx = Math.max(24, (x2 - x1) / 2);
  return `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`;
}

// canStart says whether the view offers starting a flow or a run on a
// node: open issues of the group that nothing is working on yet.
export function canStart(node: MilestoneNode): boolean {
  return !node.external && (node.status === 'ready' || node.status === 'stopped');
}

// canOpenFlow says whether clicking a node opens the flow dialog for it:
// any open issue of the group without a live run or flow.
export function canOpenFlow(node: MilestoneNode): boolean {
  return !node.external && node.state !== 'closed' && node.status !== 'running';
}

export interface StatusBadge {
  label: string;
  tone: 'success' | 'warning' | 'error' | 'accent' | 'neutral';
}

export function statusBadge(node: MilestoneNode): StatusBadge {
  switch (node.status) {
    case 'closed':
      return { label: 'closed', tone: 'success' };
    case 'ready':
      return { label: 'ready', tone: 'accent' };
    case 'running':
      return { label: 'running', tone: 'accent' };
    case 'in_review':
      return { label: 'in review', tone: 'neutral' };
    case 'approved':
      return { label: 'approved', tone: 'success' };
    case 'stopped':
      return { label: node.stop_reason ? `stopped: ${node.stop_reason}` : 'stopped', tone: 'error' };
    case 'blocked':
      return { label: 'blocked', tone: 'warning' };
    default:
      return { label: node.status || node.state, tone: 'neutral' };
  }
}

// actorPair is the "implement/A · verify/B" line of a node, or "".
export function actorPair(node: MilestoneNode): string {
  const parts: string[] = [];
  if (node.implement) parts.push(`implement/${node.implement}`);
  if (node.verify) parts.push(`verify/${node.verify}`);
  return parts.join(' · ');
}
