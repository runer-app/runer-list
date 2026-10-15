<script lang="ts">
  import type { Snippet } from 'svelte';
  import { t } from '../messages.js';
  import Logo from './Logo.svelte';

  /**
   * A signed-out screen: one centred column. A top bar with the small logo
   * (and `step` of `steps` while creating an account), unless the screen
   * shows its own large logo (`header={false}`); `footer` sits at the bottom.
   */
  let {
    children,
    header = true,
    step,
    steps = 2,
    footer,
  }: { children: Snippet; header?: boolean; step?: number; steps?: number; footer?: Snippet } = $props();
</script>

<div class="screen">
  {#if header}
    <header>
      <Logo size={17} />
      {#if step !== undefined}
        <span class="step">
          {t('create.step', { n: step, of: steps })}
          <span class="dashes" aria-hidden="true">
            {#each { length: steps } as _, i (i)}<span class:done={i < step}></span>{/each}
          </span>
        </span>
      {/if}
    </header>
  {/if}
  <main class:pad={header}>
    <div class="column">
      {@render children()}
    </div>
  </main>
  {#if footer}<footer>{@render footer()}</footer>{/if}
</div>

<style>
  .screen {
    min-height: 100vh;
    display: flex;
    flex-direction: column;
    padding-top: var(--titlebar);
    font-size: 14px;
  }
  header {
    height: 64px;
    flex: none;
    padding: 0 32px;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .step {
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--text-muted);
    font-size: 13px;
  }
  .dashes {
    display: flex;
    gap: 4px;
  }
  .dashes span {
    width: 18px;
    height: 4px;
    border-radius: 2px;
    background: var(--border);
  }
  .dashes span.done {
    background: var(--accent);
  }
  main {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 32px 16px;
  }
  /* Optically centred: the top bar's height again below. */
  main.pad {
    padding-bottom: 96px;
  }
  .column {
    width: min(100%, 520px);
    display: flex;
    flex-direction: column;
  }
  .column :global(h1) {
    margin: 0;
    font-size: 32px;
    font-weight: 600;
    letter-spacing: -0.03em;
    line-height: 1.15;
  }
  .column :global(h1 + p) {
    margin: 12px 0 0;
    color: var(--text-muted);
    font-size: 15px;
    line-height: 1.55;
  }
  /* The welcome screen's, under the large logo. */
  .column :global(h1.big) {
    margin-top: 40px;
    font-size: 34px;
  }
  .column :global(h1.big + p) {
    margin-top: 14px;
    font-size: 16px;
  }
  .column :global(.problem) {
    margin: 14px 0 0;
    color: var(--danger);
    font-size: 13px;
  }
  /* The row under a form: the main button, then a hint or a link. */
  .column :global(.actions) {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 16px;
    margin-top: 28px;
  }
  .column :global(.hint) {
    color: var(--text-muted);
    font-size: 13px;
  }
  .column :global(.link) {
    padding: 0;
    border: 0;
    background: none;
    color: var(--accent-label);
    font-size: 14px;
    cursor: pointer;
  }
  .column :global(.link:hover) {
    text-decoration: underline;
  }
  footer {
    height: 56px;
    flex: none;
    padding: 0 32px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    color: var(--text-faint);
    font-size: 13px;
  }
  @media (max-width: 700px) {
    header,
    footer {
      padding: 0 16px;
    }
  }
</style>
