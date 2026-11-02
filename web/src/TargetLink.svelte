<script lang="ts">
  // A run's or inbox item's target (#issue, !PR, run ID) as a monospace
  // link into Forgejo. stopPropagation keeps a click from also reaching a
  // clickable row around it.
  interface Props {
    href: string | undefined;
    label: string | undefined;
    stopPropagation?: boolean;
  }

  let { href, label, stopPropagation = false }: Props = $props();
</script>

<a
  class="pr-badge"
  {href}
  target="_blank"
  rel="noreferrer"
  onclick={stopPropagation ? (e) => e.stopPropagation() : undefined}
>{label}</a>

<style>
  .pr-badge {
    background: rgba(99, 102, 241, 0.15);
    color: #818cf8;
    text-decoration: none;
    font-family: monospace;
    font-weight: bold;
    padding: 2px 6px;
    border-radius: 4px;
  }
  .pr-badge:hover { text-decoration: underline; }
</style>
