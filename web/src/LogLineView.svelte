<script lang="ts">
  import type { LogLine } from './types';
  import { splitToolLine } from './state';

  let { line }: { line: LogLine } = $props();

  let tool = $derived(splitToolLine(line));
</script>

<!-- Recognized calls and long assistant messages use native disclosure, so
     the concise event order remains visible without hiding full details. -->
{#if tool}
  <details class="log-line log-{line.kind}" data-tool-class={line.tool_class}>
    <summary>
      <span class="log-tool">{tool.tool}</span>{#if tool.arg}<span class="log-arg">{tool.arg}</span>{/if}{#if line.state}<span class="log-state log-state-{line.state}">· {line.state}</span>{/if}
    </summary>
    <div class="log-details">
      {#if line.parameters}
        <section aria-label="Tool parameters">
          <h4>Parameters</h4>
          <pre>{line.parameters}</pre>
        </section>
      {/if}
      {#if line.output}
        <section aria-label="Tool output">
          <h4>Output</h4>
          <pre>{line.output}</pre>
        </section>
      {/if}
      {#if !line.parameters && !line.output}<span class="log-no-details">No additional details.</span>{/if}
    </div>
  </details>
{:else if line.detail}
  <details class="log-line log-{line.kind}">
    <summary>{line.text}</summary>
    <pre class="log-details">{line.detail}</pre>
  </details>
{:else}
  <div class="log-line log-{line.kind}">{line.text}</div>
{/if}

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
    overflow-wrap: anywhere;
  }

  summary {
    cursor: pointer;
  }

  /* log-plain keeps the box's color. */
  .log-text { color: var(--log-text); }
  .log-output { color: var(--log-output); }
  .log-error { color: var(--log-error); }
  .log-status { color: var(--log-status); font-style: italic; }

  /* A tool call: details.log-tool is the line (kind tool), span.log-tool its
     tool name, colored by class; the argument and state are muted. */
  details.log-tool { color: var(--log-arg); }
  span.log-tool { color: var(--log-tool-other); }
  [data-tool-class='shell'] span.log-tool { color: var(--log-tool-shell); }
  [data-tool-class='read'] span.log-tool { color: var(--log-tool-read); }
  [data-tool-class='edit'] span.log-tool { color: var(--log-tool-edit); }
  [data-tool-class='search'] span.log-tool { color: var(--log-tool-search); }
  [data-tool-class='forge'] span.log-tool { color: var(--log-tool-forge); }
  [data-tool-class='browser'] span.log-tool { color: var(--log-tool-browser); }

  .log-arg { margin-left: 0.35rem; }
  .log-state { margin-left: 0.45rem; color: var(--log-status); }
  .log-state-failed { color: var(--log-error); }

  .log-details {
    margin: 5px 0 2px 18px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    color: var(--log-output);
  }

  .log-details section + section { margin-top: 8px; }
  .log-details h4 {
    margin: 0 0 2px;
    color: var(--log-status);
    font-size: 11px;
    font-weight: 500;
  }
  .log-details pre {
    margin: 0;
    max-height: 240px;
    overflow: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font: inherit;
  }
  .log-no-details { color: var(--log-status); }
</style>
