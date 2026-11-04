<script lang="ts">
  // A run or flow summary in a history cell (#133): one line, clipped
  // with an ellipsis, and when the line hides text a "more" button that
  // shows all of it inline; "less" clips it again.
  import { watchTruncation } from './truncation';

  interface Props {
    text: string;
  }

  let { text }: Props = $props();

  let expanded = $state(false);
  let truncated = $state(false);
</script>

<div
  class="summary-text"
  class:expanded
  use:watchTruncation={{ key: [text, expanded], onChange: (t) => (truncated = t) }}
>
  <span class="clip" title={expanded ? undefined : text || undefined}>{text || '—'}</span>
  {#if truncated || expanded}
    <button
      type="button"
      class="summary-toggle"
      aria-expanded={expanded}
      title={expanded ? 'Show the summary on one line' : 'Show the whole summary'}
      onclick={(e) => {
        e.stopPropagation();
        expanded = !expanded;
      }}
    >{expanded ? 'less' : 'more'}</button>
  {/if}
</div>

<style>
  .summary-text {
    display: flex;
    align-items: baseline;
    gap: 6px;
    min-width: 0;
  }

  .clip {
    flex: 0 1 auto;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .summary-text.expanded {
    align-items: flex-end;
  }

  .expanded .clip {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .summary-toggle {
    flex: none;
    padding: 0;
    border: none;
    background: none;
    color: var(--accent);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
    text-decoration: underline;
  }
</style>
