import { addDays, en, localDateString, parseTask, parseTime as parseTimeIn } from '@runer/client-core';
import { clock } from './clock.svelte.js';
import { t } from './messages.js';

/** Formatting for due dates (`YYYY-MM-DD`, a date only) and reminders (an instant), in the device's zone. */

const dayMonth = new Intl.DateTimeFormat('en', { day: 'numeric', month: 'short' });
const weekday = new Intl.DateTimeFormat('en', { weekday: 'long' });
const time12 = new Intl.DateTimeFormat('en', { hour: 'numeric', minute: '2-digit', hour12: true });
const time24 = new Intl.DateTimeFormat('en', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
/** A time on the user's clock (`clock.svelte.ts`): "21:30" or "9:30 PM". */
const time = { format: (d: Date) => (clock.hour12 ? time12 : time24).format(d) };
const shortDay = new Intl.DateTimeFormat('en', { weekday: 'short' });

export const today = (): string => localDateString(new Date());

export function asDate(d: string): Date {
  const [y, m, day] = d.split('-').map(Number) as [number, number, number];
  return new Date(y, m - 1, day);
}

/** "Today", "Tomorrow", "Yesterday", a weekday within the next week, else "3 Oct". */
export function dueLabel(due: string, now: string = today()): string {
  if (due === now) return t('date.today');
  if (due === addDays(now, 1)) return t('date.tomorrow');
  if (due === addDays(now, -1)) return t('date.yesterday');
  if (due > now && due <= addDays(now, 6)) return weekday.format(asDate(due));
  return dayMonth.format(asDate(due));
}

/** "Today 09:30", "3 Oct 18:00". */
export function reminderLabel(at: number, now: string = today()): string {
  const d = new Date(at);
  return `${dueLabel(localDateString(d), now)} ${time.format(d)}`;
}

/** "Fri", "Mon 5 Oct": the dates beside the date picker's quick picks. */
export const shortWeekday = (d: string): string => shortDay.format(asDate(d));
export const shortDate = (d: string): string => `${shortDay.format(asDate(d))} ${dayMonth.format(asDate(d))}`;

/**
 * A task's date as one value. A task has a date, or a date and a time; the
 * time is its reminder (`remindAt`, on the due date), so "Today 14:00".
 */
export function whenLabel(due: string | undefined, remindAt: number | undefined, now: string = today()): string | undefined {
  if (remindAt !== undefined) return reminderLabel(remindAt, now);
  return due === undefined ? undefined : dueLabel(due, now);
}

/** `HH:MM` on the user's clock, for the time field. */
export function timeLabel(hm: string): string {
  const [h, m] = hm.split(':').map(Number) as [number, number];
  return time.format(new Date(2000, 0, 1, h, m));
}

/** A typed time as `HH:MM` (`nl/`): "14:30", "1430", "2pm"; `null` if it isn't one. */
export const parseTime = (s: string): string | null => parseTimeIn(s, en);

/** For `<input type="datetime-local">`: local `YYYY-MM-DDTHH:MM`. */
export function toLocalInput(at: number): string {
  const d = new Date(at);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${localDateString(d)}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** The instant a `datetime-local` value names in the device's zone, or `null` if empty or invalid. */
export function fromLocalInput(v: string): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(v);
  if (m === null) return null;
  const [, y, mo, d, h, mi] = m.map(Number) as [number, number, number, number, number, number];
  const ms = new Date(y, mo - 1, d, h, mi).getTime();
  return Number.isFinite(ms) ? ms : null;
}

/** "3 Oct, 14:05", for invite expiry. */
export function instantLabel(ms: number): string {
  const d = new Date(ms);
  return `${dayMonth.format(d)}, ${time.format(d)}`;
}

/** What a typed task says about its date (`nl/`): the title without those words, and the date as a task holds it. */
export interface Typed {
  title: string;
  due: string;
  remindAt?: number;
  /** As the task will show it: "Tomorrow 09:30". */
  label: string;
}

/** The date typed in `text`, read on the device's clock, or `null` if there is none. */
export function readTyped(text: string, now: Date = new Date()): Typed | null {
  const pad = (n: number) => String(n).padStart(2, '0');
  const p = parseTask(text, { today: localDateString(now), now: `${pad(now.getHours())}:${pad(now.getMinutes())}`, lang: en });
  if (p === null) return null;
  const remindAt = p.time === undefined ? null : fromLocalInput(`${p.due}T${p.time}`);
  return { title: p.title, due: p.due, ...(remindAt === null ? {} : { remindAt }), label: whenLabel(p.due, remindAt ?? undefined, localDateString(now))! };
}
