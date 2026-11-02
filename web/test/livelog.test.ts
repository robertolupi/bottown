import test from 'node:test';
import assert from 'node:assert/strict';
import * as $ from 'svelte/internal/client';
import {
  detectTrimmedLineCount,
  computePreservedScrollTop,
  isScrolledToBottom,
  buildKeyedLogLines,
  updateLogTailFollow,
} from '../src/state.ts';

test('LiveLogTail reactive effect wiring: mounts cleanly and runs without effect_update_depth_exceeded', async () => {
  let lines = $.state<string[]>(['line 1', 'line 2']);
  let isFollowing = $.state<boolean>(true);
  let previousLines: string[] = [];
  let baseIndex = 0;

  let preEffectRuns = 0;
  let effectRuns = 0;
  let pendingTrimmed = 0;
  let keyedLines: any;

  let cleanup = $.effect_root(() => {
    keyedLines = $.derived(() => buildKeyedLogLines($.get(lines), baseIndex));

    $.user_pre_effect(() => {
      preEffectRuns++;
      const curr = $.get(lines) || [];
      const prev = previousLines;
      if (curr !== prev && prev.length > 0 && curr.length > 0) {
        const trimmed = detectTrimmedLineCount(prev, curr);
        if (trimmed > 0) {
          baseIndex += trimmed;
          pendingTrimmed = trimmed;
        }
      }
      previousLines = curr;
    });

    $.user_effect(() => {
      effectRuns++;
      const _ = $.get(lines);
      const fol = $.get(isFollowing);
      if (fol) {
        // following
      } else if (pendingTrimmed > 0) {
        pendingTrimmed = 0;
      }
    });
  });

  await Promise.resolve();
  assert.equal(preEffectRuns, 1, 'pre-effect must run exactly once on mount');
  assert.equal(effectRuns, 1, 'effect must run exactly once on mount');
  assert.deepEqual($.get(keyedLines), [
    { id: 0, line: 'line 1' },
    { id: 1, line: 'line 2' },
  ]);

  // 1. Appending new lines
  $.set(lines, ['line 1', 'line 2', 'line 3']);
  await Promise.resolve();
  assert.equal(preEffectRuns, 2, 'pre-effect runs once on append');
  assert.equal(effectRuns, 2, 'effect runs once on append');
  assert.equal(baseIndex, 0);
  assert.equal($.get(keyedLines).length, 3);

  // 2. Sliding window trimming 1 line from the front
  $.set(lines, ['line 2', 'line 3', 'line 4']);
  await Promise.resolve();
  assert.equal(preEffectRuns, 3, 'pre-effect runs once on trim');
  assert.equal(effectRuns, 3, 'effect runs once on trim');
  assert.equal(baseIndex, 1);
  assert.deepEqual($.get(keyedLines), [
    { id: 1, line: 'line 2' },
    { id: 2, line: 'line 3' },
    { id: 3, line: 'line 4' },
  ]);

  // 3. Pause following (user scrolled away)
  $.set(isFollowing, false);
  await Promise.resolve();
  assert.equal(effectRuns, 4, 'effect runs when follow state changes');

  // 4. More lines trimmed while paused
  $.set(lines, ['line 3', 'line 4', 'line 5']);
  await Promise.resolve();
  assert.equal(preEffectRuns, 4, 'pre-effect runs once on next trim while paused');
  assert.equal(effectRuns, 5, 'effect runs once while paused');
  assert.equal(baseIndex, 2);
  assert.deepEqual($.get(keyedLines), [
    { id: 2, line: 'line 3' },
    { id: 3, line: 'line 4' },
    { id: 4, line: 'line 5' },
  ]);

  cleanup();
});
