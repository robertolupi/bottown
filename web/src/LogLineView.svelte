<script lang="ts">
  import type { LogLine } from './types';
  import { splitToolLine } from './state';

  let { line }: { line: LogLine } = $props();

  let tool = $derived(splitToolLine(line));
</script>

<!-- One line of a run log, colored by kind and, for tool calls, tool class
     (palette below). Kept on one line: the line is white-space: pre-wrap. -->
<div class="log-line log-{line.kind}" data-tool-class={line.tool_class}>{#if tool}<span class="log-tool">{tool.tool}</span>{tool.arg}{:else}{line.text}{/if}</div>

<style>
  /* The palette, on the log boxes' dark background (#090d16); every color
     has at least WCAG AA contrast on it. The legend in LiveLogTail.svelte
     uses the same properties. */
  :global(.log-tail-box) {
    --log-text: #f8fafc;
    --log-arg: #94a3b8;
    --log-output: #8b96a8;
    --log-error: #f87171;
    --log-status: #a1a1aa;
    --log-tool-shell: #4ade80;
    --log-tool-read: #60a5fa;
    --log-tool-edit: #fbbf24;
    --log-tool-search: #22d3ee;
    --log-tool-forge: #c084fc;
    --log-tool-browser: #f472b6;
    --log-tool-other: #cbd5e1;
  }

  .log-line {
    white-space: pre-wrap;
    word-break: break-all;
  }

  /* log-plain keeps the box's color. */
  .log-text { color: var(--log-text); }
  .log-output { color: var(--log-output); }
  .log-error { color: var(--log-error); }
  .log-status { color: var(--log-status); font-style: italic; }

  /* A tool call: div.log-tool is the line (kind tool), span.log-tool its
     tool name, colored by class; the argument is muted. */
  div.log-tool { color: var(--log-arg); }
  span.log-tool { color: var(--log-tool-other); }
  [data-tool-class='shell'] > span.log-tool { color: var(--log-tool-shell); }
  [data-tool-class='read'] > span.log-tool { color: var(--log-tool-read); }
  [data-tool-class='edit'] > span.log-tool { color: var(--log-tool-edit); }
  [data-tool-class='search'] > span.log-tool { color: var(--log-tool-search); }
  [data-tool-class='forge'] > span.log-tool { color: var(--log-tool-forge); }
  [data-tool-class='browser'] > span.log-tool { color: var(--log-tool-browser); }
</style>
