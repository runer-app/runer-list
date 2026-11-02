<script lang="ts">
  /** A list's progress: the share of its tasks that are done, as an arc on a faint ring. */
  let { done, open }: { done: number; open: number } = $props();
  const C = 2 * Math.PI * 6;
  const arc = $derived(done + open === 0 ? 0 : (done / (done + open)) * C);
</script>

<svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
  <circle cx="8" cy="8" r="6" stroke="var(--ring-track)" stroke-width="2" />
  {#if arc > 0}<circle cx="8" cy="8" r="6" stroke="var(--accent)" stroke-width="2" stroke-dasharray="{arc} {C}" transform="rotate(-90 8 8)" />{/if}
</svg>
