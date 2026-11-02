<script lang="ts">
  import { t } from '../messages.js';
  import Icon from './Icon.svelte';

  /** The 12 account-key words in a numbered grid, with Copy. */
  let { words }: { words: string[] } = $props();

  let copied = $state<string | null>(null);
  const text = $derived(words.join(' '));

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      copied = text;
    } catch {
      copied = null;
    }
  }
</script>

<div class="card">
  <ol aria-label={t('create.wordsTitle')}>
    {#each words as word, i (i)}
      <li><span class="n">{i + 1}</span> <span class="word">{word}</span></li>
    {/each}
  </ol>
  <div class="bar">
    <button onclick={() => void copy()}><Icon name={copied === text ? 'check' : 'copy'} />{copied === text ? t('create.copied') : t('create.copy')}</button>
    <span class="note">{t('create.clipboard')}</span>
  </div>
</div>

<style>
  .card {
    border: 1px solid var(--divider);
    border-radius: 14px;
    background: var(--bg-sidebar);
    overflow: hidden;
  }
  ol {
    margin: 0;
    padding: 20px 22px;
    list-style: none;
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 14px 16px;
    font-family: var(--font-mono);
  }
  li {
    display: flex;
    align-items: baseline;
    gap: 10px;
  }
  .n {
    width: 18px;
    flex: none;
    text-align: right;
    color: var(--text-faint);
    font-size: 12px;
  }
  .word {
    font-size: 15px;
  }
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    padding: 8px 12px;
    border-top: 1px solid var(--divider);
  }
  button {
    height: 32px;
    padding: 0 10px;
    display: flex;
    align-items: center;
    gap: 8px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
  }
  button:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  button :global(svg) {
    width: 14px;
    height: 14px;
  }
  .note {
    margin-left: auto;
    padding-right: 4px;
    color: var(--text-faint);
    font-size: 12px;
  }
  @media (max-width: 480px) {
    ol {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
</style>
