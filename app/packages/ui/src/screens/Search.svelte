<script lang="ts">
  import type { ClientCore, ListId, SearchHit } from '@runer/client-core';
  import { whenLabel } from '../dates.js';
  import { t } from '../messages.js';
  import Icon from '../components/Icon.svelte';
  import TaskRow from '../components/TaskRow.svelte';

  /** Search across every list (core `search`): lists by name, tasks and text by what they say. A task opens in the side panel. */
  let {
    core,
    open,
    openList,
    selected,
  }: { core: ClientCore; open: (list: ListId, id: string) => void; openList: (id: ListId) => void; selected?: string | undefined } = $props();

  let query = $state('');
  let hits = $state<SearchHit[]>([]);

  $effect(() => {
    const q = query;
    void core.search(q).then((h) => q === query && (hits = h));
  });

  const listName = (h: SearchHit) => (h.listId === core.inbox ? t('nav.inbox') : h.listName || t('list.untitled'));
</script>

<label class="field">
  <Icon name="search" />
  <!-- svelte-ignore a11y_autofocus -->
  <input type="search" bind:value={query} placeholder={t('search.placeholder')} aria-label={t('nav.search')} autofocus />
</label>

{#if query.trim() !== '' && hits.length === 0}
  <p class="empty">{t('search.none')}</p>
{/if}

<ul>
  {#each hits as h (h.item?.id ?? `list:${h.listId}`)}
    {#if h.item === undefined}
      <li class="list">
        <button onclick={() => openList(h.listId)}><Icon name="list" /><span>{listName(h)}</span><span class="where">{t('search.list')}</span></button>
      </li>
    {:else if h.item.type === 'task'}
      {@const item = h.item}
      <TaskRow
        {item}
        when={whenLabel(item.due, item.remindAt)}
        reminder={item.remindAt !== undefined}
        where={listName(h)}
        toggle={() => core.list(h.listId).toggleDone(item.id)}
        open={() => open(h.listId, item.id)}
        selected={selected === item.id}
      />
    {:else}
      <li class="list">
        <button onclick={() => openList(h.listId)}><span class="text">{h.item.title}</span><span class="where">{listName(h)}</span></button>
      </li>
    {/if}
  {/each}
</ul>

<style>
  .field {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 44px;
    margin-top: 8px;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface);
    color: var(--text-muted);
  }
  .field:focus-within {
    border-color: var(--border-strong);
  }
  input {
    flex: 1;
    min-width: 0;
    border: 0;
    background: transparent;
    color: var(--text);
    font-size: 15px;
    outline: none;
  }
  ul {
    list-style: none;
    margin: 16px 0 0;
    padding: 0;
  }
  .list button {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    min-height: 40px;
    padding: 0;
    border: 0;
    border-bottom: 1px solid var(--divider);
    background: none;
    font-size: 15px;
    text-align: left;
    cursor: pointer;
  }
  .list button:hover .text,
  .list button:hover span:not(.where) {
    text-decoration: underline;
    text-decoration-color: var(--border-strong);
  }
  .text {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-muted);
  }
  .list button > span:not(.where):not(.text) {
    flex: 1;
  }
  .where {
    color: var(--text-muted);
    font-size: 12px;
  }
  .empty {
    margin-top: 24px;
    color: var(--text-muted);
    font-size: 14px;
  }
</style>
