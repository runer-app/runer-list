<script lang="ts">
  import { tickTask } from '../tick.svelte.js';
  import { formatRule, parseRule, weekdayOf, type ClientCore, type Freq, type ListId, type Rule } from '@runer/client-core';
  import { onDestroy } from 'svelte';
  import { fromStore } from 'svelte/store';
  import { fromLocalInput, repeatLabel, today, toLocalInput } from '../dates.js';
  import { t, type MessageKey } from '../messages.js';
  import { idOf, personName } from '../people.js';
  import DatePicker from '../components/DatePicker.svelte';
  import Icon from '../components/Icon.svelte';

  /**
   * Task detail, in a panel beside the page: done, title,
   * description, and rows of properties: the date (a date, or a date and time, which is the reminder),
   * its repeat (how often, and on which weekdays or day of the month), who it's assigned to (shared lists only) and its list. Reordering is the
   * list's drag handle, not here. Text is saved when a field is left, and
   * when the panel closes, so closing never loses an edit.
   */
  let { core, list: listId, id, close }: { core: ClientCore; list: ListId; id: string; close: () => void } = $props();

  const list = $derived(core.list(listId));
  const stores = $derived({ content: fromStore(list.content), lists: fromStore(core.lists) });
  const item = $derived(stores.content.current.items.find((i) => i.id === id));
  const lists = $derived(stores.lists.current);
  const members = $derived(lists.find((l) => l.id === listId)?.members ?? []);
  const names = $derived(lists.find((l) => l.id === listId)?.names ?? {});
  const nameOf = (l: { id: ListId; inbox: boolean; name: string }) => (l.inbox ? t('nav.inbox') : l.name || t('list.untitled'));
  const listName = $derived(list.inbox ? t('nav.inbox') : stores.content.current.name || t('list.untitled'));
  const time = $derived(item?.remindAt === undefined ? undefined : toLocalInput(item.remindAt).slice(11));
  const due = $derived(item?.due ?? (item?.remindAt === undefined ? undefined : toLocalInput(item.remindAt).slice(0, 10)));

  const rule = $derived(parseRule(item?.repeat));
  const FREQS: { freq: Freq; label: MessageKey; unit: MessageKey }[] = [
    { freq: 'DAILY', label: 'item.repeatDaily', unit: 'unit.days' },
    { freq: 'WEEKLY', label: 'item.repeatWeekly', unit: 'unit.weeks' },
    { freq: 'MONTHLY', label: 'item.repeatMonthly', unit: 'unit.months' },
    { freq: 'YEARLY', label: 'item.repeatYearly', unit: 'unit.years' },
  ];
  const dayNames = [1, 2, 3, 4, 5, 6, 7].map((d) => new Date(2024, 0, d));
  const longDay = new Intl.DateTimeFormat('en', { weekday: 'long' });
  const narrowDay = new Intl.DateTimeFormat('en', { weekday: 'narrow' });

  /** Writes the repeat; the task's date moves to the rule's first date on or after it (`ListDoc.setRepeat`). */
  function saveRule(r: Rule | null) {
    list.setRepeat(id, r === null ? null : formatRule(r));
  }

  function pickFreq(v: string) {
    if (v === '') return saveRule(null);
    const freq = v as Freq;
    const from = due ?? today();
    saveRule({
      freq,
      interval: rule?.freq === freq ? rule.interval : 1,
      byDay: freq === 'WEEKLY' ? (rule?.freq === 'WEEKLY' && rule.byDay.length > 0 ? rule.byDay : [weekdayOf(from)]) : [],
      ...(freq === 'MONTHLY' ? { byMonthDay: rule?.byMonthDay ?? Number(from.slice(8)) } : {}),
    });
  }

  /** A whole number from 1 to `max` typed in a field, or `null`. */
  function count(el: HTMLInputElement, max: number): number | null {
    const n = Number(el.value);
    if (!Number.isInteger(n) || n < 1 || n > max) {
      el.value = '';
      return null;
    }
    return n;
  }

  function pickInterval(el: HTMLInputElement) {
    const n = count(el, 999);
    if (rule !== null && n !== null && n !== rule.interval) saveRule({ ...rule, interval: n });
    else if (rule !== null) el.value = String(rule.interval);
  }

  function pickMonthDay(el: HTMLInputElement) {
    const n = count(el, 31);
    if (rule !== null && n !== null && n !== rule.byMonthDay) saveRule({ ...rule, byMonthDay: n });
    else if (rule?.byMonthDay !== undefined) el.value = String(rule.byMonthDay);
  }

  /** At least one day stays picked. */
  function toggleDay(d: number) {
    if (rule === null) return;
    const days = rule.byDay.length > 0 ? rule.byDay : [weekdayOf(due ?? today())];
    const next = days.includes(d) ? days.filter((x) => x !== d) : [...days, d].sort((a, b) => a - b);
    if (next.length > 0) saveRule({ ...rule, byDay: next });
  }

  let titleEl = $state<HTMLTextAreaElement>();
  let bodyEl = $state<HTMLTextAreaElement>();

  function saveTitle() {
    const v = titleEl?.value.trim() ?? '';
    if (item !== undefined && v !== '' && v !== item.title) list.setTitle(id, v);
  }

  function saveBody() {
    if (bodyEl === undefined || item === undefined) return;
    const v = bodyEl.value.trim();
    if (v !== (item.body ?? '')) list.setBody(id, v === '' ? null : v);
  }

  onDestroy(() => {
    saveTitle();
    saveBody();
  });

  /** One date for the user; stored as `due` plus, with a time, a reminder at that time. */
  function setWhen(date: string | null, hm: string | null) {
    list.setDue(id, date);
    list.setReminder(id, date !== null && hm !== null ? fromLocalInput(`${date}T${hm}`) : null);
  }

  function grow(el: HTMLTextAreaElement) {
    const fit = () => {
      el.style.height = 'auto';
      el.style.height = `${el.scrollHeight}px`;
    };
    fit();
    el.addEventListener('input', fit);
    return { destroy: () => el.removeEventListener('input', fit) };
  }

  function enterLeaves(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      (e.currentTarget as HTMLElement).blur();
    }
  }

  function moveTo(e: Event) {
    const to = (e.currentTarget as HTMLSelectElement).value;
    if (to === listId) return;
    saveTitle();
    saveBody();
    list.moveToList(id, to);
    close();
  }

  function remove() {
    list.remove(id);
    close();
  }
</script>

<div class="panel">
  <header>
    <span class="where">{listName}</span>
    <button class="icon-button" onclick={close} aria-label={t('item.close')}><Icon name="close" /></button>
  </header>

  {#if item === undefined}
    <p class="empty" role="status">{t('item.gone')}</p>
  {:else}
    <div class="task" class:done={item.done}>
      {#if item.type === 'task'}
        <input class="box" type="checkbox" checked={item.done} onchange={(e) => tickTask(list, id, e.currentTarget)} aria-label={t('task.done')} />
      {/if}
      <div class="text">
        <textarea
          class="title"
          rows="1"
          bind:this={titleEl}
          use:grow
          value={item.title}
          onblur={saveTitle}
          onkeydown={enterLeaves}
          aria-label={t('item.title')}
        ></textarea>
        {#if item.type === 'task'}
          <textarea
            class="body"
            rows="1"
            bind:this={bodyEl}
            use:grow
            value={item.body ?? ''}
            onblur={saveBody}
            placeholder={t('item.descriptionHint')}
            aria-label={t('item.description')}
          ></textarea>
        {/if}
      </div>
    </div>

    {#if item.type === 'task'}
      <dl class="props">
        <dt>{t('item.date')}</dt>
        <dd><DatePicker {due} {time} overdue={!item.done && due !== undefined && due < today()} onpick={setWhen} /></dd>

        {#if !item.done}
          <dt>{t('item.repeat')}</dt>
          <dd>
            <label class="prop" class:set={rule !== null} title={rule === null ? undefined : t('item.repeatHint')}>
              <Icon name="repeat" />
              <select value={rule?.freq ?? ''} onchange={(e) => pickFreq(e.currentTarget.value)} aria-label={t('item.repeat')}>
                <option value="">{t('item.repeatNone')}</option>
                {#each FREQS as f (f.freq)}
                  <option value={f.freq}>{t(f.label)}</option>
                {/each}
              </select>
            </label>
            {#if rule !== null}
              <button class="icon-button stop" onclick={() => saveRule(null)} aria-label={t('item.repeatStop')} title={t('item.repeatStop')}><Icon name="close" /></button>
            {/if}
          </dd>
          {#if rule !== null}
            {@const unit = FREQS.find((f) => f.freq === rule.freq)!.unit}
            <dt>{t('item.repeatEvery')}</dt>
            <dd class="every">
              <input
                class="num"
                type="number"
                inputmode="numeric"
                min="1"
                max="999"
                value={rule.interval}
                onchange={(e) => pickInterval(e.currentTarget)}
                aria-label={t('item.repeatInterval')}
              />
              <span>{t(unit)}</span>
              {#if rule.freq === 'MONTHLY'}
                <span>{t('item.repeatOnDay')}</span>
                <input
                  class="num"
                  type="number"
                  inputmode="numeric"
                  min="1"
                  max="31"
                  value={rule.byMonthDay ?? Number((due ?? today()).slice(8))}
                  onchange={(e) => pickMonthDay(e.currentTarget)}
                  aria-label={t('item.repeatMonthDay')}
                />
              {/if}
            </dd>
            {#if rule.freq === 'WEEKLY'}
              {@const picked = rule.byDay.length > 0 ? rule.byDay : [weekdayOf(due ?? today())]}
              <dd class="days" role="group" aria-label={t('item.repeatDays')}>
                {#each dayNames as d, i (i)}
                  <button class="day" aria-pressed={picked.includes(i)} aria-label={longDay.format(d)} title={longDay.format(d)} onclick={() => toggleDay(i)}>{narrowDay.format(d)}</button>
                {/each}
              </dd>
            {/if}
            <dd class="summary" aria-live="polite">{repeatLabel(item.repeat)}</dd>
          {/if}
        {/if}

        {#if members.length > 1}
          <dt>{t('item.assignee')}</dt>
          <dd>
            <label class="prop" class:set={item.assignee !== undefined}>
              <Icon name="person" />
              <select value={item.assignee ?? ''} onchange={(e) => list.assign(id, e.currentTarget.value === '' ? null : e.currentTarget.value)} aria-label={t('item.assignee')}>
                <option value="">{t('task.unassigned')}</option>
                {#each members as m (idOf(m))}
                  <option value={idOf(m)}>{personName(idOf(m), core.me, members, names)}</option>
                {/each}
                {#if item.assignee !== undefined && !members.some((m) => idOf(m) === item.assignee)}
                  <option value={item.assignee}>{t('item.formerMember')}</option>
                {/if}
              </select>
            </label>
          </dd>
        {/if}

        {#if lists.length > 1}
          <dt>{t('item.list')}</dt>
          <dd>
            <label class="prop set">
              <Icon name={list.inbox ? 'inbox' : 'list'} />
              <select value={listId} onchange={moveTo} aria-label={t('item.list')}>
                {#each lists as l (l.id)}
                  <option value={l.id}>{nameOf(l)}</option>
                {/each}
              </select>
            </label>
          </dd>
        {/if}
      </dl>
    {/if}

    <footer>
      <button class="delete" onclick={remove}><Icon name="trash" /> {t('item.delete')}</button>
    </footer>
  {/if}
</div>

<style>
  .panel {
    display: flex;
    flex-direction: column;
    min-height: 100%;
    padding-top: var(--titlebar);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    height: 52px;
    padding: 0 12px 0 24px;
    color: var(--text-muted);
    font-size: 13px;
  }
  .where {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .icon-button {
    flex: none;
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border: 0;
    border-radius: 8px;
    background: none;
    color: var(--text-muted);
    cursor: pointer;
  }
  .icon-button:hover {
    background: var(--surface-hover);
    color: var(--text);
  }

  .task {
    display: flex;
    gap: 12px;
    padding: 20px 24px 0;
  }
  .box {
    width: 20px;
    height: 20px;
    margin-top: 4px;
  }
  .text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  textarea {
    width: 100%;
    padding: 0;
    border: 0;
    background: none;
    resize: none;
    overflow: hidden;
  }
  textarea:focus-visible {
    outline: none;
  }
  .title {
    font-size: 20px;
    font-weight: 600;
    line-height: 1.3;
    letter-spacing: -0.02em;
  }
  .done .title {
    color: var(--text-muted);
    text-decoration: line-through;
    text-decoration-thickness: 1.5px;
  }
  .body {
    color: var(--text-muted);
    font-size: 14px;
    line-height: 1.5;
  }
  .body:focus {
    color: var(--text);
  }
  .body::placeholder {
    color: var(--text-faint);
  }

  /* Label | value rows. The value column leaves room for the date picker's popover. */
  .props {
    display: grid;
    grid-template-columns: 84px 1fr;
    margin: 22px 24px 0;
    border-top: 1px solid var(--divider);
  }
  dt,
  dd {
    display: flex;
    align-items: center;
    height: 44px;
    margin: 0;
    border-bottom: 1px solid var(--divider);
  }
  dt {
    color: var(--text-muted);
    font-size: 13px;
  }
  dd {
    min-width: 0;
  }
  .prop {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 100%;
    height: 30px;
    padding: 0 10px;
    border-radius: 7px;
    background: var(--surface);
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
  }
  .prop.set {
    color: var(--text);
  }
  .prop:hover {
    background: var(--surface-hover);
  }
  .prop :global(svg) {
    width: 13px;
    height: 13px;
  }
  .prop:focus-within {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .prop select {
    min-width: 0;
    appearance: none;
    padding: 0;
    border: 0;
    background: none;
    font-size: 13px;
    text-overflow: ellipsis;
    cursor: pointer;
  }
  .prop select:focus-visible {
    outline: none;
  }

  .stop {
    margin-left: 4px;
  }
  .stop :global(svg) {
    width: 14px;
    height: 14px;
  }
  .every {
    gap: 8px;
    color: var(--text-muted);
    font-size: 13px;
  }
  .num {
    width: 56px;
    height: 30px;
    padding: 0 8px;
    border: 0;
    border-radius: 7px;
    background: var(--surface);
    color: var(--text);
    font-size: 13px;
  }
  .num:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  /* The whole width, so seven days fit on a phone. */
  .days {
    grid-column: 1 / -1;
    gap: 4px;
  }
  .day {
    width: 30px;
    height: 30px;
    padding: 0;
    border: 0;
    border-radius: 50%;
    background: var(--surface);
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
  }
  .day:hover {
    background: var(--surface-hover);
  }
  .day[aria-pressed='true'] {
    background: var(--accent);
    color: var(--accent-text);
  }
  .day:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .summary {
    grid-column: 1 / -1;
    height: auto;
    padding: 8px 0;
    color: var(--text-muted);
    font-size: 12px;
  }
  @media (pointer: coarse) {
    .days {
      height: 56px;
    }
    .day {
      width: 44px;
      height: 44px;
    }
  }

  footer {
    margin-top: auto;
    padding: 6px 16px;
    border-top: 1px solid var(--divider);
  }
  .delete {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    padding: 0 8px;
    border: 0;
    border-radius: 8px;
    background: none;
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
  }
  .delete :global(svg) {
    width: 14px;
    height: 14px;
  }
  .delete:hover {
    background: var(--surface-hover);
    color: var(--danger);
  }

  .empty {
    margin: 20px 24px;
    color: var(--text-muted);
  }
</style>
