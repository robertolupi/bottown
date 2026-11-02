// Layout and action helpers for the Milestones view. Run with `npm test`.
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  GAP_X,
  NODE_WIDTH,
  PAD,
  actorPair,
  canOpenFlow,
  canStart,
  layoutDag,
  statusBadge,
} from '../src/milestones.ts';
import type { MilestoneNode } from '../src/types.ts';

function node(number: number, layer: number, extra: Partial<MilestoneNode> = {}): MilestoneNode {
  return { number, title: `#${number}`, url: '', state: 'open', status: 'ready', layer, depends_on: [], ...extra };
}

test('layoutDag places layers in columns and orders by dependencies', () => {
  // 1 ──▶ 4, 2 ──▶ 3, 9 (external, closed) ──▶ 3
  const layout = layoutDag({
    nodes: [node(1, 0), node(2, 0), node(9, 0, { external: true, state: 'closed' }), node(3, 1), node(4, 1)],
    edges: [
      { from: 1, to: 4 },
      { from: 2, to: 3 },
      { from: 9, to: 3 },
    ],
  });
  const at = new Map(layout.nodes.map((p) => [p.node.number, p]));
  assert.equal(at.get(1)!.x, PAD);
  assert.equal(at.get(3)!.x, PAD + NODE_WIDTH + GAP_X);
  // Column 0 by number: 1, 2, 9. Column 1 by the mean row of its
  // dependencies: 4 (row 0) before 3 (rows 1 and 2).
  assert.ok(at.get(1)!.y < at.get(2)!.y && at.get(2)!.y < at.get(9)!.y);
  assert.ok(at.get(4)!.y < at.get(3)!.y);
  assert.ok(at.get(9)!.height < at.get(1)!.height, 'an external dependency is a pill');
  assert.equal(layout.width, PAD * 2 + 2 * NODE_WIDTH + GAP_X);
  assert.equal(layout.edges.length, 3);
  const blocking = Object.fromEntries(layout.edges.map((e) => [e.key, e.blocking]));
  assert.deepEqual(blocking, { '1-4': true, '2-3': true, '9-3': false });
  for (const e of layout.edges) assert.match(e.path, /^M [\d.]+ [\d.]+ C /);
});

test('layoutDag skips edges to nodes it does not have', () => {
  const layout = layoutDag({ nodes: [node(1, 0)], edges: [{ from: 7, to: 1 }] });
  assert.equal(layout.edges.length, 0);
  assert.equal(layoutDag({ nodes: [], edges: [] }).width, 0);
});

test('node actions follow status', () => {
  assert.ok(canStart(node(1, 0, { status: 'ready' })));
  assert.ok(canStart(node(1, 0, { status: 'stopped' })));
  assert.ok(!canStart(node(1, 0, { status: 'blocked' })));
  assert.ok(!canStart(node(1, 0, { status: 'ready', external: true })));
  assert.ok(canOpenFlow(node(1, 0, { status: 'in_review' })));
  assert.ok(!canOpenFlow(node(1, 0, { status: 'running' })));
  assert.ok(!canOpenFlow(node(1, 0, { status: 'closed', state: 'closed' })));
});

test('statusBadge and actorPair', () => {
  assert.deepEqual(statusBadge(node(1, 0, { status: 'stopped', stop_reason: 'out of rounds' })), {
    label: 'stopped: out of rounds',
    tone: 'error',
  });
  assert.equal(statusBadge(node(1, 0, { status: 'in_review' })).label, 'in review');
  assert.equal(actorPair(node(1, 0, { implement: 'agy', verify: 'claude' })), 'implement/agy · verify/claude');
  assert.equal(actorPair(node(1, 0)), '');
});
