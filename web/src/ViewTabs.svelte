<script lang="ts">
  // The tabs between the UI's views, each a link to its route, so a click
  // adds a history entry and the App follows the fragment.
  import { VIEWS, routeHash, type View } from './routes';

  let { view }: { view: View } = $props();
</script>

<div class="tabs" role="tablist">
  {#each VIEWS as tab (tab.view)}
    <a
      class="tab"
      class:tab-active={view === tab.view}
      role="tab"
      aria-selected={view === tab.view}
      href={routeHash(tab.view)}
      data-view={tab.view}
    >{tab.label}</a>
  {/each}
</div>
<style>
  .tabs {
    display: flex;
    /* Six tabs do not fit a phone's width: they wrap, not widen the page. */
    flex-wrap: wrap;
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
    text-decoration: none;
  }

  .tab:hover {
    color: var(--text);
  }

  .tab-active {
    color: var(--primary);
    border-bottom-color: var(--primary);
  }
</style>
