<script module lang="ts">
  let nextTooltipID = 0;
</script>

<script lang="ts">
  import { onMount } from 'svelte';
  import DOMPurify from 'dompurify';
  import { marked } from 'marked';
  import { issueDetailsCache, type IssueDetails } from './issueDetails';

  interface Props {
    number: number;
    href: string;
    label: string;
    title?: string;
    showTitle?: boolean;
  }

  let { number, href, label, title = '', showTitle = false }: Props = $props();

  const tooltipID = `issue-tooltip-${++nextTooltipID}`;
  let anchor: HTMLAnchorElement;
  let details = $state<IssueDetails | null>(null);
  let loading = $state(false);
  let loadError = $state('');
  let hovered = $state(false);
  let focused = $state(false);
  let popupStyle = $state('');

  let open = $derived(hovered || focused);
  let displayTitle = $derived(details?.title || title);
  let renderedBody = $derived(
    details?.body
      ? DOMPurify.sanitize(marked.parse(details.body, { async: false, breaks: true }), {
          USE_PROFILES: { html: true },
        })
      : '',
  );

  async function loadDetails() {
    if (details || loading) return;
    loading = true;
    loadError = '';
    const requestedIssue = number;
    try {
      const loaded = await issueDetailsCache.load(requestedIssue);
      if (requestedIssue === number) details = loaded;
    } catch {
      if (requestedIssue === number) loadError = 'Issue details unavailable.';
    } finally {
      if (requestedIssue === number) loading = false;
    }
  }

  function positionPopup() {
    const rect = anchor.getBoundingClientRect();
    const width = Math.min(440, window.innerWidth - 24);
    const left = Math.max(12, Math.min(rect.left, window.innerWidth - width - 12));
    const spaceBelow = window.innerHeight - rect.bottom - 16;
    const below = spaceBelow >= 180;
    const available = below ? spaceBelow : rect.top - 16;
    const maxHeight = Math.max(120, Math.min(440, available));
    popupStyle = below
      ? `left: ${left}px; top: ${rect.bottom + 8}px; width: ${width}px; max-height: ${maxHeight}px;`
      : `left: ${left}px; bottom: ${window.innerHeight - rect.top + 8}px; width: ${width}px; max-height: ${maxHeight}px;`;
  }

  function showFromMouse() {
    hovered = true;
    positionPopup();
    void loadDetails();
  }

  function showFromKeyboard() {
    focused = true;
    positionPopup();
    void loadDetails();
  }

  function keydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      hovered = false;
      focused = false;
    }
  }

  onMount(() => {
    if (showTitle && !title) void loadDetails();
  });
</script>

<span class="issue-reference">
  <a
    bind:this={anchor}
    class="pr-badge issue-reference-link"
    {href}
    target="_blank"
    rel="noreferrer"
    aria-describedby={open ? tooltipID : undefined}
    onclick={(event) => event.stopPropagation()}
    onmouseenter={showFromMouse}
    onmouseleave={() => (hovered = false)}
    onfocus={showFromKeyboard}
    onblur={() => (focused = false)}
    onkeydown={keydown}
  >
    {label}
  </a>
  {#if showTitle && displayTitle}
    <span class="issue-title">{displayTitle}</span>
  {/if}
  {#if open}
    <div id={tooltipID} class="issue-popup" role="tooltip" style={popupStyle}>
      <div class="issue-popup-header">
        <strong>{displayTitle || `Issue #${number}`}</strong>
        {#if details?.state}<span class="issue-state">{details.state}</span>{/if}
      </div>
      {#if loading && !details}
        <p class="issue-popup-muted">Loading issue details…</p>
      {:else if loadError && !details}
        <p class="issue-popup-muted">{loadError}</p>
      {:else if details}
        {#if renderedBody}
          <div class="issue-markdown">{@html renderedBody}</div>
        {:else}
          <p class="issue-popup-muted">No description.</p>
        {/if}
      {/if}
    </div>
  {/if}
</span>

<style>
  .issue-reference {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .issue-reference-link {
    background: rgba(99, 102, 241, 0.15);
    color: #818cf8;
    text-decoration: none;
    font-family: monospace;
    font-weight: bold;
    padding: 2px 6px;
    border-radius: 4px;
    flex-shrink: 0;
  }

  .issue-reference-link:hover {
    text-decoration: underline;
  }

  .issue-title {
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .issue-popup {
    position: fixed;
    z-index: 1000;
    overflow-y: auto;
    padding: 14px 16px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--card-bg);
    color: var(--text);
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.25);
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
    font-size: 13px;
    font-weight: 400;
    line-height: 1.5;
    text-align: left;
  }

  .issue-popup-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 8px;
  }

  .issue-state {
    color: var(--text-muted);
    font-size: 12px;
    white-space: nowrap;
  }

  .issue-popup-muted {
    margin: 8px 0 0;
    color: var(--text-muted);
  }

  .issue-markdown {
    overflow-wrap: anywhere;
  }

  .issue-markdown :global(p),
  .issue-markdown :global(ul),
  .issue-markdown :global(ol),
  .issue-markdown :global(pre) {
    margin: 8px 0;
  }

  .issue-markdown :global(p:first-child),
  .issue-markdown :global(ul:first-child),
  .issue-markdown :global(ol:first-child),
  .issue-markdown :global(pre:first-child) {
    margin-top: 0;
  }

  .issue-markdown :global(p:last-child),
  .issue-markdown :global(ul:last-child),
  .issue-markdown :global(ol:last-child),
  .issue-markdown :global(pre:last-child) {
    margin-bottom: 0;
  }

  .issue-markdown :global(pre) {
    overflow-x: auto;
    white-space: pre-wrap;
  }

  .issue-markdown :global(img) {
    max-width: 100%;
  }
</style>
