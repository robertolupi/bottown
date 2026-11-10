<script lang="ts">
  let { command, label = command }: { command: string; label?: string } = $props();
  let text: HTMLElement;
  let feedback = $state('');

  async function copy(event: MouseEvent) {
    event.stopPropagation();
    try {
      if (!navigator.clipboard) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(command);
      feedback = 'Copied';
    } catch {
      text.textContent = command;
      const range = document.createRange();
      range.selectNodeContents(text);
      const selection = window.getSelection();
      selection?.removeAllRanges();
      selection?.addRange(range);
      feedback = 'Selected — copy text';
    }
  }
</script>

<span class="command-copy" title={command}>
  <code bind:this={text}>{label}</code>
  <button type="button" aria-label={`Copy ${command}`} onclick={copy}>Copy</button>
  <span class="feedback" role="status">{feedback}</span>
</span>

<style>
  .command-copy { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 6px; max-width: 100%; min-width: 0; padding: 3px 6px; border: 1px solid var(--border); border-radius: 4px; background: rgba(125,125,125,.08); font-size: 12px; }
  code { overflow-wrap: anywhere; min-width: 0; }
  button { cursor: pointer; color: var(--text); background: var(--card-bg); border: 1px solid var(--border); border-radius: 3px; }
  .feedback { color: var(--text-muted); font-size: 11px; }
</style>
