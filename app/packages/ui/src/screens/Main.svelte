<script lang="ts">
  import { parseInviteLink, type ClientCore, type ListId } from '@runer/client-core';
  import { onMount } from 'svelte';
  import type { AppUpdate, DebugLog } from '../shell.js';
  import { fromStore } from 'svelte/store';
  import { clock, deviceHour12 } from '../clock.svelte.js';
  import { t } from '../messages.js';
  import type { View } from '../view.js';
  import Icon from '../components/Icon.svelte';
  import Logo from '../components/Logo.svelte';
  import Ring from '../components/Ring.svelte';
  import Notices from '../components/Notices.svelte';
  import WarningBanner from '../components/WarningBanner.svelte';
  import UpdateBanner from '../components/UpdateBanner.svelte';
  import Today from './Today.svelte';
  import ListView from './ListView.svelte';
  import Lists from './Lists.svelte';
  import ItemDetail from './ItemDetail.svelte';
  import Help from './Help.svelte';
  import Settings from './Settings.svelte';
  import SignedOut from './SignedOut.svelte';
  import Join from './Join.svelte';
  import Warnings from './Warnings.svelte';
  import Search from './Search.svelte';

  let {
    core,
    signedOut,
    pendingLink = null,
    links,
    debugLog,
    update,
    version,
  }: {
    core: ClientCore;
    /** The account is gone from this device (sign-out); back to Welcome. */
    signedOut: () => void;
    /** An invite link that arrived before there was an account. */
    pendingLink?: string | null;
    /** Invite links opened while the app runs. */
    links?: ((cb: (link: string) => void) => () => void) | undefined;
    /** Debug builds: the device's log, under Settings. */
    debugLog?: DebugLog | undefined;
    /** A downloaded update, ready for a restart. */
    update?: AppUpdate | undefined;
    /** The app's version, under Settings. */
    version?: string | undefined;
  } = $props();

  let view = $state<View>({ name: 'today' });
  /** The task open in the side panel, beside whatever page is showing. */
  let selected = $state<{ list: ListId; id: string } | null>(null);

  const stores = $derived({ lists: fromStore(core.lists), today: fromStore(core.today), connection: fromStore(core.connection), warnings: fromStore(core.warnings) });
  const settings = $derived(fromStore(core.settings));
  $effect(() => {
    const c = settings.current.clock;
    clock.hour12 = c === undefined ? deviceHour12() : c === '12h';
  });
  const all = $derived(stores.lists.current);
  const lists = $derived(all.filter((l) => !l.inbox));
  const todayCount = $derived(stores.today.current.length);
  const warnings = $derived(stores.warnings.current);
  const connection = $derived(stores.connection.current);
  const status = $derived(
    connection === 'offline' ? t('app.offline') : connection === 'connecting' ? t('app.connecting') : '',
  );
  const inbox = $derived(all.find((l) => l.inbox));

  /** Phones: where a list's back button leads, the page it was opened from. */
  let listFrom = $state<'lists' | 'search'>('lists');

  function go(next: View) {
    if (next.name === 'list' && view.name !== 'list') listFrom = view.name === 'search' ? 'search' : 'lists';
    view = next;
    selected = null;
    if (typeof window !== 'undefined') window.scrollTo?.(0, 0);
  }

  const openList = (id: ListId) => go({ name: 'list', id });
  const isList = (id: ListId) => view.name === 'list' && view.id === id;
  /** Phones: a list is a page under the Lists tab. */
  const inLists = $derived(view.name === 'lists' || (view.name === 'list' && view.id !== core.inbox));
  const select = (list: ListId, id: string) => (selected = { list, id });

  /** An invite link pasted where a list is named opens Join. Any other link (half an invite, say) does nothing; anything else is a name. */
  function join(text: string): 'opened' | 'ignored' | 'name' {
    if (parseInviteLink(text) === null) return /^[a-z][a-z0-9+.-]*:\/\//i.test(text.trim()) ? 'ignored' : 'name';
    go({ name: 'join', link: text.trim() });
    return 'opened';
  }

  /** A new list, named right in the sidebar. */
  let naming = $state(false);
  let newName = $state('');
  let createError = $state('');

  async function createList(e: SubmitEvent) {
    e.preventDefault();
    const name = newName.trim();
    if (name === '') return void (naming = false);
    createError = '';
    const pasted = join(name);
    if (pasted === 'ignored') return;
    if (pasted === 'opened') return void ((newName = ''), (naming = false));
    try {
      openList(await core.createList(name));
      newName = '';
      naming = false;
    } catch {
      createError = t('lists.createFailed');
    }
  }

  // A list that is gone (left, removed, deleted) can't stay open.
  $effect(() => {
    const gone = (id: ListId) => id !== core.inbox && all.length > 0 && !all.some((l) => l.id === id);
    const id = view.name === 'list' ? view.id : null;
    if (id !== null && gone(id)) view = { name: 'lists' };
    if (selected !== null && gone(selected.list)) selected = null;
  });

  onMount(() => {
    if (pendingLink !== null) view = { name: 'join', link: pendingLink };
    return links?.((link) => go({ name: 'join', link }));
  });
</script>

{#if connection === 'signed-out'}
  <SignedOut
    signIn={async () => {
      await core.signOut();
      signedOut();
    }}
  />
{:else if view.name === 'join'}
  <!-- A page of its own, like sign-in: nothing else to do until the invite is answered. -->
  {#key view.link}
    <Join {core} link={view.link} joined={openList} cancel={() => go({ name: 'today' })} />
  {/key}
{:else}
<div class="app" class:panel={selected !== null}>
  <nav aria-label={t('nav.main')}>
    <div class="top">
      <span class="logo"><Logo /></span>
      <button class="tool" class:active={view.name === 'search'} aria-label={t('nav.search')} onclick={() => go({ name: 'search' })}><Icon name="search" /></button>
    </div>
    <button class="item" class:active={view.name === 'today'} aria-current={view.name === 'today' ? 'page' : undefined} onclick={() => go({ name: 'today' })}>
      <span class="icon"><Icon name="sun" /></span><span class="label">{t('nav.today')}</span>
      {#if todayCount > 0}<span class="count" aria-hidden="true">{todayCount}</span>{/if}
    </button>
    <button class="item" class:active={isList(core.inbox)} aria-current={isList(core.inbox) ? 'page' : undefined} onclick={() => openList(core.inbox)}>
      <span class="icon"><Icon name="inbox" /></span><span class="label">{t('nav.inbox')}</span>
      {#if (inbox?.openTasks ?? 0) > 0}<span class="count" aria-hidden="true">{inbox?.openTasks}</span>{/if}
    </button>
    <button class="item lists-tab" class:active={inLists} aria-current={view.name === 'lists' ? 'page' : undefined} onclick={() => go({ name: 'lists' })}>
      <span class="icon"><Icon name="lists" /></span><span class="label">{t('nav.lists')}</span>
    </button>
    <button class="item search-tab" class:active={view.name === 'search'} aria-current={view.name === 'search' ? 'page' : undefined} onclick={() => go({ name: 'search' })}>
      <span class="icon"><Icon name="search" /></span><span class="label">{t('nav.search')}</span>
    </button>
    <div class="mine">
      <div class="mine-head">
        <span>{t('nav.lists')}</span>
        <button class="tool small" aria-label={t('lists.add')} aria-expanded={naming} onclick={() => ((naming = !naming), (createError = ''))}><Icon name="plus" /></button>
      </div>
      {#if naming}
        <form class="new" onsubmit={createList}>
          <!-- svelte-ignore a11y_autofocus -->
          <input
            bind:value={newName}
            placeholder={t('lists.new')}
            aria-label={t('lists.new')}
            autofocus
            enterkeyhint="done"
            onkeydown={(e) => e.key === 'Escape' && ((naming = false), (newName = ''))}
            onblur={() => newName.trim() === '' && (naming = false)}
          />
        </form>
        <p class="hint">{t('lists.joinHint')}</p>
        {#if createError !== ''}<p class="error" role="alert">{createError}</p>{/if}
      {/if}
      {#each lists as l (l.id)}
        <button class="item" class:active={isList(l.id)} aria-current={isList(l.id) ? 'page' : undefined} onclick={() => openList(l.id)}>
          <span class="icon"><Ring done={l.doneTasks} open={l.openTasks} /></span><span class="label">{l.name || t('list.untitled')}</span>
          {#if l.openTasks > 0}<span class="count" aria-hidden="true">{l.openTasks}</span>{/if}
        </button>
      {/each}
    </div>
    <div class="foot">
      <button class="item help-item" class:active={view.name === 'help'} aria-current={view.name === 'help' ? 'page' : undefined} onclick={() => go({ name: 'help' })}>
        <span class="icon"><Icon name="help" /></span><span class="label">{t('nav.help')}</span>
      </button>
      <button class="item settings-tab" class:active={view.name === 'settings'} aria-current={view.name === 'settings' ? 'page' : undefined} onclick={() => go({ name: 'settings' })}>
        <span class="icon"><Icon name="settings" /></span><span class="label">{t('nav.settings')}</span>
      </button>
    </div>
  </nav>

  <!-- One status line: under the sidebar on desktop; on phones a bar at the top, only when not in sync. -->
  <p class="sync" class:off={status !== ''} role="status"><span class="dot" aria-hidden="true"></span>{status || t('app.synced')}</p>

  <main>
    {#if warnings.length > 0 && view.name !== 'warnings'}
      <WarningBanner count={warnings.length} open={() => go({ name: 'warnings' })} />
    {/if}
    {#if update}
      <UpdateBanner {update} />
    {/if}
    <Notices {core} />

    {#if view.name === 'today'}
      <Today {core} open={select} selected={selected?.id} />
    {:else if view.name === 'search'}
      <Search {core} open={select} {openList} selected={selected?.id} />
    {:else if view.name === 'lists'}
      <Lists {core} open={openList} {join} />
    {:else if view.name === 'list'}
      {#key view.id}
        <ListView
          {core}
          id={view.id}
          open={select}
          selected={selected?.id}
          back={view.id === core.inbox ? undefined : { label: t(`nav.${listFrom}`), go: () => go({ name: listFrom }) }}
          gone={() => go({ name: 'lists' })}
        />
      {/key}
    {:else if view.name === 'settings'}
      <Settings {core} {signedOut} {debugLog} {version} help={() => go({ name: 'help' })} />
    {:else if view.name === 'help'}
      <Help back={() => go({ name: 'settings' })} />
    {:else}
      <Warnings {core} back={() => go({ name: 'today' })} />
    {/if}
  </main>

  {#if selected !== null}
    <aside aria-label={t('item.panel')}>
      {#key selected.list + selected.id}
        <ItemDetail {core} list={selected.list} id={selected.id} close={() => (selected = null)} />
      {/key}
    </aside>
  {/if}
</div>
{/if}

<style>
  .app {
    --sidebar: 244px;
    display: grid;
    grid-template-columns: var(--sidebar) 1fr;
    min-height: 100vh;
  }
  .app.panel {
    grid-template-columns: var(--sidebar) 1fr 380px;
  }
  aside {
    position: sticky;
    top: 0;
    height: 100vh;
    overflow-y: auto;
    border-left: 1px solid var(--divider);
    background: var(--bg-sidebar);
  }

  nav {
    position: sticky;
    top: 0;
    height: 100vh;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: max(20px, calc(4px + var(--titlebar))) 12px 44px;
    border-right: 1px solid var(--divider);
    background: var(--bg-sidebar);
    font-size: 14px;
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 36px;
    margin-bottom: 16px;
    padding: 0 0 0 10px;
  }
  .tool {
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
  .tool.small {
    width: 28px;
    height: 28px;
    border-radius: 6px;
  }
  .tool:hover,
  .tool.active {
    background: var(--surface-hover);
    color: var(--text);
  }
  .item {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
    height: 34px;
    padding: 0 10px;
    border: 0;
    border-radius: 8px;
    background: transparent;
    color: var(--text);
    opacity: 0.88;
    text-align: left;
    cursor: pointer;
  }
  .item:hover {
    background: var(--surface-hover);
  }
  .item.active {
    background: var(--surface-hover);
    font-weight: 500;
    opacity: 1;
  }
  .icon {
    display: inline-flex;
    width: 16px;
    justify-content: center;
    color: var(--text-muted);
  }
  .icon :global(svg) {
    width: 16px;
    height: 16px;
  }
  .label {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .count {
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 400;
    font-variant-numeric: tabular-nums;
  }
  .mine {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-top: 22px;
    /* Many lists scroll here; Today, Inbox and the foot stay put. */
    flex: 1;
    min-height: 0;
    overflow-y: auto;
  }
  .mine > * {
    flex-shrink: 0;
  }
  .mine-head {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--bg-sidebar);
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 28px;
    padding: 0 4px 0 10px;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 500;
  }
  .new input {
    width: 100%;
    height: 34px;
    padding: 0 10px;
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    background: var(--surface);
    outline: none;
  }
  .hint {
    margin: 4px 0 0;
    padding: 0 10px;
    color: var(--text-faint);
    font-size: 12px;
  }
  .error {
    margin: 0;
    padding: 0 10px;
    color: var(--danger);
    font-size: 12px;
  }
  .foot {
    display: flex;
    flex-direction: column;
    margin-top: auto;
    padding-top: 16px;
  }
  /* The sidebar holds the lists and search; the Lists and Search tabs are for phones, which have no sidebar. */
  .lists-tab,
  .search-tab {
    display: none;
  }

  .sync {
    position: fixed;
    left: 0;
    bottom: 16px;
    z-index: 1;
    width: var(--sidebar);
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    padding: 0 22px;
    color: var(--text-muted);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dot {
    flex: none;
    width: 6px;
    height: 6px;
    border-radius: 3px;
    background: var(--ok);
  }
  .sync.off .dot {
    background: var(--text-faint);
  }

  main {
    width: 100%;
    max-width: 832px;
    min-width: 0;
    padding: max(20px, calc(4px + var(--titlebar))) 56px 64px;
  }

  /* Phones: the sidebar becomes a bottom tab bar; lists and search are reached from it. */
  @media (max-width: 700px) {
    .app,
    .app.panel {
      grid-template-columns: 1fr;
      padding-bottom: calc(72px + env(safe-area-inset-bottom, 0px));
    }
    main {
      padding: calc(12px + var(--titlebar)) 16px 32px;
    }
    /* No room beside the page: the panel covers it. */
    aside {
      position: fixed;
      inset: 0;
      z-index: 2;
      height: auto;
      border-left: 0;
      background: var(--bg);
    }
    /* A floating glass pill over the page, as on iOS; it sits just above the home indicator. */
    nav {
      position: fixed;
      inset: auto 16px max(12px, calc(env(safe-area-inset-bottom, 0px) - 12px)) 16px;
      top: auto;
      z-index: 1;
      height: auto;
      overflow: visible;
      flex-direction: row;
      justify-content: space-around;
      padding: 4px;
      border: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
      border-radius: 999px;
      background: color-mix(in srgb, var(--bg-popover) 72%, transparent);
      -webkit-backdrop-filter: blur(20px) saturate(1.6);
      backdrop-filter: blur(20px) saturate(1.6);
      box-shadow: 0 8px 28px rgb(0 0 0 / 0.28);
    }
    .item {
      flex: 1;
      flex-direction: column;
      justify-content: center;
      gap: 2px;
      height: auto;
      min-width: 44px;
      min-height: 44px;
      padding: 6px 8px;
      border-radius: 999px;
      font-size: 11px;
    }
    .lists-tab,
    .search-tab {
      display: flex;
    }
    /* The tab bar is full: on a phone, Help is opened from Settings. */
    .count,
    .top,
    .mine,
    .help-item {
      display: none;
    }
    .foot {
      flex: 1;
      margin: 0;
      padding: 0;
    }
    .sync {
      inset: var(--titlebar) 0 auto 0;
      width: auto;
      padding: 6px 16px;
      background: var(--bg-sidebar);
    }
    .sync:not(.off) {
      display: none;
    }
  }
  /* A touch phone: a row's drag handle sits left of it and is shown without hover, so it needs the room. */
  @media (max-width: 700px) and (hover: none) {
    main {
      padding-left: 36px;
    }
  }
</style>
