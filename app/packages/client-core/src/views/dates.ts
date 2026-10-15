import { isDateString } from '../list/item.js';

/** The device's local calendar date as `YYYY-MM-DD`. */
export function localDateString(d: Date): string {
  const y = d.getFullYear().toString().padStart(4, '0');
  const m = (d.getMonth() + 1).toString().padStart(2, '0');
  const day = d.getDate().toString().padStart(2, '0');
  return `${y}-${m}-${day}`;
}

/** `date` plus `days` calendar days (negative allowed). */
export function addDays(date: string, days: number): string {
  if (!isDateString(date)) throw new RangeError(`not a YYYY-MM-DD date: ${date}`);
  const [y, m, d] = date.split('-').map(Number) as [number, number, number];
  return new Date(Date.UTC(y, m - 1, d + days)).toISOString().slice(0, 10);
}
