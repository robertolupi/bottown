<script lang="ts">
  // One event of an issue's timeline: a dot on the line, its title (a link
  // when linked and it has a URL), time, detail and actor. standalone marks
  // an event outside any run in a flow's combined timeline.
  import type { TimelineEvent } from './types';
  import { formatTime } from './format';

  interface Props {
    ev: TimelineEvent;
    standalone?: boolean;
    linked?: boolean;
  }

  let { ev, standalone = false, linked = true }: Props = $props();
</script>

<div class="timeline-item timeline-{ev.type}" class:timeline-standalone={standalone}>
  <div class="timeline-dot"></div>
  <div class="timeline-body">
    <div class="timeline-header">
      <span class="timeline-title">
        {#if linked && ev.url}
          <a href={ev.url} target="_blank" rel="noreferrer">{ev.title}</a>
        {:else}
          {ev.title}
        {/if}
      </span>
      <span class="muted text-sm">{formatTime(ev.time)}</span>
    </div>
    {#if ev.detail}
      <div class="timeline-detail">{ev.detail}</div>
    {/if}
    {#if ev.actor}
      <div class="timeline-meta muted text-sm">by {ev.actor}</div>
    {/if}
  </div>
</div>

<style>
  .timeline-item {
    position: relative;
    padding-bottom: 20px;
    border-left: 2px solid var(--border);
    padding-left: 16px;
  }

  .timeline-item:last-child {
    border-left: 2px solid transparent;
  }

  .timeline-dot {
    position: absolute;
    left: -7px;
    top: 2px;
    width: 12px;
    height: 12px;
    border-radius: 50%;
    background: var(--primary);
    border: 2px solid var(--card-bg);
  }

  .timeline-review .timeline-dot { background: var(--warning); }
  .timeline-pr_merged .timeline-dot { background: var(--success); }

  .timeline-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-weight: 600;
    font-size: 13px;
  }

  .timeline-detail {
    font-size: 13px;
    margin-top: 4px;
    color: var(--text);
  }

  .timeline-meta {
    margin-top: 2px;
  }

  .timeline-standalone {
    margin-left: 16px;
  }
</style>
