<script lang="ts">
  import { t } from '../messages.js';
  import type { AppUpdate } from '../shell.js';

  /** A downloaded update: it runs once the app restarts, now or at the next start. */
  let { update }: { update: AppUpdate } = $props();

  let busy = $state(false);
  let failed = $state(false);

  async function restart() {
    busy = true;
    try {
      await update.restart();
    } catch {
      failed = true;
    }
    busy = false;
  }
</script>

<div class="banner" role="status">
  {#if failed}
    <span>{t('update.failed')}</span>
  {:else}
    <span>{t('update.ready')}</span>
    <button onclick={restart} disabled={busy}>{t('update.restart')}</button>
  {/if}
</div>

<style>
  .banner {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0 0 20px;
    padding: 6px 6px 6px 14px;
    min-height: 32px;
    border: 1px solid var(--divider);
    border-radius: 12px;
    background: var(--bg-sidebar);
    font-size: 13px;
  }
  span {
    flex: 1;
  }
  button {
    height: 32px;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
  }
  button:hover {
    background: var(--surface-hover);
  }
</style>
