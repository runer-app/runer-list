<script lang="ts">
  import type { Snippet } from 'svelte';

  /** `large` is a screen's main action (welcome, sign-in, the account key); the default sits in a page. */
  let {
    children,
    onclick,
    variant = 'primary',
    type = 'button',
    disabled = false,
    large = false,
  }: {
    children: Snippet;
    onclick?: () => void;
    variant?: 'primary' | 'quiet' | 'danger';
    type?: 'button' | 'submit';
    disabled?: boolean;
    large?: boolean;
  } = $props();
</script>

<button {type} {disabled} class={variant} class:large {onclick}>{@render children()}</button>

<style>
  button {
    height: 36px;
    padding: 0 14px;
    border-radius: 8px;
    border: 1px solid transparent;
    font-size: 14px;
    font-weight: 500;
    white-space: nowrap;
    cursor: pointer;
  }
  .large {
    height: 46px;
    padding: 0 22px;
    border-radius: 10px;
    font-size: 15px;
  }
  .primary {
    background: var(--accent);
    color: var(--accent-text);
    font-weight: 600;
  }
  .quiet {
    background: transparent;
    border-color: var(--border);
  }
  .quiet:hover {
    background: var(--surface-hover);
  }
  .danger {
    background: var(--danger);
    color: var(--accent-text);
  }
  .primary:disabled {
    background: var(--border);
    color: var(--text-muted);
  }
  button:disabled {
    cursor: default;
  }
  .quiet:disabled {
    opacity: 0.6;
  }
</style>
