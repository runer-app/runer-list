<script lang="ts">
  import { tickNote } from '../tick.svelte.js';
  import Icon from './Icon.svelte';

  /** Where a ticked repeating task went ("Next: Monday"), for a few seconds. */
  let shown = $state('');
  $effect(() => {
    if (tickNote.n === 0) return;
    shown = tickNote.text;
    const timer = setTimeout(() => (shown = ''), 3500);
    return () => clearTimeout(timer);
  });
</script>

{#if shown !== ''}
  <p class="note" role="status"><Icon name="repeat" />{shown}</p>
{/if}

<style>
  .note {
    position: fixed;
    left: 50%;
    bottom: calc(24px + env(safe-area-inset-bottom));
    z-index: 30;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    padding: 0 14px;
    height: 38px;
    border-radius: 10px;
    background: var(--text);
    color: var(--bg);
    font-size: 13px;
    font-weight: 500;
    white-space: nowrap;
    transform: translateX(-50%);
    pointer-events: none;
  }
  .note :global(svg) {
    width: 14px;
    height: 14px;
  }
  /* Phones: above the tab bar. */
  @media (max-width: 700px) {
    .note {
      bottom: calc(76px + env(safe-area-inset-bottom));
    }
  }
</style>
