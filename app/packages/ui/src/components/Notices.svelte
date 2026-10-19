<script lang="ts">
  import { toBase64url, type ClientCore, type Notice } from '@runer/client-core';
  import { fromStore } from 'svelte/store';
  import { t } from '../messages.js';
  import { allNames, initialOf, nameOf } from '../people.js';
  import Avatar from './Avatar.svelte';
  import Icon from './Icon.svelte';

  /** Membership news in our lists ("Anna joined Weekend") and lists we lost access to, until dismissed. Rotations are not news to the user. */
  let { core }: { core: ClientCore } = $props();

  const stores = $derived({ notices: fromStore(core.notices), lists: fromStore(core.lists) });
  const names = $derived(allNames(stores.lists.current));
  let dismissed = $state(0);
  const shown = $derived(
    stores.notices.current
      .slice(dismissed)
      .filter((e): e is Exclude<Notice, { kind: 'rotated' }> => e.kind !== 'rotated')
      .slice(-3),
  );

  function listName(id: string): string {
    const l = stores.lists.current.find((x) => x.id === id);
    return l === undefined ? t('warning.unknownList') : l.name || t('list.untitled');
  }

  /** The list's own names first: someone may be called differently there. */
  function who(e: { list: string; member: Uint8Array }): { id: string; name: string; initial: string } {
    const id = toBase64url(e.member);
    const l = stores.lists.current.find((x) => x.id === e.list);
    const own = { ...names, ...l?.names };
    const members = l?.members ?? [];
    return { id, name: nameOf(id, members, own), initial: initialOf(id, '', members, own) };
  }
</script>

{#if shown.length > 0}
  <div class="notices" role="status">
    <ul>
      {#each shown as e, i (i)}
        {#if e.kind === 'lost-access'}
          <li><span class="gone"><Icon name="lists" /></span>{t('notice.lost-access', { list: e.name || t('list.untitled') })}</li>
        {:else}
          {@const p = who(e)}
          <li><Avatar id={p.id} initial={p.initial} size={22} />{t(`notice.${e.kind}`, { name: p.name, list: listName(e.list) })}</li>
        {/if}
      {/each}
    </ul>
    <button aria-label={t('notice.dismiss')} onclick={() => (dismissed = stores.notices.current.length)}><Icon name="close" /></button>
  </div>
{/if}

<style>
  .notices {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    margin: 0 0 20px;
    padding: 6px 6px 6px 14px;
    border: 1px solid var(--divider);
    border-radius: 12px;
    background: var(--bg-sidebar);
    font-size: 13px;
  }
  ul {
    display: flex;
    flex-direction: column;
    gap: 6px;
    flex: 1;
    margin: 0;
    padding: 5px 0;
    list-style: none;
  }
  li {
    display: flex;
    align-items: center;
    gap: 10px;
    /* A member's name is whatever their app wrote: a long one without spaces wraps. */
    overflow-wrap: anywhere;
  }
  /* Where an avatar would be: the list is no one's news. */
  .gone {
    display: grid;
    flex: none;
    place-items: center;
    width: 22px;
    height: 22px;
    color: var(--text-muted);
  }
  .gone :global(svg) {
    width: 14px;
    height: 14px;
  }
  button {
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    padding: 0;
    border: 0;
    border-radius: 8px;
    background: transparent;
    color: var(--text-muted);
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
</style>
