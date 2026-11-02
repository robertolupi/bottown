<script lang="ts">
  // Confirm pushing main to origin, listing the commits that will go.
  import type { UnpushedCommit } from './types';
  import Modal from './Modal.svelte';

  interface Props {
    commits: UnpushedCommit[];
    actionLoading: boolean;
    onconfirm: () => void;
    onclose: () => void;
  }

  let { commits, actionLoading, onconfirm, onclose }: Props = $props();
</script>

<Modal>
  <h3>Push main to origin</h3>
  <p class="muted">The following commit(s) will be pushed to origin/main:</p>
  <ul class="modal-commits-list">
    {#each commits as c}
      <li>
        <strong class="font-mono">{c.short_sha}</strong> {c.subject}
        {#if c.pr_number}
          <span class="badge pr-badge">!{c.pr_number}</span>
        {/if}
        <span class="muted text-sm">— {c.author}</span>
      </li>
    {/each}
  </ul>
  <div class="modal-actions">
    <button class="btn btn-secondary" onclick={onclose} disabled={actionLoading}>
      Cancel
    </button>
    <button class="btn btn-accent" onclick={onconfirm} disabled={actionLoading}>
      {actionLoading ? 'Pushing...' : 'Confirm & Push to Origin'}
    </button>
  </div>
</Modal>

<style>
  .pr-badge {
    background: rgba(99, 102, 241, 0.15);
    color: #818cf8;
    text-decoration: none;
    font-family: monospace;
    font-weight: bold;
    padding: 2px 6px;
    border-radius: 4px;
  }
  .pr-badge:hover { text-decoration: underline; }
</style>
