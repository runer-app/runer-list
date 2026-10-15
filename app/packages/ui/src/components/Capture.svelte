<script lang="ts">
  import { readTyped, type Typed } from '../dates.js';
  import { t } from '../messages.js';
  import Icon from './Icon.svelte';

  /**
   * The capture field on Today and Lists (client-spec §7): Enter adds, and the field stays ready for the next.
   * An `add` that returns `false` took nothing, and the text stays.
   * `shortcut` is a key that jumps here from anywhere on the page that isn't a text field.
   * With `dates`, a date typed in the text ("tomorrow 9:30") shows as a chip and leaves the title when the task is
   * added; Escape, or a press on the chip, keeps the words as text for this task.
   */
  let {
    placeholder,
    add,
    shortcut,
    dates = false,
  }: { placeholder: string; add: (title: string, when?: Typed) => boolean | void; shortcut?: string; dates?: boolean } = $props();
  let value = $state('');
  let asText = $state(false);
  const typed = $derived(dates && !asText ? readTyped(value) : null);
  let input: HTMLInputElement;

  function jump(e: KeyboardEvent) {
    if (shortcut === undefined || e.key.toLowerCase() !== shortcut.toLowerCase() || e.metaKey || e.ctrlKey || e.altKey) return;
    const at = e.target as HTMLElement | null;
    if (at?.closest('input, textarea, select, [contenteditable], [role="dialog"], [role="alertdialog"]')) return;
    e.preventDefault();
    input.focus();
  }

  function submit(e: SubmitEvent) {
    e.preventDefault();
    // Only a date, and no title left: nothing to add.
    const title = (typed?.title ?? value).trim();
    if (title === '') return;
    if (add(title, typed ?? undefined) === false) return;
    value = '';
    asText = false;
  }

  function typing() {
    if (value === '') asText = false;
  }

  function keyOnInput(e: KeyboardEvent) {
    if (e.key === 'Escape') keepText(e);
  }

  function keepText(e: Event) {
    if (typed === null) return;
    e.preventDefault();
    asText = true;
    input.focus();
  }
</script>

<svelte:window onkeydown={jump} />

<form onsubmit={submit}>
  <span aria-hidden="true"><Icon name="plus" /></span>
  <input
    bind:this={input}
    bind:value
    {placeholder}
    aria-label={placeholder}
    aria-keyshortcuts={shortcut}
    enterkeyhint="done"
    oninput={typing}
    onkeydown={keyOnInput}
  />
  <span class="typed" aria-live="polite">
    {#if typed !== null}
      <button type="button" class="when" title={t('typed.hint')} aria-label={t('typed.chip', { when: typed.label })} onclick={keepText}><Icon name="calendar" />{typed.label}</button>
    {/if}
  </span>
  {#if shortcut !== undefined && value === ''}<kbd aria-hidden="true">{shortcut.toUpperCase()}</kbd>{/if}
</form>

<style>
  form {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 40px;
  }
  span:first-child {
    display: grid;
    place-items: center;
    width: 18px;
    color: var(--text-muted);
  }
  span:first-child :global(svg) {
    width: 14px;
    height: 14px;
  }
  /* The date understood in the text: the look of a set date (DatePicker). */
  .when {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 30px;
    padding: 0 10px;
    border: 0;
    border-radius: 7px;
    background: var(--accent-soft);
    color: var(--accent-soft-text);
    font-size: 13px;
    white-space: nowrap;
    cursor: pointer;
  }
  .when :global(svg) {
    width: 13px;
    height: 13px;
  }
  input {
    flex: 1;
    min-width: 0;
    min-height: 40px;
    font-size: 15px;
    border: 0;
    background: transparent;
    outline: none;
  }
  input::placeholder {
    color: var(--text-muted);
  }
  kbd {
    display: grid;
    place-items: center;
    min-width: 20px;
    height: 20px;
    padding: 0 5px;
    border: 1px solid var(--border);
    border-radius: 5px;
    color: var(--text-faint);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  form:focus-within kbd {
    display: none;
  }
</style>
