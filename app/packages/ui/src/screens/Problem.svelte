<script lang="ts">
  import { VaultError } from '@runer/client-core';
  import { t } from '../messages.js';
  import Button from '../components/Button.svelte';
  import Centered from '../components/Centered.svelte';

  let { error, retry }: { error: unknown; retry: () => void } = $props();

  const keychain = $derived(error instanceof VaultError && error.code === 'Storage' && /keychain/i.test(error.message));
  const detail = $derived(error instanceof Error ? error.message : typeof error === 'object' && error !== null && 'message' in error ? String(error.message) : String(error));
</script>

<Centered>
  <h1>{t('error.title')}</h1>
  <p>{keychain ? t('error.keychain') : detail}</p>
  {#if keychain}<p class="detail">{detail}</p>{/if}
  <div class="actions"><Button large onclick={retry}>{t('error.retry')}</Button></div>
</Centered>

<style>
  .detail {
    margin: 14px 0 0;
    color: var(--text-faint);
    font-family: var(--font-mono);
    font-size: 12px;
    word-break: break-word;
  }
</style>
