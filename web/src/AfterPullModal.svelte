<script lang="ts">
  // Confirm running the after-pull steps that main's .bottown.toml declares
  // (§4.4), with their output once run.
  import type { RepoSettingsCheck } from './types';
  import Modal from './Modal.svelte';

  interface Props {
    settings: RepoSettingsCheck | undefined;
    output: string | null;
    actionLoading: boolean;
    onconfirm: () => void;
    onclose: () => void;
  }

  let { settings, output, actionLoading, onconfirm, onclose }: Props = $props();
</script>

<Modal onbackdrop={() => { if (!actionLoading) onclose(); }}>
  <h3>Run the after-pull steps</h3>
  <p>
    <code>{settings?.file}</code> on <code>{settings?.branch}</code>
    ({settings?.commit?.slice(0, 12)}) declares these commands. They run one at a time,
    with <code>sh -c</code>, in the main checkout, stopping at the first that fails:
  </p>
  <ul class="modal-commits-list">
    {#each settings?.after_pull || [] as step}
      <li><code>{step}</code></li>
    {/each}
  </ul>
  {#if output}
    <pre class="modal-output">{output}</pre>
  {/if}
  <div class="modal-actions">
    <button class="btn btn-secondary" onclick={onclose} disabled={actionLoading}>
      Close
    </button>
    <button class="btn btn-accent" onclick={onconfirm} disabled={actionLoading}>
      {actionLoading ? 'Running...' : 'Confirm & Run'}
    </button>
  </div>
</Modal>
