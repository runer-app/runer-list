<script lang="ts">
  import type { ClientCore, ListId } from '@runer/client-core';
  import { fromStore } from 'svelte/store';
  import { t } from '../messages.js';
  import { membersSummary } from '../people.js';
  import Avatars from '../components/Avatars.svelte';
  import Confirm from '../components/Confirm.svelte';
  import Icon from '../components/Icon.svelte';
  import ListEditor from '../components/ListEditor.svelte';
  import ShareList from './ShareList.svelte';
  import { dismiss } from '../dismiss.js';

  /**
   * One list, or the Inbox: a bar with who can see it,
   * Share and the "⋯" menu (show completed tasks; for a list also rename,
   * delete or leave), then the title and the
   * list as a page of tasks and notes, edited in place (ListEditor).
   */
  let {
    core,
    id,
    open,
    selected,
    back,
    gone,
  }: {
    core: ClientCore;
    id: ListId;
    open: (list: ListId, item: string) => void;
    /** The task open in the side panel. */
    selected?: string | undefined;
    /** Phones, which have no sidebar: the way back to the page the list was opened from, named by `label`. */
    back?: { label: string; go: () => void } | undefined;
    /** The list was deleted or left here. */
    gone: () => void;
  } = $props();

  const list = $derived(core.list(id));
  const stores = $derived({ content: fromStore(list.content), lists: fromStore(core.lists) });
  const content = $derived(stores.content.current);
  const summary = $derived(stores.lists.current.find((l) => l.id === id));
  const members = $derived(summary?.members ?? []);
  const names = $derived(summary?.names ?? {});
  const role = $derived(summary?.role ?? list.role);
  const shared = $derived(members.length > 1);

  let editingName = $state(false);
  let name = $state('');
  let menu = $state(false);
  let sharing = $state(false);
  /** Done tasks are hidden until asked for, per visit. */
  let showDone = $state(false);
  const doneCount = $derived(content.items.filter((i) => i.type === 'task' && i.done).length);

  function startRename() {
    name = content.name;
    editingName = true;
    menu = false;
  }

  function saveName(e?: SubmitEvent) {
    e?.preventDefault();
    const next = name.trim();
    if (next !== '' && next !== content.name) list.setName(next);
    editingName = false;
  }
</script>

{#if back !== undefined}
  <p class="up"><button class="back" onclick={back.go}><Icon name="back" />{back.label}</button></p>
{/if}
<div class="bar">
  <p class="kicker">
    {#if shared}
      <Icon name="people" />{t('list.sharedKicker')}<span class="sep">·</span><span class="who">{membersSummary(members, core.me, names)}</span>
    {:else}
      <Icon name="lock" />{t('list.privateKicker')}
    {/if}
  </p>
  <div class="tools">
    {#if !list.inbox}
      {#if shared}<button class="faces" tabindex="-1" aria-hidden="true" onclick={() => (sharing = true)}><Avatars {members} {names} me={core.me} /></button>{/if}
      <button class="share" onclick={() => (sharing = true)}>{t('share.open')}</button>
    {/if}
    <div class="menu-wrap" use:dismiss={() => (menu = false)}>
      <button class="dots" aria-label={t('list.menu')} aria-expanded={menu} onclick={() => (menu = !menu)}>
        <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><circle cx="5" cy="12" r="1.6" fill="currentColor" /><circle cx="12" cy="12" r="1.6" fill="currentColor" /><circle cx="19" cy="12" r="1.6" fill="currentColor" /></svg>
      </button>
      {#if menu}
        <div class="menu">
          <button onclick={() => ((showDone = !showDone), (menu = false))} disabled={doneCount === 0 && !showDone}>
            {showDone ? t('editor.hideDone') : t('editor.showDone', { n: doneCount })}
          </button>
          <!-- The Inbox can't be renamed, deleted or left. -->
          {#if !list.inbox}
            <div class="line" role="separator"></div>
            <button onclick={startRename}>{t('list.rename')}</button>
            {#if role === 'owner'}
              <Confirm
                look="text"
                label={t('list.delete')}
                question={t('list.deleteQuestion', { name: content.name || t('list.untitled') })}
                warning={shared ? t('list.deleteSharedWarning') : t('list.deleteWarning')}
                confirmLabel={t('list.deleteConfirm')}
                run={async () => {
                  await list.delete();
                  gone();
                }}
              />
            {:else}
              <Confirm
                look="text"
                label={t('list.leave')}
                question={t('list.leaveQuestion', { name: content.name || t('list.untitled') })}
                warning={t('list.leaveWarning')}
                confirmLabel={t('list.leaveConfirm')}
                run={async () => {
                  await list.leave();
                  gone();
                }}
              />
            {/if}
          {/if}
        </div>
      {/if}
    </div>
  </div>
</div>

{#if list.inbox}
  <h1>{t('nav.inbox')}</h1>
  <p class="lead">{t('inbox.lead')}</p>
{:else if editingName}
  <form class="rename" onsubmit={saveName}>
    <!-- svelte-ignore a11y_autofocus -->
    <input class="h1" bind:value={name} aria-label={t('list.name')} autofocus onblur={() => saveName()} onkeydown={(e) => e.key === 'Escape' && (editingName = false)} />
  </form>
{:else}
  <h1><button class="name" onclick={startRename} title={t('list.rename')}>{content.name || t('list.untitled')}</button></h1>
{/if}

{#key id}
  <ListEditor {list} items={content.items} {members} {names} me={core.me} {selected} {showDone} open={(item) => open(id, item)} />
{/key}

{#if sharing}
  <ShareList {core} {id} close={() => (sharing = false)} />
{/if}

<style>
  /* The sidebar is the way around on desktop; the back button is for phones. */
  .up {
    display: none;
    margin: 0 0 4px;
  }
  @media (max-width: 700px) {
    .up {
      display: block;
    }
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
  .bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    min-height: 36px;
    margin-bottom: 12px;
  }
  .kicker {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    margin: 0;
    color: var(--text-muted);
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .kicker :global(svg) {
    flex: none;
    width: 14px;
    height: 14px;
  }
  .sep {
    color: var(--text-faint);
  }
  .who {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tools {
    display: flex;
    flex: none;
    align-items: center;
    gap: 8px;
  }
  .faces {
    display: flex;
    padding: 0;
    border: 0;
    background: none;
    cursor: pointer;
  }
  .share {
    height: 32px;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: transparent;
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
  }
  .share:hover {
    background: var(--surface-hover);
  }
  .menu-wrap {
    position: relative;
  }
  .dots {
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border: 0;
    border-radius: 8px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }
  .dots:hover,
  .dots[aria-expanded='true'] {
    background: var(--surface-hover);
    color: var(--text);
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 5;
    display: flex;
    flex-direction: column;
    width: 200px;
    padding: 6px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--bg-popover);
    box-shadow:
      0 16px 40px rgb(8 6 12 / 0.55),
      0 2px 6px rgb(8 6 12 / 0.4);
  }
  .menu > button,
  .menu :global(.trigger) {
    height: 34px;
    padding: 0 10px;
    border: 0;
    border-radius: 8px;
    background: transparent;
    color: var(--text);
    font-size: 14px;
    font-weight: 400;
    text-align: left;
    cursor: pointer;
  }
  @media (pointer: coarse) {
    .menu > button,
    .menu :global(.trigger) {
      height: 44px;
    }
  }
  .menu > button:hover:not(:disabled),
  .menu :global(.trigger:hover) {
    background: var(--surface-hover);
  }
  .menu > button:disabled {
    color: var(--text-faint);
    cursor: default;
  }
  .line {
    height: 1px;
    margin: 6px 4px;
    background: var(--divider);
  }
  h1,
  .h1 {
    margin: 0 0 8px;
    font-size: 32px;
    font-weight: 600;
    letter-spacing: -0.03em;
    line-height: 1.15;
    overflow-wrap: anywhere;
  }
  .name {
    padding: 0;
    border: 0;
    background: none;
    font: inherit;
    letter-spacing: inherit;
    text-align: left;
    cursor: text;
  }
  .rename {
    margin: 0;
  }
  .h1 {
    width: 100%;
    border: 0;
    border-bottom: 1px solid var(--border-strong);
    background: transparent;
    outline: none;
  }
  .lead {
    margin: 0;
    color: var(--text-muted);
    font-size: 15px;
  }
</style>
