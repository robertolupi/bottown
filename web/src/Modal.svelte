<script lang="ts">
  // A modal dialog over a dimmed backdrop. With onbackdrop, a click on the
  // backdrop or Escape calls it (to close); without, only the dialog's own
  // buttons do. wide is for the timeline. The dialogs' shared parts
  // (header, actions, forms, lists) are styled here for every modal.
  import type { Snippet } from 'svelte';

  interface Props {
    wide?: boolean;
    onbackdrop?: () => void;
    children: Snippet;
  }

  let { wide = false, onbackdrop, children }: Props = $props();
</script>

{#if onbackdrop}
  <div
    class="modal-backdrop"
    role="presentation"
    onclick={() => onbackdrop()}
    onkeydown={(e) => { if (e.key === 'Escape') onbackdrop(); }}
  >
    <div
      class="modal-card"
      class:timeline-modal={wide}
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
    >
      {@render children()}
    </div>
  </div>
{:else}
  <div class="modal-backdrop">
    <div class="modal-card" class:timeline-modal={wide} role="dialog" aria-modal="true" tabindex="-1">
      {@render children()}
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(0, 0, 0, 0.6);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .modal-card {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    max-width: 520px;
    width: 90%;
    padding: 24px;
    box-shadow: 0 10px 25px rgba(0, 0, 0, 0.2);
  }

  .timeline-modal {
    max-width: 680px;
  }

  .modal-card :global {
    .modal-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      border-bottom: 1px solid var(--border);
      padding-bottom: 12px;
    }

    .modal-header h3 {
      margin: 0;
    }

    .modal-commits-list {
      max-height: 240px;
      overflow-y: auto;
      padding-left: 20px;
      margin: 16px 0 24px;
      font-size: 13px;
    }

    .modal-commits-list li {
      margin-bottom: 6px;
    }

    .modal-output {
      max-height: 240px;
      overflow: auto;
      font-size: 12px;
      padding: 8px;
      margin: 0 0 16px;
      background: rgba(125, 125, 125, 0.1);
      border-radius: 6px;
      white-space: pre-wrap;
    }

    .modal-actions {
      display: flex;
      justify-content: flex-end;
      gap: 12px;
      margin-top: 20px;
    }

    .input {
      background: var(--card-bg);
      border: 1px solid var(--border);
      color: var(--text);
      padding: 8px 12px;
      border-radius: 6px;
      font-size: 13px;
    }

    .form-grid {
      display: flex;
      flex-direction: column;
      gap: 16px;
      margin-top: 16px;
    }

    .form-group {
      display: flex;
      flex-direction: column;
      gap: 6px;
    }

    .form-group label {
      font-size: 13px;
      font-weight: 500;
    }

    .form-row {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 12px;
    }

    .form-checkbox {
      margin: 8px 0;
      font-size: 13px;
    }

    .form-checkbox label {
      display: flex;
      align-items: center;
      gap: 8px;
      cursor: pointer;
    }

    .font-mono {
      font-family: monospace;
    }
  }
</style>
