<script lang="ts">
  // Start a single run (§5): the form's values live in App, so they stay
  // between openings and the Milestones view can preset the issue.
  import type { ActorDetail } from './types';
  import type { RunForm } from './actions';
  import Modal from './Modal.svelte';

  interface Props {
    actors: ActorDetail[] | undefined;
    form: RunForm;
    actionLoading: boolean;
    onsubmit: () => void;
    onclose: () => void;
  }

  let { actors, form = $bindable(), actionLoading, onsubmit, onclose }: Props = $props();
</script>

<Modal>
  <div class="modal-header">
    <h3>Start Single Run</h3>
    <button class="close-btn" onclick={onclose}>✕</button>
  </div>

  <form onsubmit={(e) => { e.preventDefault(); onsubmit(); }} class="form-grid">
    <div class="form-row">
      <div class="form-group">
        <label for="run-kind">Run Kind</label>
        <select id="run-kind" class="input" bind:value={form.kind}>
          <option value="implement">Implement (Issue)</option>
          <option value="verify">Verify (PR)</option>
          <option value="discuss">Discuss (Issue/PR)</option>
          <option value="plan">Plan (Kickoff issue)</option>
        </select>
      </div>

      <div class="form-group">
        <label for="run-target">{form.kind === 'verify' ? 'PR Number' : 'Issue Number'}</label>
        <input
          id="run-target"
          type="number"
          class="input"
          placeholder="e.g. 51"
          required
          bind:value={form.target}
        />
      </div>
    </div>

    <div class="form-group">
      <label for="run-act">Actor</label>
      <select id="run-act" class="input" bind:value={form.actor}>
        {#if actors}
          {#each actors as act}
            <option value={act.name}>{act.name} ({act.family})</option>
          {/each}
        {/if}
      </select>
    </div>

    {#if form.kind === 'discuss'}
      <div class="form-group">
        <label for="run-q">Question (optional)</label>
        <input
          id="run-q"
          type="text"
          class="input"
          placeholder="What should the agent answer?"
          bind:value={form.question}
        />
      </div>
    {/if}

    {#if form.kind === 'verify'}
      <div class="form-checkbox">
        <label>
          <input type="checkbox" bind:checked={form.allowSameFamily} />
          Allow same-family verifier (waiver)
        </label>
      </div>
    {/if}

    <div class="modal-actions">
      <button type="button" class="btn btn-secondary" onclick={onclose}>
        Cancel
      </button>
      <button type="submit" class="btn btn-primary" disabled={actionLoading || !form.target}>
        {actionLoading ? 'Starting...' : 'Start Run'}
      </button>
    </div>
  </form>
</Modal>
