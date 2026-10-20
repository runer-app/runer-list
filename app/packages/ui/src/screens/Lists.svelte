<script lang="ts">
  import type { ClientCore, ListId, ListSummary } from '@runer/client-core';
  import { fromStore } from 'svelte/store';
  import { t } from '../messages.js';
  import { membersSummary } from '../people.js';
  import Capture from '../components/Capture.svelte';
  import Icon from '../components/Icon.svelte';
  import Ring from '../components/Ring.svelte';

  /** Every list: "New list" (or a pasted invite link, which `join` takes), then "Shared" and "Only me", each with its open tasks and who's in it. */
  let { core, open, join }: { core: ClientCore; open: (id: ListId) => void; join: (text: string) => 'opened' | 'ignored' | 'name' } = $props();

  const store = $derived(fromStore(core.lists));
  const lists = $derived(store.current.filter((l) => !l.inbox));
  const shared = $derived(lists.filter((l) => l.shared));
  const mine = $derived(lists.filter((l) => !l.shared));
  let error = $state('');

  /** `false` keeps the text in the field: a link that isn't an invite does nothing. */
  function add(name: string): boolean {
    const pasted = join(name);
    if (pasted === 'name') void create(name);
    return pasted !== 'ignored';
  }

  async function create(name: string) {
    error = '';
    try {
      open(await core.createList(name));
    } catch {
      error = t('lists.createFailed');
    }
  }

  function sub(l: ListSummary): string {
    const tasks = l.openTasks === 1 ? t('lists.openTask') : t('lists.openTasks', { n: l.openTasks });
    return `${tasks} · ${l.shared ? membersSummary(l.members, core.me, l.names) : t('lists.private')}`;
  }
</script>

{#snippet section(title: string, items: ListSummary[])}
  <h2>{title}</h2>
  <ul>
    {#each items as l (l.id)}
      <li>
        <button onclick={() => open(l.id)}>
          <Ring done={l.doneTasks} open={l.openTasks} />
          <span class="text">
            <span class="name">{l.name || t('list.untitled')}</span>
            <span class="sub">{sub(l)}</span>
          </span>
          <Icon name="chevron" />
        </button>
      </li>
    {/each}
  </ul>
{/snippet}

<h1>{t('nav.lists')}</h1>
<p class="lead">{t('lists.lead')}</p>
<Capture placeholder={t('lists.new')} {add} />
<p class="hint">{t('lists.joinHint')}</p>
{#if error !== ''}<p class="error" role="alert">{error}</p>{/if}

{#if lists.length === 0}
  <p class="empty">{t('lists.empty')}</p>
{/if}
{#if shared.length > 0}{@render section(t('lists.shared'), shared)}{/if}
{#if mine.length > 0}{@render section(t('lists.onlyMe'), mine)}{/if}

<style>
  h1 {
    margin: 8px 0 0;
    font-size: 32px;
    font-weight: 600;
    letter-spacing: -0.03em;
    line-height: 1.15;
  }
  .lead {
    margin: 8px 0 16px;
    color: var(--text-muted);
    font-size: 15px;
  }
  h2 {
    margin: 28px 0 4px;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 500;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li button {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    min-height: 52px;
    padding: 6px 0;
    border: 0;
    border-bottom: 1px solid var(--divider);
    background: none;
    color: var(--text);
    text-align: left;
    cursor: pointer;
  }
  li button > :global(svg:last-child) {
    width: 16px;
    height: 16px;
    color: var(--text-faint);
  }
  .text {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }
  .name {
    font-size: 15px;
    overflow-wrap: anywhere;
  }
  .sub {
    color: var(--text-muted);
    font-size: 12px;
  }
  .empty {
    margin-top: 24px;
    color: var(--text-muted);
    font-size: 14px;
  }
  .hint {
    margin: 6px 0 0;
    color: var(--text-faint);
    font-size: 13px;
  }
  .error {
    color: var(--danger);
    font-size: 13px;
  }
</style>
