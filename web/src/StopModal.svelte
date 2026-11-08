<script lang="ts">
  // Confirm stopping a run, a flow (runId flow-N), or a milestone run
  // (milestone-SLUG) (§5).
  import Modal from './Modal.svelte';

  interface Props {
    runId: string;
    actionLoading: boolean;
    onconfirm: () => void;
    onclose: () => void;
  }

  let { runId, actionLoading, onconfirm, onclose }: Props = $props();

  let milestone = $derived(runId.startsWith('milestone-') ? runId.slice('milestone-'.length) : '');
</script>

<Modal>
  {#if milestone}
    <h3>Stop Milestone Run</h3>
    <p class="muted">
      Are you sure you want to stop the milestone run for <strong>{milestone}</strong>?
      It interrupts the flows it started, which end their runs, and exits once they have stopped.
      Starting it again resumes.
    </p>
  {:else if runId.startsWith('land-')}
    <h3>Stop Landing</h3>
    <p class="muted">Stop landing PR !{runId.slice(5)}? This interrupts the checks and landing worker. The PR may remain unmerged at its landing commit; you can Land it again.</p>
  {:else}
    <h3>Stop Run & Flow</h3>
    <p class="muted">
      Are you sure you want to stop run <strong>{runId}</strong>?
      This will kill the harness process group and end the run as failed. If this run is part of a flow, the flow will also stop.
    </p>
  {/if}
  <div class="modal-actions">
    <button class="btn btn-secondary" onclick={onclose} disabled={actionLoading}>
      Cancel
    </button>
    <button class="btn btn-danger" onclick={onconfirm} disabled={actionLoading}>
      {actionLoading ? 'Stopping...' : milestone ? 'Confirm & Stop Milestone Run' : 'Confirm & Stop Run'}
    </button>
  </div>
</Modal>
