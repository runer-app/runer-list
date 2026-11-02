<script lang="ts">
  import { t } from '../messages.js';
  import Icon from './Icon.svelte';

  /** Security warnings can't be dismissed silently: this stays until they're gone. */
  let { count, open }: { count: number; open: () => void } = $props();
</script>

<div class="banner" role="alert">
  <Icon name="warning" />
  <span>{count === 1 ? t('warning.banner.one') : t('warning.banner', { n: count })}</span>
  <button onclick={open}>{t('warning.details')}</button>
</div>

<style>
  .banner {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0 0 20px;
    padding: 6px 6px 6px 14px;
    border: 1px solid color-mix(in srgb, var(--danger) 35%, transparent);
    border-radius: 12px;
    background: color-mix(in srgb, var(--danger) 8%, transparent);
    color: var(--danger);
    font-size: 13px;
  }
  .banner :global(svg) {
    flex: none;
    width: 16px;
    height: 16px;
  }
  span {
    flex: 1;
    color: var(--text);
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
