<script lang="ts">
  import { t } from '../messages.js';
  import Button from '../components/Button.svelte';
  import Centered from '../components/Centered.svelte';

  /** `signIn` resolves false for a typo, which the vault catches before any request. */
  let { signIn, back }: { signIn: (words: string[]) => Promise<boolean>; back: () => void } = $props();

  let text = $state('');
  let busy = $state(false);
  let problem = $state('');

  const words = $derived(text.trim().toLowerCase().split(/\s+/).filter((w) => w !== ''));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    problem = '';
    try {
      if (!(await signIn(words))) problem = t('signIn.typo');
    } catch (err) {
      problem = err instanceof Error ? err.message : t('error.network');
    } finally {
      busy = false;
    }
  }
</script>

<Centered>
  <h1>{t('signIn.title')}</h1>
  <p>{t('signIn.lead')}</p>
  <form onsubmit={submit}>
    <label>
      <span class="label">{t('signIn.words')}</span>
      <textarea class="field" bind:value={text} rows="3" autocomplete="off" autocapitalize="none" spellcheck="false"></textarea>
    </label>
    <span class="count" aria-live="polite">{t('signIn.count', { n: words.length })}</span>
    {#if problem !== ''}<p class="problem" role="alert">{problem}</p>{/if}
    <div class="actions">
      <Button large type="submit" disabled={busy || words.length !== 12}>{busy ? t('signIn.working') : t('signIn.submit')}</Button>
      <button type="button" class="link" onclick={back} disabled={busy}>{t('signIn.back')}</button>
    </div>
  </form>
</Centered>

<style>
  label {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 28px;
  }
  .label {
    color: var(--text-muted);
    font-size: 12px;
  }
  textarea {
    padding: 12px;
    font-family: var(--font-mono);
    line-height: 1.6;
    resize: none;
  }
  .count {
    display: block;
    margin-top: 8px;
    color: var(--text-faint);
    font-size: 12px;
  }
</style>
