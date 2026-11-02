<script lang="ts">
  import { onMount } from 'svelte';
  import {
    detectTrimmedLineCount,
    computePreservedScrollTop,
    isScrolledToBottom,
    buildKeyedLogLines,
  } from './state';

  let { runId, lines = [] }: { runId: string; lines?: string[] } = $props();

  let scrollContainer = $state<HTMLDivElement | null>(null);
  let isFollowing = $state<boolean>(true);
  let previousLines = $state<string[]>([]);
  let baseIndex = $state<number>(0);
  let isProgrammaticScroll = false;

  let pendingTrimmedHeight = 0;
  let pendingOldScrollTop = 0;

  let keyedLines = $derived(buildKeyedLogLines(lines, baseIndex));

  function scrollToBottom() {
    if (!scrollContainer) return;
    isProgrammaticScroll = true;
    scrollContainer.scrollTop = scrollContainer.scrollHeight;
    setTimeout(() => {
      isProgrammaticScroll = false;
    }, 50);
  }

  function handleFollowClick() {
    isFollowing = true;
    scrollToBottom();
  }

  function handleScroll() {
    if (isProgrammaticScroll || !scrollContainer) return;
    const atBottom = isScrolledToBottom(
      scrollContainer.scrollTop,
      scrollContainer.scrollHeight,
      scrollContainer.clientHeight,
    );
    isFollowing = atBottom;
  }

  onMount(() => {
    previousLines = lines ? [...lines] : [];
    if (lines && lines.length > 0) {
      scrollToBottom();
    }
  });

  $effect.pre(() => {
    const curr = lines || [];
    const prev = previousLines;

    if (curr !== prev && prev.length > 0 && curr.length > 0) {
      const trimmedCount = detectTrimmedLineCount(prev, curr);
      if (trimmedCount > 0) {
        baseIndex += trimmedCount;
        if (!isFollowing && scrollContainer) {
          const children = scrollContainer.querySelectorAll('.log-line');
          if (children.length > 0) {
            const first = children[0] as HTMLElement;
            if (trimmedCount < children.length) {
              const target = children[trimmedCount] as HTMLElement;
              pendingTrimmedHeight = target.offsetTop - first.offsetTop;
            } else {
              pendingTrimmedHeight = scrollContainer.scrollHeight;
            }
            pendingOldScrollTop = scrollContainer.scrollTop;
          }
        }
      }
    }
    previousLines = [...curr];
  });

  $effect(() => {
    // Reading lines registers dependency so effect runs when DOM is updated with new lines
    const _ = lines;

    if (!scrollContainer) return;

    if (isFollowing) {
      scrollToBottom();
    } else if (pendingTrimmedHeight > 0) {
      const newScroll = computePreservedScrollTop(pendingOldScrollTop, pendingTrimmedHeight);
      isProgrammaticScroll = true;
      scrollContainer.scrollTop = newScroll;
      setTimeout(() => {
        isProgrammaticScroll = false;
      }, 50);
      pendingTrimmedHeight = 0;
      pendingOldScrollTop = 0;
    }
  });
</script>

<div class="log-tail-box">
  <div class="log-tail-header">
    <span>Live Tail (.bottown/runs/{runId}.log)</span>
    <button
      type="button"
      class="log-follow-btn"
      class:following={isFollowing}
      class:paused={!isFollowing}
      onclick={handleFollowClick}
      title={isFollowing ? 'Automatically following latest output' : 'Jump to latest output and resume following'}
      aria-label={isFollowing ? 'Following output' : 'Follow output / Jump to latest'}
      data-action="follow"
    >
      {#if isFollowing}
        <span class="follow-dot" aria-hidden="true">●</span>
        <span>Following</span>
      {:else}
        <span class="follow-arrow" aria-hidden="true">↓</span>
        <span>Follow output<span class="sr-only"> / Jump to latest</span></span>
      {/if}
    </button>
  </div>
  <div
    class="log-tail-content"
    bind:this={scrollContainer}
    onscroll={handleScroll}
  >
    {#if !lines || lines.length === 0}
      <div class="log-line muted">Waiting for agent activity...</div>
    {:else}
      {#each keyedLines as item (item.id)}
        <div class="log-line">{item.text}</div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .log-tail-box {
    background: #090d16;
    color: #e2e8f0;
    font-family: monospace;
    font-size: 12px;
  }

  .log-tail-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 6px 12px;
    background: #111827;
    color: #94a3b8;
    font-size: 11px;
    border-bottom: 1px solid #1e293b;
    gap: 8px;
  }

  .log-follow-btn {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 4px;
    background: rgba(125, 125, 125, 0.15);
    color: #94a3b8;
    border: 1px solid rgba(255, 255, 255, 0.08);
    display: inline-flex;
    align-items: center;
    gap: 5px;
    cursor: pointer;
    line-height: 1.4;
    font-family: inherit;
    transition: all 0.15s ease;
  }

  .log-follow-btn:hover {
    background: rgba(125, 125, 125, 0.25);
    color: #f1f5f9;
  }

  .log-follow-btn.following {
    background: rgba(16, 185, 129, 0.12);
    color: #34d399;
    border-color: rgba(16, 185, 129, 0.25);
  }

  .log-follow-btn.paused {
    background: rgba(59, 130, 246, 0.15);
    color: #60a5fa;
    border-color: rgba(59, 130, 246, 0.3);
    font-weight: 500;
  }

  .log-follow-btn.paused:hover {
    background: rgba(59, 130, 246, 0.25);
    color: #93c5fd;
  }

  .follow-dot {
    font-size: 8px;
    color: #10b981;
  }

  .follow-arrow {
    font-size: 11px;
    line-height: 1;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  .log-tail-content {
    max-height: 180px;
    overflow-y: auto;
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    overflow-anchor: none;
  }

  .log-line {
    white-space: pre-wrap;
    word-break: break-all;
  }

  .muted {
    color: #64748b;
  }
</style>
