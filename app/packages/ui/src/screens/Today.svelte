<script lang="ts">
  import { localDateString, type ClientCore, type ListId, type TaskRef } from '@runer/client-core';
  import { tick } from 'svelte';
  import { fromStore } from 'svelte/store';
  import { moveBlock } from '../blocks.js';
  import { reminderLabel, whenLabel, type Typed } from '../dates.js';
  import { startDrag, stepGap } from '../drag.js';
  import { t } from '../messages.js';
  import Capture from '../components/Capture.svelte';
  import Grip from '../components/Grip.svelte';
  import TaskRow from '../components/TaskRow.svelte';

  /**
   * The start screen: what's due, across every list, and
   * capture straight into it. Below it, My tasks: the next 7 days and later
   * (client-spec §3.6). Each section can be dragged into the
   * user's own order (`ClientCore.setTaskOrder`), within the section, since
   * moving a task between sections would mean changing its date.
   */
  let { core, open, selected }: { core: ClientCore; open: (list: ListId, id: string) => void; selected?: string | undefined } = $props();

  const stores = $derived({ today: fromStore(core.today), mine: fromStore(core.myTasks) });
  const tasks = $derived(stores.today.current);
  const mine = $derived(stores.mine.current);
  const date = new Intl.DateTimeFormat('en', { weekday: 'long', day: 'numeric', month: 'long' }).format(new Date());

  type Section = 'overdue' | 'today' | 'upcoming' | 'planned';
  /** The date (and time) beside a task; in Today's own section only a time, since the day is known. */
  const when = (item: TaskRef['item'], withDate: boolean) =>
    withDate ? whenLabel(item.due, item.remindAt) : item.remindAt === undefined ? undefined : reminderLabel(item.remindAt);
  const sections = $derived<Record<Section, TaskRef[]>>({
    overdue: tasks.filter((r) => r.overdue),
    today: tasks.filter((r) => !r.overdue),
    upcoming: mine.upcoming,
    planned: mine.planned,
  });
  const lists: Record<Section, HTMLUListElement | undefined> = $state({ overdue: undefined, today: undefined, upcoming: undefined, planned: undefined });
  let dragging = $state<string | null>(null);
  let drop = $state<{ section: Section; gap: number } | null>(null);

  // A task captured here goes to the Inbox, due today, so it shows up right here.
  function add(title: string, when?: Typed) {
    core.list(core.inbox).addTask(title, { due: when?.due ?? localDateString(new Date()), ...(when?.remindAt === undefined ? {} : { remindAt: when.remindAt }) });
  }

  const listName = (ref: TaskRef) => (ref.listId === core.inbox ? t('nav.inbox') : ref.listName || t('list.untitled'));
  const keyOf = (ref: TaskRef) => ref.listId + ref.item.id;

  /** Saves every section's order, with `section` rearranged. */
  function reorder(section: Section, from: number, gap: number) {
    const next = { ...sections, [section]: moveBlock(sections[section], from, gap) };
    core.setTaskOrder([...next.overdue, ...next.today, ...next.upcoming, ...next.planned].map((r) => r.item.id));
  }

  function beginDrag(e: PointerEvent, section: Section, from: number) {
    dragging = keyOf(sections[section][from]!);
    startDrag(e, {
      rows: () => [...(lists[section]?.children ?? [])] as HTMLElement[],
      from,
      hover: (gap) => (drop = gap === null ? null : { section, gap }),
      drop: (gap) => reorder(section, from, gap),
    });
    const done = () => {
      dragging = null;
      window.removeEventListener('pointerup', done);
      window.removeEventListener('pointercancel', done);
    };
    window.addEventListener('pointerup', done);
    window.addEventListener('pointercancel', done);
  }

  async function keyOnGrip(e: KeyboardEvent, section: Section, from: number) {
    const gap = stepGap(from, sections[section].length, e.key);
    if (gap === null) return;
    e.preventDefault();
    reorder(section, from, gap);
    await tick();
    (lists[section]?.children[gap > from ? gap - 1 : gap]?.querySelector('.grip') as HTMLElement | null)?.focus();
  }
</script>

{#snippet rows(section: Section, withDate: boolean)}
  {@const refs = sections[section]}
  <ul bind:this={lists[section]}>
    {#each refs as ref, i (keyOf(ref))}
      <TaskRow
        item={ref.item}
        overdue={ref.overdue}
        when={when(ref.item, withDate)}
        reminder={ref.item.remindAt !== undefined}
        where={listName(ref)}
        toggle={() => core.list(ref.listId).toggleDone(ref.item.id)}
        open={() => open(ref.listId, ref.item.id)}
        selected={selected === ref.item.id}
        dragged={dragging === keyOf(ref)}
        drop={drop?.section === section && drop.gap === i}
      >
        {#snippet grip()}
          <Grip down={(e) => beginDrag(e, section, i)} key={(e) => void keyOnGrip(e, section, i)} />
        {/snippet}
      </TaskRow>
    {/each}
  </ul>
  {#if drop?.section === section && drop.gap === refs.length}<div class="drop-end"></div>{/if}
{/snippet}

{#snippet heading(label: string, n: number, late = false)}
  <h2 class:late>{label}<span class="n">{n}</span></h2>
{/snippet}

<p class="date">{date}</p>
<h1>{t('nav.today')}</h1>
<p class="lead">{t('today.lead')}</p>

{#if sections.overdue.length > 0}
  {@render heading(t('task.overdue'), sections.overdue.length, true)}
  {@render rows('overdue', true)}
  {@render heading(t('nav.today'), sections.today.length)}
{/if}
{#if tasks.length === 0}
  <div class="empty">
    <p class="clear">{t('today.clear')}</p>
    <p>{t('today.empty')}</p>
  </div>
{:else if sections.today.length > 0}
  {@render rows('today', false)}
{/if}
<Capture placeholder={t('today.capture')} {add} shortcut="n" dates />

{#if mine.upcoming.length > 0}
  {@render heading(t('today.upcoming'), mine.upcoming.length)}
  {@render rows('upcoming', true)}
{/if}
{#if mine.planned.length > 0}
  {@render heading(t('today.planned'), mine.planned.length)}
  {@render rows('planned', true)}
{/if}

<style>
  .date {
    margin: 8px 0 0;
    color: var(--text-muted);
    font-size: 13px;
  }
  h1 {
    margin: 4px 0 8px;
    font-size: 32px;
    font-weight: 600;
    letter-spacing: -0.03em;
    line-height: 1.15;
  }
  .lead {
    margin: 0 0 20px;
    color: var(--text-muted);
    font-size: 15px;
  }
  h2 {
    display: flex;
    gap: 8px;
    margin: 28px 0 0;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 500;
  }
  h2.late {
    margin-top: 8px;
    color: var(--danger);
  }
  .n {
    color: var(--text-faint);
    font-weight: 400;
  }
  ul {
    list-style: none;
    margin: 4px 0 0;
    padding: 0;
  }
  .drop-end {
    height: 2px;
    margin-top: -1px;
    border-radius: 1px;
    background: var(--accent);
  }
  .empty {
    margin: 8px 0 12px;
    padding: 20px 0;
    border-top: 1px solid var(--divider);
    border-bottom: 1px solid var(--divider);
  }
  .empty p {
    margin: 0;
    color: var(--text-muted);
    font-size: 13px;
  }
  .empty .clear {
    margin-bottom: 4px;
    color: var(--text);
    font-size: 15px;
    font-weight: 500;
  }
</style>
