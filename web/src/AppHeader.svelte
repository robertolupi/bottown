<script lang="ts">
  // The page header (monitor-ui design §3): the repository and instance,
  // a health chip and an upstream chip linking to the Repository view,
  // Refresh beside the upstream chip (it also fetches origin), how fresh
  // the live poll is, and a link to the repository in Forgejo.
  import type { UIStatusResponse } from './types';
  import { formatTime } from './format';
  import { freshness, healthProblems, upstreamText } from './now';

  interface Props {
    status: UIStatusResponse | null;
    refreshing: boolean;
    onRefresh: () => void;
    // When the last live poll (or status load) succeeded, ms; 0 before one.
    lastLiveOk: number;
    // The last live poll's error, while polls fail.
    liveError?: string | null;
    now: number;
  }

  let { status, refreshing, onRefresh, lastLiveOk, liveError, now }: Props = $props();

  let problems = $derived(healthProblems(status?.health));
  let upstream = $derived(status ? upstreamText(status) : null);
  let fresh = $derived(freshness(lastLiveOk, now));
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
    {#if status}
      <a
        class="badge chip health-chip badge-{problems.length ? 'warning' : 'success'}"
        href="#/repo"
        title={problems.length ? problems.join('; ') : 'Every health check passes'}
      >{problems.length ? `${problems.length} problem${problems.length === 1 ? '' : 's'}` : '✓ healthy'}</a>
      {#if upstream}
        <a
          class="badge chip upstream-chip badge-{upstream.tone}"
          href="#/repo"
          title={status.upstream?.fetched_at ? `origin as of ${formatTime(status.upstream.fetched_at)}` : undefined}
        >{upstream.text}</a>
      {/if}
    {/if}
    <button class="btn btn-sm btn-secondary" onclick={onRefresh} disabled={refreshing}>
      {#if refreshing}Refreshing...{:else}Refresh{/if}
    </button>
    <span
      class="badge chip freshness-chip badge-{fresh.stale ? 'warning' : 'neutral'}"
      class:stale={fresh.stale}
      title={liveError ? `Live poll failed: ${liveError}` : 'Since the last successful live poll'}
    >{fresh.text}</span>
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
    flex-wrap: wrap;
  }

  .chip {
    text-decoration: none;
    white-space: nowrap;
  }

  .logo {
    margin: 0;
    font-size: 24px;
    font-weight: 700;
    letter-spacing: -0.5px;
  }
</style>
