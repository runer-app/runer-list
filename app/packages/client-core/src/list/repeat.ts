import { addDays } from '../views/dates.js';
import { isDateString } from './item.js';

/**
 * A task's `repeat`: a subset of the RFC 5545 RRULE syntax.
 *
 * - `FREQ=DAILY|WEEKLY|MONTHLY|YEARLY`, required;
 * - `INTERVAL=n`, 1 to 999, default 1;
 * - `BYDAY=MO,TH`, weekly only: plain weekdays, no numbers;
 * - `BYMONTHDAY=n`, monthly only, 1 to 31; a month without that day uses its last day.
 *
 * Anything else, a repeated part or a lower-case word, is not a rule: it is
 * refused on write and ignored on read. Occurrences are counted from the
 * task's due date (RFC 5545's DTSTART), with weeks starting on Monday.
 */
export interface Rule {
  freq: Freq;
  interval: number;
  /** Weekly: Monday = 0, sorted, no repeats. Empty: the due date's weekday. */
  byDay: number[];
  /** Monthly: the day of the month; absent: the due date's day. */
  byMonthDay?: number;
}

export type Freq = 'DAILY' | 'WEEKLY' | 'MONTHLY' | 'YEARLY';

export const WEEKDAYS = ['MO', 'TU', 'WE', 'TH', 'FR', 'SA', 'SU'] as const;
const FREQS: readonly string[] = ['DAILY', 'WEEKLY', 'MONTHLY', 'YEARLY'];
const COUNT = /^[1-9]\d{0,2}$/;

/** The rule `s` holds, or `null` if it isn't one this client supports. */
export function parseRule(s: unknown): Rule | null {
  if (typeof s !== 'string' || s === '') return null;
  const parts = new Map<string, string>();
  for (const part of s.split(';')) {
    const m = /^([A-Z]+)=([A-Z0-9,]+)$/.exec(part);
    if (m === null || parts.has(m[1]!)) return null;
    parts.set(m[1]!, m[2]!);
  }
  const freq = parts.get('FREQ');
  if (freq === undefined || !FREQS.includes(freq)) return null;
  const rule: Rule = { freq: freq as Freq, interval: 1, byDay: [] };
  for (const [k, v] of parts) {
    if (k === 'FREQ') continue;
    if (k === 'INTERVAL' && COUNT.test(v)) rule.interval = Number(v);
    else if (k === 'BYDAY' && freq === 'WEEKLY') {
      const days = v.split(',').map((d) => WEEKDAYS.indexOf(d as (typeof WEEKDAYS)[number]));
      if (days.includes(-1) || new Set(days).size !== days.length) return null;
      rule.byDay = days.sort((a, b) => a - b);
    } else if (k === 'BYMONTHDAY' && freq === 'MONTHLY' && COUNT.test(v) && Number(v) <= 31) rule.byMonthDay = Number(v);
    else return null;
  }
  return rule;
}

/** The rule as it is written into a document: parts in a fixed order, `INTERVAL=1` left out. */
export function formatRule(rule: Rule): string {
  const parts = [`FREQ=${rule.freq}`];
  if (rule.interval !== 1) parts.push(`INTERVAL=${rule.interval}`);
  if (rule.freq === 'WEEKLY' && rule.byDay.length > 0) parts.push(`BYDAY=${[...rule.byDay].sort((a, b) => a - b).map((d) => WEEKDAYS[d]).join(',')}`);
  if (rule.freq === 'MONTHLY' && rule.byMonthDay !== undefined) parts.push(`BYMONTHDAY=${rule.byMonthDay}`);
  return parts.join(';');
}

/** `s` in its canonical form, or `null` if it isn't a supported rule. */
export function canonicalRule(s: unknown): string | null {
  const r = parseRule(s);
  return r === null ? null : formatRule(r);
}

/**
 * The first date after both `due` and `today` that the rule names, counted
 * from `due`: missed dates are skipped, and the result depends on `due`, not
 * on the day it was ticked, as long as `due` isn't in the past.
 */
export function nextDue(rule: Rule | string, due: string, today: string): string {
  const r = typeof rule === 'string' ? mustParse(rule) : rule;
  assertDate(due);
  assertDate(today);
  const after = today > due ? today : due;
  const n = r.interval;
  switch (r.freq) {
    case 'DAILY':
      return addDays(due, (Math.floor(daysBetween(due, after) / n) + 1) * n);
    case 'WEEKLY': {
      const days = r.byDay.length > 0 ? r.byDay : [weekdayOf(due)];
      const anchor = mondayOf(due);
      let c = addDays(after, 1);
      for (;;) {
        const week = daysBetween(anchor, mondayOf(c)) / 7;
        const off = week % n;
        if (off !== 0) c = addDays(anchor, (week + n - off) * 7);
        else if (days.includes(weekdayOf(c))) return c;
        else c = addDays(c, 1);
      }
    }
    case 'MONTHLY':
    case 'YEARLY': {
      const step = r.freq === 'MONTHLY' ? n : 12 * n;
      const [y, m, d] = parts(due);
      const day = r.freq === 'MONTHLY' ? (r.byMonthDay ?? d) : d;
      const [ay, am] = parts(after);
      // Start a step before `after`'s month and walk forward; at most a few steps.
      let k = Math.max(0, Math.floor(((ay - y) * 12 + (am - m)) / step) - 1);
      for (;;) {
        const c = onDay(y, m - 1 + k * step, day);
        if (c > after) return c;
        k++;
      }
    }
  }
}

/** Whether `date` is one the rule names when counted from `date` itself. */
export function matches(rule: Rule, date: string): boolean {
  if (rule.freq === 'WEEKLY') return rule.byDay.length === 0 || rule.byDay.includes(weekdayOf(date));
  if (rule.freq === 'MONTHLY' && rule.byMonthDay !== undefined) {
    const [y, m] = parts(date);
    return onDay(y, m - 1, rule.byMonthDay) === date;
  }
  return true;
}

/** The first date on or after `from` that the rule names, counted from `from`. */
export function firstDue(rule: Rule | string, from: string): string {
  const r = typeof rule === 'string' ? mustParse(rule) : rule;
  return matches(r, from) ? from : nextDue(r, from, from);
}

/**
 * A reminder moved to `date`, at the same wall-clock time on the device's
 * clock: 09:00 stays 09:00 across a change to or from summer time.
 */
export function moveReminder(at: number, date: string): number {
  const t = new Date(at);
  const [y, m, d] = parts(date);
  return new Date(y, m - 1, d, t.getHours(), t.getMinutes(), t.getSeconds(), t.getMilliseconds()).getTime();
}

function mustParse(s: string): Rule {
  const r = parseRule(s);
  if (r === null) throw new RangeError(`not a supported repeat rule: ${s}`);
  return r;
}

function assertDate(d: string): void {
  if (!isDateString(d)) throw new RangeError(`not a YYYY-MM-DD date: ${d}`);
}

const parts = (date: string) => date.split('-').map(Number) as [number, number, number];
const utc = (date: string) => {
  const [y, m, d] = parts(date);
  return Date.UTC(y, m - 1, d);
};
const daysBetween = (a: string, b: string) => Math.round((utc(b) - utc(a)) / 86_400_000);
/** Monday = 0. */
export const weekdayOf = (date: string): number => (new Date(utc(date)).getUTCDay() + 6) % 7;
const mondayOf = (date: string) => addDays(date, -weekdayOf(date));

/** Day `day` of month `month0` (zero-based, may overflow into later years) of `year`, or that month's last day. */
function onDay(year: number, month0: number, day: number): string {
  const first = new Date(Date.UTC(year, month0, 1));
  const last = new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth() + 1, 0)).getUTCDate();
  first.setUTCDate(Math.min(day, last));
  return first.toISOString().slice(0, 10);
}
