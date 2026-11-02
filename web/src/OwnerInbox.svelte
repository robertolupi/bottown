<script lang="ts">
  // The owner inbox (§4.1): what waits on the owner in this repository,
  // each item with a link and, for some, an action (gc, stop) that opens
  // its confirmation dialog.
  import type { InboxItem } from './types';
  import { resolveInboxAction } from './state';
  import IssueReference from './IssueReference.svelte';
  import TargetLink from './TargetLink.svelte';

  interface Props {
    inbox: InboxItem[];
    inboxError: string | undefined;
    onGC: (includeFailed: boolean) => void;
    onStop: (runId: string) => void;
  }

  let { inbox, inboxError, onGC, onStop }: Props = $props();
</script>

<section class="card full-width">
  <div class="card-header">
    <div class="card-header-left">
      <h2>Owner Inbox</h2>
      {#if inboxError}
        <span class="badge badge-error">Error</span>
      {:else if inbox && inbox.length > 0}
        <span class="badge badge-warning">{inbox.length} item{inbox.length > 1 ? 's' : ''}</span>
      {:else}
        <span class="badge badge-success">Inbox zero</span>
      {/if}
    </div>
  </div>

  <div class="card-body">
    {#if inboxError}
      <div class="banner banner-error">
        <span>Failed to load inbox: {inboxError}</span>
      </div>
    {:else if !inbox || inbox.length === 0}
      <div class="inbox-zero">
        <span class="inbox-zero-icon">✓</span>
        <span>Nothing waiting on the owner in this repository.</span>
      </div>
    {:else}
      <div class="inbox-list">
        {#each inbox as item (item.id)}
          <div class="inbox-item">
            <div class="inbox-badge-col">
              {#if item.kind === 'ready_to_merge'}
                <span class="badge badge-success">ready to merge</span>
              {:else if item.kind === 'conflicts_with_main'}
                <span class="badge badge-error">conflicts with main</span>
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
                <button
                  class="btn btn-sm btn-secondary"
                  onclick={() => {
                    const resolved = resolveInboxAction(item);
                    if (resolved?.modal === 'gc') {
                      onGC(resolved.includeFailed);
                    } else if (resolved?.modal === 'stop') {
                      onStop(resolved.stopRunId);
                    }
                  }}
                >
                  {item.action.label}
                </button>
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
</section>

<style>
  .inbox-list {
    display: flex;
    flex-direction: column;
    gap: 12px;
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
