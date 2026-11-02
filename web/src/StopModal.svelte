<script lang="ts">
  // Confirm stopping a run, or a flow (runId flow-N) (§5).
  import Modal from './Modal.svelte';

  interface Props {
    runId: string;
    actionLoading: boolean;
    onconfirm: () => void;
    onclose: () => void;
  }

  let { runId, actionLoading, onconfirm, onclose }: Props = $props();
</script>

<Modal>
  <h3>Stop Run & Flow</h3>
  <p class="muted">
    Are you sure you want to stop run <strong>{runId}</strong>?
    This will kill the harness process group and end the run as failed. If this run is part of a flow, the flow will also stop.
  </p>
  <div class="modal-actions">
    <button class="btn btn-secondary" onclick={onclose} disabled={actionLoading}>
      Cancel
    </button>
    <button class="btn btn-danger" onclick={onconfirm} disabled={actionLoading}>
      {actionLoading ? 'Stopping...' : 'Confirm & Stop Run'}
    </button>
  </div>
</Modal>
