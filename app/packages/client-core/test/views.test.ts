import { describe, expect, it } from 'vitest';
import type { Item, ListSnapshot } from '../src/list/index.js';
import { addDays, inOrder, localDateString, myTasksView, openTaskCount, searchLists, todayView } from '../src/index.js';

const task = (id: string, title: string, extra: Partial<Item> = {}): Item => ({ id, type: 'task', title, done: false, ...extra });
const list = (id: string, name: string, items: Item[]): ListSnapshot => ({ id, name, items });
const ctx = { me: 'me', today: '2026-10-01' };

describe('dates', () => {
  it('formats local dates and does calendar arithmetic', () => {
    expect(localDateString(new Date(2026, 0, 5))).toBe('2026-01-05');
    expect(addDays('2026-10-01', 7)).toBe('2026-10-08');
    expect(addDays('2026-12-31', 1)).toBe('2027-01-01');
    expect(addDays('2026-03-01', -1)).toBe('2026-02-28');
    expect(addDays('2028-02-28', 1)).toBe('2028-02-29');
    expect(() => addDays('bad', 1)).toThrow(RangeError);
  });
});

describe('todayView', () => {
  const lists = [
    list('L2', 'Work', [task('t1', 'Report', { due: '2026-10-01' }), task('t2', 'Old', { due: '2026-09-28' }), task('t3', 'Later', { due: '2026-10-09' })]),
    list('L1', 'Home', [
      task('t4', 'Milk', { due: '2026-10-01' }),
      task('t5', 'Done already', { due: '2026-10-01', done: true }),
      task('t6', "Anna's", { due: '2026-10-01', assignee: 'anna' }),
      task('t7', 'Mine', { due: '2026-10-01', assignee: 'me' }),
      task('t8', 'No date'),
      { id: 'n1', type: 'note', title: 'a note', done: false, due: '2026-10-01' },
    ]),
  ];

  it('lists the users open tasks due today or earlier, overdue first, with their list', () => {
    const out = todayView(lists, ctx);
    expect(out.map((t) => t.item.id)).toEqual(['t2', 't4', 't7', 't1']);
    expect(out[0]).toMatchObject({ overdue: true, listName: 'Work' });
    expect(out[1]).toMatchObject({ overdue: false, listName: 'Home' });
  });

  it('includes unassigned tasks and ones assigned to me, but not someone elses', () => {
    const ids = todayView(lists, ctx).map((t) => t.item.id);
    expect(ids).toContain('t4');
    expect(ids).toContain('t7');
    expect(ids).not.toContain('t6');
  });

  it('shows a task once when an interrupted move left it in two lists', () => {
    const dup = [list('B', 'Groceries', [task('t1', 'Milk', { due: '2026-10-01' })]), list('A', 'Inbox', [task('t1', 'Milk', { due: '2026-10-01' })])];
    const out = todayView(dup, ctx);
    expect(out).toHaveLength(1);
    expect(out[0]!.listId).toBe('A');
  });
});

describe('myTasksView', () => {
  const lists = [
    list('L', 'Home', [
      task('a', 'today', { due: '2026-10-01' }),
      task('b', 'tomorrow', { due: '2026-10-02' }),
      task('c', 'day 7', { due: '2026-10-08' }),
      task('d', 'day 8', { due: '2026-10-09' }),
      task('e', 'overdue', { due: '2026-09-30' }),
      task('f', 'undated'),
    ]),
  ];

  it('splits into today, upcoming (next 7 days) and planned (later)', () => {
    const v = myTasksView(lists, ctx);
    expect(v.today.map((t) => t.item.id)).toEqual(['e', 'a']);
    expect(v.upcoming.map((t) => t.item.id)).toEqual(['b', 'c']);
    expect(v.planned.map((t) => t.item.id)).toEqual(['d']);
  });

  it('honours a different horizon', () => {
    const v = myTasksView(lists, { ...ctx, upcomingDays: 1 });
    expect(v.upcoming.map((t) => t.item.id)).toEqual(['b']);
    expect(v.planned.map((t) => t.item.id)).toEqual(['c', 'd']);
  });
});

describe('inOrder', () => {
  const ref = (id: string) => ({ listId: 'l', listName: 'L', index: 0, overdue: false, item: { id, type: 'task', title: id, done: false } });
  it('puts the tasks the user placed in their order, then the rest as they were', () => {
    const refs = ['a', 'b', 'c', 'd'].map(ref);
    expect(inOrder(refs, ['c', 'x', 'a']).map((r) => r.item.id)).toEqual(['c', 'a', 'b', 'd']);
    expect(inOrder(refs, []).map((r) => r.item.id)).toEqual(['a', 'b', 'c', 'd']);
  });
});

describe('openTaskCount', () => {
  it('counts open tasks only', () => {
    const l = list('L', 'x', [task('a', 'a'), task('b', 'b', { done: true }), { id: 'n', type: 'note', title: 'n', done: false }]);
    expect(openTaskCount(l)).toBe(1);
  });
});

describe('searchLists', () => {
  const lists = [
    list('L1', 'Trip to Rome', [task('a', 'Book flights'), task('b', 'Café near the Pantheon', { body: 'reserve a table' }), task('c', 'Pack', { done: true, body: 'flights leave early' })]),
    list('L2', 'Home', [{ id: 'n', type: 'note', title: 'Rome notes', done: false }]),
  ];

  it('needs every word, in any order, ignoring case and accents', () => {
    expect(searchLists(lists, 'cafe pantheon').map((h) => h.item?.id)).toEqual(['b']);
    expect(searchLists(lists, 'FLIGHTS book').map((h) => h.item?.id)).toEqual(['a']);
    expect(searchLists(lists, 'zzz')).toEqual([]);
    expect(searchLists(lists, '   ')).toEqual([]);
  });

  it('matches bodies, but ranks titles first and open tasks above done ones', () => {
    const hits = searchLists(lists, 'flights');
    expect(hits.map((h) => [h.item?.id, h.field])).toEqual([['a', 'title'], ['c', 'body']]);
  });

  it('finds lists by name', () => {
    const hits = searchLists(lists, 'rome');
    expect(hits[0]).toMatchObject({ field: 'name', listId: 'L1' });
    expect(hits.map((h) => h.item?.id ?? h.listId)).toEqual(['L1', 'n']);
  });

  it('respects the limit', () => {
    expect(searchLists(lists, 'o', 2)).toHaveLength(2);
  });
});
