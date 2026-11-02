<script lang="ts">
  // Confirm cleaning up runs, bottown run gc [--failed] (§5).
  import Modal from './Modal.svelte';

  interface Props {
    includeFailed: boolean;
    actionLoading: boolean;
    onconfirm: () => void;
    onclose: () => void;
  }

  let { includeFailed = $bindable(), actionLoading, onconfirm, onclose }: Props = $props();
</script>

<Modal>
  <h3>Clean up Runs (bottown run gc)</h3>
  <p class="muted">
    Remove local run worktrees and purges server records for finished runs.
  </p>
  <div class="form-checkbox">
    <label>
      <input type="checkbox" bind:checked={includeFailed} />
      Include failed runs with leftover worktrees (--failed)
    </label>
  </div>
  <div class="modal-actions">
    <button class="btn btn-secondary" onclick={onclose} disabled={actionLoading}>
      Cancel
    </button>
    <button class="btn btn-accent" onclick={onconfirm} disabled={actionLoading}>
      {actionLoading ? 'Cleaning up...' : 'Confirm & Run GC'}
    </button>
  </div>
</Modal>
