<script lang="ts">
  // The tabs between the UI's views: the overview and Milestones.
  type View = 'overview' | 'milestones';

  let { view, onSelect }: { view: View; onSelect: (view: View) => void } = $props();
</script>

<div class="tabs" role="tablist">
  <button
    class="tab"
    class:tab-active={view === 'overview'}
    role="tab"
    aria-selected={view === 'overview'}
    onclick={() => onSelect('overview')}
  >Inbox · Runs · Health</button>
  <button
    class="tab"
    class:tab-active={view === 'milestones'}
    role="tab"
    aria-selected={view === 'milestones'}
    onclick={() => onSelect('milestones')}
  >Milestones</button>
</div>

<style>
  .tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 20px;
    border-bottom: 1px solid var(--border);
  }

  .tab {
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    padding: 8px 14px;
    font-size: 14px;
    font-weight: 500;
    color: var(--text-muted);
    cursor: pointer;
    margin-bottom: -1px;
  }

  .tab:hover {
    color: var(--text);
  }

  .tab-active {
    color: var(--primary);
    border-bottom-color: var(--primary);
  }
</style>
