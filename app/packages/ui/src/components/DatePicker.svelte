<script lang="ts">
  import { addDays, comingWeekend, localDateString, nextWeek } from '@runer/client-core';
  import { asDate, parseTime, shortDate, shortWeekday, timeLabel, today, whenLabel } from '../dates.js';
  import { dismiss } from '../dismiss.js';
  import { t } from '../messages.js';
  import Icon from './Icon.svelte';

  /**
   * A task's date: a chip that opens quick picks, a month and an optional
   * time. A date alone has no time; a time is the task's reminder (`dates.ts`,
   * `whenLabel`). Picking a day keeps the time and closes.
   */
  let {
    due,
    time,
    overdue = false,
    onpick,
  }: { due: string | undefined; time: string | undefined; overdue?: boolean; onpick: (due: string | null, time: string | null) => void } = $props();

  let open = $state(false);
  let addingTime = $state(false);
  let month = $state(today().slice(0, 7));

  const now = $derived(today());
  const label = $derived(whenLabel(due, due !== undefined && time !== undefined ? atTime(due, time) : undefined, now));

  const monthName = new Intl.DateTimeFormat('en', { month: 'long', year: 'numeric' });
  const narrowDay = new Intl.DateTimeFormat('en', { weekday: 'narrow' });
  const longDate = new Intl.DateTimeFormat('en', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
  /** Monday first; 2024-01-01 was a Monday. */
  const weekdays = Array.from({ length: 7 }, (_, i) => narrowDay.format(new Date(2024, 0, 1 + i)));

  const picks = $derived(
    [
      { label: t('date.today'), date: now },
      { label: t('date.tomorrow'), date: addDays(now, 1) },
      { label: t('date.nextWeek'), date: nextWeek(now) },
      { label: t('date.nextWeekend'), date: comingWeekend(now) },
    ].map((p, i) => ({ ...p, hint: i < 2 ? shortWeekday(p.date) : shortDate(p.date) })),
  );

  /** The days to draw for `month`: whole weeks, Monday first. */
  const days = $derived.by(() => {
    const first = `${month}-01`;
    const d = asDate(first);
    const lead = (d.getDay() + 6) % 7;
    const length = new Date(d.getFullYear(), d.getMonth() + 1, 0).getDate();
    return Array.from({ length: Math.ceil((lead + length) / 7) * 7 }, (_, i) => addDays(first, i - lead));
  });

  function atTime(date: string, hm: string): number {
    const [h, m] = hm.split(':').map(Number) as [number, number];
    const d = asDate(date);
    d.setHours(h, m);
    return d.getTime();
  }

  function toggle() {
    open = !open;
    addingTime = false;
    badTime = false;
    month = (due ?? now).slice(0, 7);
  }

  function pick(date: string) {
    onpick(date, time ?? null);
    open = false;
  }

  function shift(by: number) {
    const d = asDate(`${month}-01`);
    month = localDateString(new Date(d.getFullYear(), d.getMonth() + by, 1)).slice(0, 7);
  }

  let badTime = $state(false);

  /**
   * Saves a typed time. A plain text field, not `type="time"`: WebKit's time
   * field commits on its own schedule, and the popover can close (on a press
   * outside) before it does. This runs on Enter, on blur, and when the field
   * goes away, so a typed time is never lost.
   */
  function commitTime(el: HTMLInputElement): boolean {
    const v = el.value.trim();
    if (v === '') return true;
    const hm = parseTime(v);
    badTime = hm === null;
    if (hm !== null && hm !== time) onpick(due ?? now, hm);
    if (hm !== null) el.value = timeLabel(hm);
    return hm !== null;
  }

  function timeField(el: HTMLInputElement) {
    if (time === undefined) el.focus();
    return { destroy: () => void commitTime(el) };
  }

  let timeEl = $state<HTMLInputElement>();

  /** Removes the time, or with `date` null the date too; empties the field first so going away doesn't save it again. */
  function clear(date: string | null) {
    if (timeEl !== undefined) timeEl.value = '';
    addingTime = false;
    onpick(date, null);
  }

  function keyOnTime(e: KeyboardEvent) {
    if (e.key === 'Enter' && commitTime(e.currentTarget as HTMLInputElement)) open = false;
  }
</script>

<div class="date" use:dismiss={() => (open = false)}>
  <div class="chips">
    <button class="chip" class:set={due !== undefined} class:late={overdue} onclick={toggle} aria-expanded={open} aria-label={`${t('item.date')}: ${label ?? t('item.noDate')}`}>
      <Icon name="calendar" />
      <span>{label ?? t('item.addDate')}</span>
    </button>
    {#if due !== undefined}
      <button class="clear" onclick={() => clear(null)} aria-label={t('item.removeDate')}><Icon name="close" /></button>
    {/if}
  </div>

  {#if open}
    <div class="pop" role="dialog" aria-label={t('item.date')}>
      <ul class="picks">
        {#each picks as p (p.label)}
          <li>
            <button class:on={p.date === due} onclick={() => pick(p.date)}>
              <span class="check">{#if p.date === due}<Icon name="check" />{/if}</span><span class="pick">{p.label}</span><span class="hint">{p.hint}</span>
            </button>
          </li>
        {/each}
      </ul>

      <div class="month">
        <div class="head">
          <strong>{monthName.format(asDate(`${month}-01`))}</strong>
          <button onclick={() => shift(-1)} aria-label={t('date.previousMonth')} disabled={month <= now.slice(0, 7)}><Icon name="back" /></button>
          <button onclick={() => shift(1)} aria-label={t('date.nextMonth')}><Icon name="chevron" /></button>
        </div>
        <div class="grid">
          {#each weekdays as w, i (i)}<span class="wd" aria-hidden="true">{w}</span>{/each}
          {#each days as d (d)}
            <button
              class="day"
              class:out={!d.startsWith(month)}
              class:past={d < now}
              class:today={d === now}
              class:on={d === due}
              aria-pressed={d === due}
              aria-label={longDate.format(asDate(d))}
              onclick={() => pick(d)}>{asDate(d).getDate()}</button
            >
          {/each}
        </div>
      </div>

      <div class="time">
        <span class="time-label"><Icon name="clock" /> {t('item.time')}</span>
        {#if time !== undefined || addingTime}
          <label>
            <input
              use:timeField
              bind:this={timeEl}
              class:bad={badTime}
              value={time === undefined ? '' : timeLabel(time)}
              placeholder={timeLabel('14:30')}
              onblur={(e) => commitTime(e.currentTarget)}
              oninput={() => (badTime = false)}
              onkeydown={keyOnTime}
              aria-label={t('item.time')}
              aria-invalid={badTime}
              size="6"
            />
          </label>
          {#if time !== undefined}
            <button class="link" onclick={() => clear(due ?? now)} aria-label={t('item.removeTime')}>{t('item.clear')}</button>
          {/if}
        {:else}
          <button class="link" onclick={() => (addingTime = true)}>{t('item.addTime')}</button>
        {/if}
      </div>
      {#if time !== undefined}<p class="note">{t('item.timeHint')}</p>{/if}
    </div>
  {/if}
</div>

<style>
  .date {
    position: relative;
    flex: 1;
  }
  .chips {
    display: flex;
    align-items: center;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 30px;
    padding: 0 10px;
    border: 0;
    border-radius: 7px;
    background: var(--surface);
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
  }
  .chip :global(svg) {
    width: 13px;
    height: 13px;
  }
  .chip:hover {
    background: var(--surface-hover);
  }
  .chip.set {
    background: var(--accent-soft);
    color: var(--accent-soft-text);
    font-weight: 500;
  }
  .chip.late {
    color: var(--danger);
  }
  .chip[aria-expanded='true'] {
    box-shadow: 0 0 0 2px var(--border-strong);
  }
  .clear {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    margin-left: auto;
    border: 0;
    border-radius: 6px;
    background: none;
    color: var(--text-faint);
    cursor: pointer;
  }
  .clear :global(svg) {
    width: 12px;
    height: 12px;
  }
  .clear:hover {
    background: var(--surface-hover);
    color: var(--text);
  }

  /* Right-aligned with the row, so it stays inside the panel. */
  .pop {
    position: absolute;
    z-index: 20;
    top: calc(100% + 8px);
    right: 0;
    width: min(300px, calc(100vw - 32px));
    overflow: hidden;
    border: 1px solid var(--border);
    border-radius: 14px;
    background: var(--bg-popover);
    box-shadow:
      0 18px 44px rgb(8 6 12 / 0.6),
      0 2px 6px rgb(8 6 12 / 0.4);
    animation: drop 120ms ease-out;
  }
  @keyframes drop {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .pop {
      animation: none;
    }
  }

  .picks {
    margin: 0;
    padding: 6px;
    list-style: none;
  }
  .picks button {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    height: 34px;
    padding: 0 10px;
    border: 0;
    border-radius: 8px;
    background: none;
    font-size: 14px;
    text-align: left;
    cursor: pointer;
  }
  @media (pointer: coarse) {
    .picks button {
      height: 44px;
    }
  }
  .picks button:hover {
    background: var(--surface-hover);
  }
  .check {
    display: grid;
    width: 14px;
    color: var(--accent-label);
  }
  .check :global(svg) {
    width: 14px;
    height: 14px;
    stroke-width: 2.6;
  }
  .pick {
    flex: 1;
  }
  .picks button.on .pick {
    color: var(--accent-soft-text);
    font-weight: 500;
  }
  .hint {
    color: var(--text-muted);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
  }

  .month {
    padding: 12px 14px 10px;
    border-top: 1px solid var(--divider);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 2px;
    padding-bottom: 8px;
  }
  .head strong {
    flex: 1;
    font-size: 13px;
    font-weight: 600;
  }
  .head button {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border: 0;
    border-radius: 6px;
    background: none;
    color: var(--text-muted);
    cursor: pointer;
  }
  .head button :global(svg) {
    width: 14px;
    height: 14px;
  }
  .head button:hover:not(:disabled) {
    background: var(--surface-hover);
    color: var(--text);
  }
  .head button:disabled {
    opacity: 0.35;
    cursor: default;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(7, minmax(0, 1fr));
    row-gap: 2px;
    text-align: center;
  }
  .wd {
    height: 24px;
    line-height: 24px;
    color: var(--text-muted);
    font-size: 11px;
  }
  .day {
    justify-self: center;
    width: 30px;
    height: 30px;
    margin: 1px 0;
    border: 0;
    border-radius: 50%;
    background: none;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
    cursor: pointer;
  }
  .day:hover {
    background: var(--surface-hover);
  }
  .day.out,
  .day.past {
    color: var(--text-faint);
  }
  .day.out {
    opacity: 0.7;
  }
  .day.today {
    box-shadow: inset 0 0 0 1.5px var(--border-strong);
    color: var(--accent-soft-text);
    font-weight: 600;
  }
  .day.on {
    background: var(--accent);
    box-shadow: none;
    color: var(--accent-text);
    font-weight: 600;
    opacity: 1;
  }

  .time {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-top: 1px solid var(--divider);
  }
  .time-label {
    flex: 1;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding-left: 4px;
    color: var(--text-muted);
    font-size: 13px;
  }
  .time-label :global(svg) {
    width: 14px;
    height: 14px;
  }
  .time input {
    width: 88px;
    height: 30px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: 7px;
    background: var(--bg);
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  .time input.bad {
    border-color: var(--danger);
  }
  .link {
    height: 30px;
    padding: 0 10px;
    border: 0;
    border-radius: 7px;
    background: none;
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
  }
  .link:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  .note {
    margin: 0;
    padding: 0 14px 10px;
    color: var(--text-muted);
    font-size: 12px;
  }
</style>
