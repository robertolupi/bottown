<script lang="ts">
  // The page header: the repository and instance, when origin was last
  // fetched, Refresh and a link to the repository in Forgejo.
  import type { UIStatusResponse } from './types';
  import { formatTime } from './format';

  interface Props {
    status: UIStatusResponse | null;
    refreshing: boolean;
    onRefresh: () => void;
  }

  let { status, refreshing, onRefresh }: Props = $props();
</script>

<header class="header">
  <div class="header-left">
    <h1 class="logo">bottown</h1>
    {#if status}
      <span class="badge repo-badge">{status.repo}</span>
      <span class="badge instance-badge">{status.instance}</span>
    {/if}
  </div>
  <div class="header-right">
    {#if status?.upstream?.fetched_at}
      <span class="muted text-sm">origin as of {formatTime(status.upstream.fetched_at)}</span>
    {/if}
    <button class="btn btn-secondary" onclick={onRefresh} disabled={refreshing}>
      {#if refreshing}Refreshing...{:else}Refresh{/if}
    </button>
    {#if status?.links?.repo}
      <a class="btn btn-primary" href={status.links.repo} target="_blank" rel="noreferrer">
        Forgejo ↗
      </a>
    {/if}
  </div>
</header>

<style>
  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 24px;
    flex-wrap: wrap;
    gap: 16px;
  }

  .header-left, .header-right {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .logo {
    margin: 0;
    font-size: 24px;
    font-weight: 700;
    letter-spacing: -0.5px;
  }
</style>
