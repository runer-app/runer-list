import { isDateString } from '../list/item.js';
import { addDays } from '../views/dates.js';

/**
 * Dates and times typed into a task ("Call mom tomorrow 9:30"). The engine
 * here knows no language: the words come from a `Language` (`en.ts`). It has
 * no clock and no time zone either; "now" is passed in.
 */

/** One language's words. Every word is lower case. */
export interface Language {
  today: string[];
  tomorrow: string[];
  /** Monday first. The first name of a day is the full one; the others are short forms, which count in fewer places (see `parseTask`). */
  weekdays: string[][];
  /** January first; full and short names alike. */
  months: string[][];
  next: string[];
  this: string[];
  on: string[];
  at: string[];
  in: string[];
  of: string[];
  /** "a", "one": a count of one in "in a week". */
  one: string[];
  units: Record<'day' | 'week' | 'month' | 'year', string[]>;
  weekend: string[];
  /** What may follow a day's number: "12th". */
  ordinals: string[];
  /** Named times, as `HH:MM`. */
  times: Record<string, string>;
  /** The first forms are whole words; a bare letter ("2p") only counts in a time field. */
  am: string[];
  pm: string[];
  /** Whether `12/10` is 12 October. */
  dayFirst: boolean;
  /** The sheet of what's understood, read at `examplesAt`; the tests run it and Help shows it. */
  examples: Example[];
  examplesAt: Now;
  /** Texts that hold no date, however much they look like it. */
  plain: string[];
}

export interface Example {
  text: string;
  title: string;
  due: string;
  time?: string;
}

export interface Now {
  /** `YYYY-MM-DD`. */
  today: string;
  /** `HH:MM`. */
  now: string;
}

export interface Parsed {
  /** The text without the recognised words. */
  title: string;
  due: string;
  /** `HH:MM`, on `due`. */
  time?: string;
  /** Where the recognised words were in the text. */
  match: [start: number, end: number];
}

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** A typed time as `HH:MM`: "14:30", "1430", "9", "9.15", "2pm", "2:30 pm"; `null` if it isn't one. */
export function parseTime(s: string, lang: Language): string | null {
  const half = (words: string[]) => words.map(escape).join('|');
  const m = new RegExp(`^(\\d{1,2})(?:[:.]?(\\d{2}))?\\s*(?:(${half(lang.am)})|(${half(lang.pm)}))?$`, 'i').exec(s.trim());
  if (m === null) return null;
  let h = Number(m[1]);
  const min = m[2] === undefined ? 0 : Number(m[2]);
  if (m[3] !== undefined || m[4] !== undefined) {
    if (h < 1 || h > 12) return null;
    h = (h % 12) + (m[4] !== undefined ? 12 : 0);
  }
  if (h > 23 || min > 59) return null;
  return `${String(h).padStart(2, '0')}:${String(min).padStart(2, '0')}`;
}

/** Monday = 0. */
const weekdayOf = (date: string) => (new Date(`${date}T00:00:00Z`).getUTCDay() + 6) % 7;

/** The Monday after `today`. */
export const nextWeek = (today: string): string => addDays(today, 7 - weekdayOf(today));

/** The Saturday ahead; from a weekend, the next one. */
export function comingWeekend(today: string): string {
  const dow = weekdayOf(today);
  return addDays(today, dow < 5 ? 5 - dow : 12 - dow);
}

/** `date` plus `months`, on the same day of the month or its last day. */
function addMonths(date: string, months: number): string {
  const [y, m, d] = date.split('-').map(Number) as [number, number, number];
  const first = new Date(Date.UTC(y, m - 1 + months, 1));
  const last = new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth() + 1, 0)).getUTCDate();
  first.setUTCDate(Math.min(d, last));
  return first.toISOString().slice(0, 10);
}

const ymd = (y: number, m: number, d: number) => `${String(y).padStart(4, '0')}-${String(m).padStart(2, '0')}-${String(d).padStart(2, '0')}`;

interface DateMatch {
  end: number;
  due: string;
  /** A word that is only a date in the right company: a short weekday, a bare "weekend". */
  loose: boolean;
}

interface TimeMatch {
  end: number;
  time: string;
}

/**
 * The date and time typed in `text`, or `null` if there is none. What keeps
 * it from guessing:
 *
 * - a number alone is never a date or a time; a time needs a colon, am/pm or "at";
 * - a month needs a day;
 * - a short weekday ("sun") counts only at the end of the text, or beside "on", "next" or a time;
 * - a weekday is the next one after today, "next friday" the Friday of next week;
 * - a date without a year that has passed is next year's; a time without a date is today, or tomorrow once it has passed;
 * - of several dates, the last one wins.
 */
export function parseTask(text: string, { today, now, lang }: Now & { lang: Language }): Parsed | null {
  const tokens = [...text.matchAll(/\S+/g)].map((m) => ({ start: m.index, end: m.index + m[0].length, word: m[0].toLowerCase().replace(/[,;!?]+$/, '') }));
  const word = (i: number) => tokens[i]?.word ?? '';
  /** Whether token `i` is one of `words`, with or without a full stop after it. */
  const is = (i: number, words: string[]) => words.includes(word(i)) || words.includes(word(i).replace(/\.$/, ''));
  const indexIn = (i: number, table: string[][]) => table.findIndex((names) => is(i, names));
  const dow = weekdayOf(today);

  /** A day and month without a year: this year's, or next year's once it has passed. */
  function dayMonth(day: number, month: number, year?: number): string | null {
    const thisYear = Number(today.slice(0, 4));
    const date = ymd(year ?? thisYear, month, day);
    if (!isDateString(date)) return null;
    return year === undefined && date < today ? (isDateString(ymd(thisYear + 1, month, day)) ? ymd(thisYear + 1, month, day) : null) : date;
  }

  const dayNumber = new RegExp(`^(\\d{1,2})(?:${lang.ordinals.map(escape).join('|')})?$`);
  const dayAt = (i: number) => {
    const m = dayNumber.exec(word(i));
    return m === null ? null : Number(m[1]);
  };
  const yearAt = (i: number) => (/^20\d\d$/.test(word(i)) ? Number(word(i)) : undefined);

  function dateAt(start: number): DateMatch | null {
    const led = is(start, lang.on);
    const i = led ? start + 1 : start;
    const found = (end: number, due: string | null, loose = false): DateMatch | null => (due === null ? null : { end, due, loose: loose && !led });

    if (is(i, lang.today)) return found(i + 1, today);
    if (is(i, lang.tomorrow)) return found(i + 1, addDays(today, 1));

    if (is(i, lang.next)) {
      const day = indexIn(i + 1, lang.weekdays);
      if (day !== -1) return found(i + 2, addDays(nextWeek(today), day));
      if (is(i + 1, lang.units.week)) return found(i + 2, nextWeek(today));
      if (is(i + 1, lang.units.month)) return found(i + 2, `${addMonths(today, 1).slice(0, 8)}01`);
      if (is(i + 1, lang.weekend)) return found(i + 2, addDays(nextWeek(today), 5));
      return null;
    }
    const pointed = is(i, lang.this);
    const j = pointed ? i + 1 : i;
    const day = indexIn(j, lang.weekdays);
    if (day !== -1) return found(j + 1, addDays(today, ((day - dow + 6) % 7) + 1), !pointed && !is(j, [lang.weekdays[day]![0]!]));
    if (is(j, lang.weekend)) return found(j + 1, comingWeekend(today), !pointed);
    if (pointed) return null;

    if (is(i, lang.in)) {
      const n = is(i + 1, lang.one) ? 1 : /^\d{1,3}$/.test(word(i + 1)) ? Number(word(i + 1)) : null;
      if (n === null) return null;
      if (is(i + 2, lang.units.day)) return found(i + 3, addDays(today, n));
      if (is(i + 2, lang.units.week)) return found(i + 3, addDays(today, 7 * n));
      if (is(i + 2, lang.units.month)) return found(i + 3, addMonths(today, n));
      if (is(i + 2, lang.units.year)) return found(i + 3, addMonths(today, 12 * n));
      return null;
    }

    if (isDateString(word(i))) return found(i + 1, word(i));
    const slash = /^(\d{1,2})\/(\d{1,2})(?:\/(20\d\d))?$/.exec(word(i));
    // "24/7" is opening hours, not a day in July.
    if (slash !== null && word(i) !== '24/7') {
      const [a, b] = [Number(slash[1]), Number(slash[2])];
      return found(i + 1, dayMonth(lang.dayFirst ? a : b, lang.dayFirst ? b : a, slash[3] === undefined ? undefined : Number(slash[3])));
    }

    // "12 oct", "12th of october 2027", "oct 12".
    const first = dayAt(i);
    if (first !== null) {
      const at = is(i + 1, lang.of) ? i + 2 : i + 1;
      const month = indexIn(at, lang.months);
      if (month === -1) return null;
      const year = yearAt(at + 1);
      return found(year === undefined ? at + 1 : at + 2, dayMonth(first, month + 1, year));
    }
    const month = indexIn(i, lang.months);
    const second = month === -1 ? null : dayAt(i + 1);
    if (second !== null) {
      const year = yearAt(i + 2);
      return found(year === undefined ? i + 2 : i + 3, dayMonth(second, month + 1, year));
    }
    return null;
  }

  const halves = [...lang.am, ...lang.pm].filter((w) => w.length > 1);
  function timeAt(start: number): TimeMatch | null {
    const led = is(start, lang.at);
    const i = led ? start + 1 : start;
    const named = lang.times[word(i)];
    if (named !== undefined) return { end: i + 1, time: named };
    // "2 pm", in two words.
    const two = halves.includes(word(i + 1)) ? parseTime(`${word(i)} ${word(i + 1)}`, lang) : null;
    if (two !== null) return { end: i + 2, time: two };
    const one = parseTime(word(i), lang);
    // Without "at", only what can't be anything else: a colon, or am/pm in full.
    if (one === null || !(led || word(i).includes(':') || halves.some((h) => word(i).endsWith(h)))) return null;
    return { end: i + 1, time: one };
  }

  /** A date, a time, or both in either order, starting at token `i`. */
  function phraseAt(i: number): { end: number; due: string; time?: string } | null {
    let time = timeAt(i);
    const date = dateAt(time?.end ?? i);
    if (date !== null && time === null) time = timeAt(date.end);
    const end = Math.max(date?.end ?? 0, time?.end ?? 0);
    if (date === null) return time === null ? null : { end, due: time.time > now ? today : addDays(today, 1), time: time.time };
    if (date.loose && time === null && end !== tokens.length) return null;
    return { end, due: date.due, ...(time === null ? {} : { time: time.time }) };
  }

  let last: (NonNullable<ReturnType<typeof phraseAt>> & { start: number }) | null = null;
  for (let i = 0; i < tokens.length; ) {
    const p = phraseAt(i);
    if (p !== null) last = { ...p, start: i };
    i = p?.end ?? i + 1;
  }
  if (last === null) return null;

  const match: [number, number] = [tokens[last.start]!.start, tokens[last.end - 1]!.end];
  let title = `${text.slice(0, match[0])} ${text.slice(match[1])}`.replace(/\s+/g, ' ').trim().replace(/,$/, '');
  // "Tomorrow call mom" is saved as "Call mom".
  if (last.start === 0 && text[match[0]] !== text[match[0]]!.toLowerCase()) title = title.charAt(0).toUpperCase() + title.slice(1);
  return { title, due: last.due, ...(last.time === undefined ? {} : { time: last.time }), match };
}
