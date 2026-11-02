<script lang="ts">
  // A run's or thread's execution status: succeeded, failed, running or
  // anything else (revoked), each in its own tone.
  import { statusTone } from './format';

  let { status }: { status: string } = $props();
</script>

<span class="badge badge-{statusTone(status)}">
  {status}
</span>
