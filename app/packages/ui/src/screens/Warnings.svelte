<script lang="ts">
  import type { ClientCore, SecurityWarning } from '@runer/client-core';
  import { fromStore } from 'svelte/store';
  import { t } from '../messages.js';
  import Icon from '../components/Icon.svelte';

  /** Detail for every security warning (client-spec §3.3): what happened, and what to do. */
  let { core, back }: { core: ClientCore; back: () => void } = $props();

  const stores = $derived({ warnings: fromStore(core.warnings), lists: fromStore(core.lists) });
  const warnings = $derived(stores.warnings.current);

  function listName(id: string): string {
    const l = stores.lists.current.find((x) => x.id === id);
    if (l === undefined) return t('warning.unknownList');
    return l.inbox ? t('nav.inbox') : l.name || t('list.untitled');
  }

  function describe(w: SecurityWarning): { title: string; body: string } {
    switch (w.kind) {
      case 'log-fork':
        return { title: t('warning.log-fork'), body: t('warning.log-fork.body', { list: listName(w.list) }) };
      case 'log-rollback':
        return { title: t('warning.log-rollback'), body: t('warning.log-rollback.body', { list: listName(w.list) }) };
      case 'withheld-removal':
        return { title: t('warning.withheld-removal'), body: t('warning.withheld-removal.body', { list: listName(w.list) }) };
      case 'bundle-rollback':
        return { title: t('warning.bundle-rollback'), body: t('warning.bundle-rollback.body') };
      case 'invite-already-used':
        return { title: t('warning.invite-already-used'), body: t('warning.invite-already-used.body') };
    }
  }
</script>

<p class="kicker"><button class="back" onclick={back}><Icon name="back" />{t('nav.today')}</button></p>
<h1>{t('warning.title')}</h1>
<p class="lead">{t('warning.lead')}</p>

{#if warnings.length === 0}
  <p class="lead">{t('warning.none')}</p>
{:else}
  <ul>
    {#each warnings as w, i (i)}
      {@const d = describe(w)}
      <li>
        <span class="icon"><Icon name="warning" /></span>
        <div>
          <h2>{d.title}</h2>
          <p>{d.body}</p>
        </div>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .kicker {
    margin: 0 0 12px;
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 32px;
    margin-left: -8px;
    padding: 0 10px 0 4px;
    border: 0;
    border-radius: 8px;
    background: none;
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
  }
  .back:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .back :global(svg) {
    width: 16px;
    height: 16px;
  }
  h1 {
    margin: 0 0 8px;
    font-size: 32px;
    font-weight: 600;
    letter-spacing: -0.03em;
    line-height: 1.15;
  }
  .lead {
    margin: 0 0 24px;
    color: var(--text-muted);
    font-size: 15px;
  }
  ul {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: flex;
    gap: 12px;
    padding: 16px;
    border: 1px solid color-mix(in srgb, var(--danger) 35%, transparent);
    border-radius: 12px;
    background: color-mix(in srgb, var(--danger) 8%, transparent);
  }
  .icon {
    flex: none;
    color: var(--danger);
  }
  .icon :global(svg) {
    width: 18px;
    height: 18px;
  }
  h2 {
    margin: 0 0 4px;
    font-size: 14px;
    font-weight: 600;
  }
  p {
    margin: 0;
    color: var(--text-soft);
    font-size: 13px;
    line-height: 1.55;
  }
</style>
