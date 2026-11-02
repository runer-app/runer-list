<script lang="ts">
  import { t } from '../messages.js';
  import Button from '../components/Button.svelte';
  import Centered from '../components/Centered.svelte';

  /**
   * The server refused this device's login: the account key changed or the
   * account is gone. Nothing works here until the user signs in again.
   */
  let { signIn }: { signIn: () => Promise<void> } = $props();
</script>

<Centered>
  <h1>{t('signedOut.title')}</h1>
  <p>{t('signedOut.lead')}</p>
  <p>{t('signedOut.detail')}</p>
  <div class="actions"><Button large onclick={() => void signIn()}>{t('signedOut.action')}</Button></div>
</Centered>
