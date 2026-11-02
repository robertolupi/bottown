<script lang="ts">
  // An issue's timeline (§4.2), in a dialog.
  import type { IssueTimeline } from './types';
  import Modal from './Modal.svelte';
  import TimelineEventItem from './TimelineEventItem.svelte';

  interface Props {
    timeline: IssueTimeline | null;
    loading: boolean;
    onclose: () => void;
  }

  let { timeline, loading, onclose }: Props = $props();
</script>

<Modal wide>
  <div class="modal-header">
    <h3>Timeline for Issue #{timeline?.issue}: {timeline?.title}</h3>
    <button class="close-btn" onclick={onclose}>✕</button>
  </div>

  {#if loading}
    <div class="loading-state">Loading timeline...</div>
  {:else if timeline}
    <div class="timeline-list">
      {#if timeline.events.length === 0}
        <div class="muted">No events recorded for this issue.</div>
      {:else}
        {#each timeline.events as ev}
          <TimelineEventItem {ev} linked={false} />
        {/each}
      {/if}
    </div>
  {/if}

  <div class="modal-actions">
    <button class="btn btn-secondary" onclick={onclose}>
      Close
    </button>
  </div>
</Modal>

<style>
  .timeline-list {
    margin: 20px 0;
    max-height: 400px;
    overflow-y: auto;
    position: relative;
    padding-left: 20px;
  }
</style>
