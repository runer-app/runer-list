<script lang="ts">
  import type { Snippet } from 'svelte';
  import { t } from '../messages.js';
  import Icon from './Icon.svelte';

  /**
   * A modal over the app: a title, an optional lead, the content. Escape or
   * a press outside closes it, unless a dialog opened from inside it is up.
   * `alert` is for a question that needs an answer (a confirm).
   */
  let {
    title,
    lead,
    close,
    width = 480,
    alert = false,
    closeButton = true,
    top,
    children,
  }: {
    title: string;
    lead?: string | undefined;
    close: () => void;
    width?: number;
    alert?: boolean;
    closeButton?: boolean;
    /** Above the title (an avatar). */
    top?: Snippet;
    children: Snippet;
  } = $props();

  let panel: HTMLElement;
  /** Another dialog opened from this one answers Escape first. */
  const nested = () => panel.querySelector('[role="dialog"], [role="alertdialog"]') !== null;

  function key(e: KeyboardEvent) {
    if (e.key === 'Escape' && !nested()) {
      e.stopPropagation();
      close();
    }
  }
</script>

<svelte:window onkeydown={key} />

<div class="backdrop" role="presentation" onpointerdown={(e) => e.target === e.currentTarget && close()}>
  <section bind:this={panel} role={alert ? 'alertdialog' : 'dialog'} aria-modal="true" aria-label={title} style:width="min({width}px, 100%)">
    <header>
      <div class="titles">
        {#if top}{@render top()}{/if}
        <h2>{title}</h2>
        {#if lead}<p class="lead">{lead}</p>{/if}
      </div>
      {#if closeButton}
        <button class="close" aria-label={t('item.close')} onclick={close}><Icon name="close" /></button>
      {/if}
    </header>
    {@render children()}
  </section>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 100;
    display: grid;
    place-items: center;
    padding: 16px;
    background: rgb(10 8 14 / 0.62);
    animation: fade 120ms ease-out;
  }
  section {
    max-height: calc(100vh - 32px);
    overflow-y: auto;
    padding: 20px 24px 22px;
    border: 1px solid var(--border);
    border-radius: 16px;
    background: var(--bg-popover);
    box-shadow: 0 24px 60px rgb(0 0 0 / 0.5);
    color: var(--text);
    font-size: 14px;
    text-align: left;
    white-space: normal;
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
  }
  .titles {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
  }
  h2 {
    margin: 0;
    font-size: 18px;
    font-weight: 600;
    letter-spacing: -0.02em;
  }
  .lead {
    margin: 0;
    color: var(--text-muted);
    font-size: 13px;
    line-height: 1.5;
  }
  .close {
    display: grid;
    flex: none;
    place-items: center;
    width: 32px;
    height: 32px;
    margin: -4px -8px 0 0;
    padding: 0;
    border: 0;
    border-radius: 8px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }
  .close:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .close :global(svg) {
    width: 16px;
    height: 16px;
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .backdrop {
      animation: none;
    }
  }
</style>
