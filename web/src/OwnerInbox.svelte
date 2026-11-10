<script lang="ts">
  // Needs you (§4.1, monitor-ui design §3): what waits on the owner in
  // this repository, as one line of counts by kind that expands in place
  // to the items, each with a link and, for some, a command to copy. Expansion is
  // remembered in localStorage.
  import CommandCopy from './CommandCopy.svelte';
  import type { InboxItem } from './types';
  import { filterInboxItems } from './state';
  import { inboxCounts, loadInboxExpanded, saveInboxExpanded } from './now';
  import IssueReference from './IssueReference.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    inbox: InboxItem[];
    inboxError: string | undefined;
  }

  let { inbox, inboxError }: Props = $props();
  let showActorFiled = $state(false);
  let expanded = $state(loadInboxExpanded());
  let visibleInbox = $derived(filterInboxItems(inbox, showActorFiled));
  let counts = $derived(inboxCounts(visibleInbox));
  // Actor-filed issues the filter hides: the expanded list offers them.
  let hiddenFiled = $derived((inbox ?? []).length - visibleInbox.length);

  function toggle() {
    expanded = !expanded;
    saveInboxExpanded(expanded);
  }
</script>

<section class="card full-width needs-you" aria-label="Needs you">
  <div class="needs-you-line">
    <h2>Needs you</h2>
    {#if inboxError}
      <span class="badge badge-error">Failed to load inbox: {inboxError}</span>
    {:else if counts.length === 0}
      <span class="muted inbox-zero-line">Nothing waiting on you.</span>
    {:else}
      <span class="needs-you-counts">
        {#each counts as c (c.kind)}
          <span class="badge badge-{c.tone}" data-kind={c.kind}>{c.label}</span>
        {/each}
      </span>
    {/if}
    {#if !inboxError && (visibleInbox.length > 0 || hiddenFiled > 0)}
      <button class="btn btn-sm btn-secondary needs-you-toggle" aria-expanded={expanded} onclick={toggle}>
        {expanded ? 'collapse ▴' : 'expand ▾'}
      </button>
    {/if}
  </div>

  {#if expanded && !inboxError && (visibleInbox.length > 0 || hiddenFiled > 0)}
  <div class="card-body">
    <label class="muted actor-filed-toggle">
      <input type="checkbox" bind:checked={showActorFiled} />
      Show actor-filed issues
    </label>
    {#if visibleInbox.length > 0}
      <div class="inbox-list">
        {#each visibleInbox as item (item.id)}
          <div class="inbox-item">
            <div class="inbox-badge-col">
              {#if item.kind === 'ready_to_land'}
                <span class="badge badge-success">ready to land</span>
              {:else if item.kind === 'conflicts_with_base'}
                <span class="badge badge-error">conflicts with base</span>
              {:else if item.kind === 'stopped_flow'}
                <span class="badge badge-warning">stopped flow</span>
              {:else if item.kind === 'broken_run'}
                <span class="badge badge-error">broken run</span>
              {:else if item.kind === 'discuss_answer'}
                <span class="badge badge-accent">discuss answer</span>
              {:else if item.kind === 'actor_filed_issue'}
                <span class="badge badge-neutral">actor filed</span>
              {/if}
            </div>

            <div class="inbox-content-col">
              <div class="inbox-title-row">
                {#if item.issue}
                  <IssueReference
                    number={item.issue}
                    href={item.target_url}
                    label={item.target}
                    title={item.issue_title}
                    showTitle
                  />
                {:else}
                  <TargetLink href={item.target_url} label={item.target} />
                  <span class="inbox-title">{item.title}</span>
                {/if}
              </div>
              <div class="inbox-desc muted">{item.description}</div>
            </div>

            <div class="inbox-action-col">
              {#if item.action}
                <CommandCopy command={item.action.command} />
              {/if}
              <a class="btn btn-sm btn-primary" href={item.target_url} target="_blank" rel="noreferrer">
                View ↗
              </a>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
  {/if}
</section>

<style>
  .needs-you-line {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
    padding: 12px 16px;
  }

  .needs-you-line h2 {
    margin: 0;
    font-size: 16px;
  }

  .needs-you-counts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    flex: 1;
  }

  .inbox-zero-line {
    flex: 1;
  }

  .needs-you-toggle {
    margin-left: auto;
  }

  .actor-filed-toggle {
    display: block;
    margin-bottom: 12px;
  }

  .inbox-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
    max-height: var(--panel-max-height);
    max-block-size: var(--panel-max-height);
    overflow: auto;
  }

  .inbox-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 16px;
    background: rgba(125, 125, 125, 0.05);
    border: 1px solid var(--border);
    border-radius: 8px;
    gap: 16px;
    flex-wrap: wrap;
  }

  .inbox-badge-col {
    min-width: 140px;
  }

  .inbox-content-col {
    flex: 1;
    min-width: 250px;
  }

  .inbox-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
    font-size: 14px;
    margin-bottom: 4px;
  }

  .inbox-desc {
    font-size: 13px;
  }

  .inbox-action-col {
    display: flex;
    align-items: center;
    gap: 8px;
  }
</style>
