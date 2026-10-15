<script lang="ts">
  import Avatar from './Avatar.svelte';
  import Button from './Button.svelte';
  import Dialog from './Dialog.svelte';
  import { t } from '../messages.js';

  /**
   * A destructive action behind a second step: the button opens a small
   * dialog with the consequence, a confirm and a cancel. Escape or a press
   * outside cancels. Errors from `run` are shown, not thrown.
   * `look` is the first button's: a bordered button, plain text in a row, a
   * link in a sentence, or outlined in the danger colour. `question` titles
   * the dialog when it differs from the button ("Remove Anna from this list?").
   */
  let {
    label,
    question,
    warning,
    detail,
    person,
    confirmLabel,
    run,
    look = 'button',
  }: {
    label: string;
    question?: string;
    warning: string;
    /** A quieter second line. */
    detail?: string;
    /** Whose avatar tops the dialog. */
    person?: { id: string; initial: string };
    confirmLabel: string;
    run: () => Promise<void> | void;
    look?: 'button' | 'text' | 'link' | 'outline';
  } = $props();

  let asking = $state(false);
  let busy = $state(false);
  let error = $state('');
  let opener: HTMLElement | null = null;

  function ask(e: MouseEvent) {
    opener = e.currentTarget as HTMLElement;
    error = '';
    asking = true;
  }

  function close() {
    if (busy) return;
    asking = false;
    opener?.focus();
  }

  async function go() {
    busy = true;
    error = '';
    try {
      await run();
      busy = false;
      close();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  const focusFirst = (node: HTMLElement) => node.focus();
</script>

{#if look === 'button'}
  <Button variant="quiet" onclick={() => (asking = true)}>{label}</Button>
{:else}
  <button class="trigger {look}" onclick={ask}>{label}</button>
{/if}

{#if asking}
  <Dialog title={question ?? label} {close} width={420} alert closeButton={false}>
    {#snippet top()}
      {#if person}<span class="who"><Avatar id={person.id} initial={person.initial} size={40} /></span>{/if}
    {/snippet}
    <p class="warning">{warning}</p>
    {#if detail}<p class="detail">{detail}</p>{/if}
    {#if error !== ''}<p class="error" role="alert">{error}</p>{/if}
    <div class="buttons">
      <button class="cancel" onclick={close} disabled={busy} use:focusFirst>{t('common.cancel')}</button>
      <button class="confirm" onclick={go} disabled={busy}>{confirmLabel}</button>
    </div>
  </Dialog>
{/if}

<style>
  .trigger {
    height: 32px;
    padding: 0 10px;
    border: 0;
    border-radius: 8px;
    background: transparent;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 500;
    white-space: nowrap;
    cursor: pointer;
  }
  .trigger:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .trigger.link {
    height: auto;
    padding: 0;
    color: var(--accent-label);
    font-weight: 400;
  }
  .trigger.link:hover {
    background: none;
    text-decoration: underline;
  }
  .trigger.outline {
    padding: 0 12px;
    border: 1px solid color-mix(in srgb, var(--danger) 45%, transparent);
    color: var(--danger);
  }
  .trigger.outline:hover {
    background: color-mix(in srgb, var(--danger) 12%, transparent);
    color: var(--danger);
  }

  .who {
    display: block;
    margin-bottom: 10px;
  }
  .warning,
  .detail,
  .error {
    margin: 8px 0 0;
    font-size: 14px;
    line-height: 1.55;
  }
  .detail {
    color: var(--text-muted);
    font-size: 13px;
  }
  .error {
    color: var(--danger);
    font-size: 13px;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 20px;
  }
  .buttons button {
    height: 38px;
    padding: 0 16px;
    border-radius: 9px;
    font-size: 14px;
    font-weight: 500;
    cursor: pointer;
  }
  .cancel {
    border: 1px solid var(--border);
    background: transparent;
  }
  .cancel:hover {
    background: var(--surface-hover);
  }
  .confirm {
    border: 0;
    background: var(--danger-strong);
    color: #fff;
    font-weight: 600;
  }
  .confirm:hover {
    filter: brightness(1.08);
  }
  .buttons button:disabled {
    opacity: 0.6;
    cursor: default;
  }
</style>
