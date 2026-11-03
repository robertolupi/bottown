<script lang="ts">
  // Start an issue flow (§5): the form's values live in App, so they stay
  // between openings and the Milestones view can preset the issue.
  import type { ActorDetail } from './types';
  import type { FlowForm } from './actions';
  import { actorModelPlaceholder } from './state';
  import Modal from './Modal.svelte';

  interface Props {
    actors: ActorDetail[] | undefined;
    form: FlowForm;
    sameFamily: boolean;
    actionLoading: boolean;
    onsubmit: () => void;
    onclose: () => void;
  }

  let { actors, form = $bindable(), sameFamily, actionLoading, onsubmit, onclose }: Props = $props();
</script>

<Modal>
  <div class="modal-header">
    <h3>Start an Issue Flow</h3>
    <button class="close-btn" onclick={onclose}>✕</button>
  </div>
  <p class="muted text-sm">
    Supervises an issue through implement and cross-family verify until ready to merge.
  </p>

  <form onsubmit={(e) => { e.preventDefault(); onsubmit(); }} class="form-grid">
    <div class="form-group">
      <label for="flow-issue">Issue Number</label>
      <input
        id="flow-issue"
        type="number"
        class="input"
        placeholder="e.g. 51"
        required
        bind:value={form.issue}
      />
    </div>

    <div class="form-row">
      <div class="form-group">
        <label for="flow-imp">Implementer</label>
        <!-- A model is the harness's: changing the actor clears it. -->
        <select id="flow-imp" class="input" bind:value={form.implement} onchange={() => (form.implementModel = '')}>
          {#if actors}
            {#each actors as act}
              <option value={act.name}>{act.name} ({act.family})</option>
            {/each}
          {/if}
        </select>
      </div>

      <div class="form-group">
        <label for="flow-ver">Verifier (Cross-Family)</label>
        <select id="flow-ver" class="input" bind:value={form.verify} onchange={() => (form.verifyModel = '')}>
          {#if actors}
            {#each actors as act}
              <option value={act.name}>{act.name} ({act.family})</option>
            {/each}
          {/if}
        </select>
      </div>
    </div>

    <div class="form-row">
      <div class="form-group">
        <label for="flow-imp-model">Implementer model (optional)</label>
        <input
          id="flow-imp-model"
          type="text"
          class="input"
          placeholder={actorModelPlaceholder(actors, form.implement)}
          bind:value={form.implementModel}
        />
      </div>

      <div class="form-group">
        <label for="flow-ver-model">Verifier model (optional)</label>
        <input
          id="flow-ver-model"
          type="text"
          class="input"
          placeholder={actorModelPlaceholder(actors, form.verify)}
          bind:value={form.verifyModel}
        />
      </div>
    </div>

    {#if sameFamily}
      <div class="banner banner-error">
        <span>Verifier must be from another model family than implementer.</span>
      </div>
    {/if}

    <div class="form-row">
      <div class="form-group">
        <label for="flow-rounds">Rounds</label>
        <input id="flow-rounds" type="number" class="input" min="1" max="10" bind:value={form.rounds} />
      </div>

      <div class="form-group">
        <label for="flow-timeout">Timeout</label>
        <input id="flow-timeout" type="text" class="input" placeholder="60m" bind:value={form.timeout} />
      </div>
    </div>

    <div class="modal-actions">
      <button type="button" class="btn btn-secondary" onclick={onclose}>
        Cancel
      </button>
      <button type="submit" class="btn btn-primary" disabled={actionLoading || sameFamily || !form.issue}>
        {actionLoading ? 'Starting...' : 'Start Flow'}
      </button>
    </div>
  </form>
</Modal>
