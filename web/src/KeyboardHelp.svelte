<script lang="ts">
  // The keys, shown by `?` (design §5): a small panel the one keydown
  // listener closes on Esc, and a backdrop click closes too.
  import { KEY_HELP } from './keys';

  interface Props {
    onclose: () => void;
  }

  let { onclose }: Props = $props();
</script>

<div
  class="keys-backdrop"
  role="presentation"
  onclick={onclose}
  onkeydown={(e) => { if (e.key === 'Escape') onclose(); }}
>
  <div
    class="keys-panel"
    role="dialog"
    aria-modal="true"
    aria-label="Keyboard shortcuts"
    tabindex="-1"
    onclick={(e) => e.stopPropagation()}
    onkeydown={(e) => e.stopPropagation()}
  >
    <div class="keys-head">
      <h3>Keys</h3>
      <button class="close-btn" aria-label="Close" onclick={onclose}>✕</button>
    </div>
    <table>
      <tbody>
        {#each KEY_HELP as row (row.keys)}
          <tr>
            <th><kbd>{row.keys}</kbd></th>
            <td>{row.does}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>

<style>
  .keys-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .keys-panel {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 20px 24px;
    max-width: 420px;
    width: 90%;
    box-shadow: 0 10px 25px rgba(0, 0, 0, 0.2);
  }

  .keys-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    border-bottom: 1px solid var(--border);
    padding-bottom: 10px;
    margin-bottom: 12px;
  }

  .keys-head h3 {
    margin: 0;
    font-size: 15px;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }

  th {
    text-align: left;
    width: 40%;
    padding: 4px 0;
    font-weight: 500;
  }

  td {
    padding: 4px 0;
    color: var(--text-muted);
  }

  kbd {
    display: inline-block;
    font-family: monospace;
    font-size: 12px;
    background: rgba(125, 125, 125, 0.14);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 1px 6px;
  }

  .close-btn {
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    font-size: 14px;
    line-height: 1;
  }

  .close-btn:hover {
    color: var(--text);
  }
</style>
