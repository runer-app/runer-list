<script lang="ts">
  import { PRODUCT_NAME } from '../product.js';

  /** In pixels: 19 in the sidebar, 22 on the welcome screen, 17 in a screen's top bar. */
  let { size = 19 }: { size?: number } = $props();

  /** The lit blocks of the 4×4 mark (img/runer-mark.svg), counted row by row. */
  const LIT = [7, 8, 10, 13];
</script>

<span class="logo" style:font-size="{size}px">
  <svg class="mark" viewBox="8 8 48 48" aria-hidden="true">
    {#each { length: 16 }, i}
      <rect x={8 + (i % 4) * 13} y={8 + Math.floor(i / 4) * 13} width="9" height="9" rx="3" class:lit={LIT.includes(i)} />
    {/each}
  </svg>{PRODUCT_NAME}<span class="dot">.</span>
</span>

<style>
  .logo {
    display: inline-flex;
    align-items: center;
    font-weight: 600;
    letter-spacing: -0.03em;
  }
  .mark {
    width: 1.1em;
    height: 1.1em;
    margin-right: 0.35em;
    fill: currentColor;
    fill-opacity: 0.1;
  }
  .mark .lit {
    fill: #7ccfd6;
    fill-opacity: 1;
  }
  .dot {
    color: var(--accent);
  }
</style>
