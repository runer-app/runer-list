<script lang="ts">
  import { t } from '../messages.js';

  /** A drag handle (six dots) for a row's gutter: drag it, or ↑/↓ with it focused. Shown on row hover or focus by the parent. */
  let { down, key }: { down: (e: PointerEvent) => void; key: (e: KeyboardEvent) => void } = $props();
</script>

<button class="grip" aria-label={t('editor.move')} title={t('editor.moveHint')} onpointerdown={down} onkeydown={key}>
  <svg viewBox="0 0 10 16" width="10" height="16" aria-hidden="true">
    {#each [3, 8, 13] as y (y)}<circle cx="2.5" cy={y} r="1.3" fill="currentColor" /><circle cx="7.5" cy={y} r="1.3" fill="currentColor" />{/each}
  </svg>
</button>

<style>
  .grip {
    display: grid;
    place-items: center;
    width: 20px;
    height: 30px;
    padding: 0;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text-faint);
    cursor: grab;
    touch-action: none;
  }
  .grip:hover {
    background: var(--surface-hover);
  }
</style>
