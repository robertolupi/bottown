<script lang="ts">
  // Start `bottown milestone run SLUG` for an epic: its --agents pool and
  // --parallel. The server checks the pool with the parser the command
  // uses; its message (error) shows here, under the field.
  import type { ActorDetail } from './types';
  import type { MilestoneRunForm } from './actions';
  import Modal from './Modal.svelte';

  interface Props {
    actors: ActorDetail[] | undefined;
    form: MilestoneRunForm;
    error: string | null;
    actionLoading: boolean;
    onsubmit: () => void;
    onclose: () => void;
  }

  let { actors, form = $bindable(), error, actionLoading, onsubmit, onclose }: Props = $props();

  let valid = $derived(form.agents.trim() !== '' && Number(form.parallel) >= 1);
</script>

<Modal>
  <div class="modal-header">
    <h3>Start a Milestone Run</h3>
    <button class="close-btn" onclick={onclose}>✕</button>
  </div>
  <p class="muted text-sm">
    Drives <code>{form.slug}</code>'s ready children as flows on <code>milestone/{form.slug}</code>,
    pairing actors of different families from the pool, and finishes the milestone when they are all closed.
  </p>

  <form onsubmit={(e) => { e.preventDefault(); if (valid) onsubmit(); }} class="form-grid">
    <div class="form-group">
      <label for="ms-run-agents">Agents</label>
      <input
        id="ms-run-agents"
        type="text"
        class="input font-mono"
        placeholder="claude=*,agy:MODEL=1,kimi=2"
        required
        bind:value={form.agents}
      />
      <span class="muted text-sm">
        <code>actor[:model]=N</code> entries, N the most flows at once or <code>*</code>.
        {#if actors?.length}
          Actors: {actors.map((a) => `${a.name} (${a.family})`).join(', ')}.
        {/if}
      </span>
    </div>

    <div class="form-group">
      <label for="ms-run-parallel">Parallel</label>
      <input id="ms-run-parallel" type="number" class="input" min="1" required bind:value={form.parallel} />
    </div>

    {#if error}
      <div class="banner banner-error ms-run-error"><span>{error}</span></div>
    {/if}

    <div class="modal-actions">
      <button type="button" class="btn btn-secondary" onclick={onclose}>Cancel</button>
      <button type="submit" class="btn btn-primary" disabled={actionLoading || !valid}>
        {actionLoading ? 'Starting...' : 'Start Milestone Run'}
      </button>
    </div>
  </form>
</Modal>

<style>
  .ms-run-error span {
    overflow-wrap: anywhere;
  }
</style>
