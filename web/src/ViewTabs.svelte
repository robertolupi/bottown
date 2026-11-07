<script lang="ts">
  // The tabs between the UI's views: the overview, Milestones, the
  // Timeline and Stats.
  type View = 'overview' | 'milestones' | 'timeline' | 'stats';

  let { view, onSelect }: { view: View; onSelect: (view: View) => void } = $props();

  const tabs: { view: View; label: string }[] = [
    { view: 'overview', label: 'Inbox · Runs · Health' },
    { view: 'milestones', label: 'Milestones' },
    { view: 'timeline', label: 'Timeline' },
    { view: 'stats', label: 'Stats' },
  ];
</script>

<div class="tabs" role="tablist">
  {#each tabs as tab (tab.view)}
    <button
      class="tab"
      class:tab-active={view === tab.view}
      role="tab"
      aria-selected={view === tab.view}
      onclick={() => onSelect(tab.view)}
    >{tab.label}</button>
  {/each}
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
