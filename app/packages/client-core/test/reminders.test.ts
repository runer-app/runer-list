import { describe, expect, it } from 'vitest';
import { GENERIC_REMINDER_TITLE, planReminders, type Item, type ListSnapshot } from '../src/index.js';
import { createMemoryPlatform, RecordingNotifier } from './platform-node/memory.js';

const NOW = 1_800_000_000_000;
const MIN = 60_000;
const task = (id: string, title: string, extra: Partial<Item> = {}): Item => ({ id, type: 'task', title, done: false, ...extra });
const list = (id: string, name: string, items: Item[]): ListSnapshot => ({ id, name, items });
const base = { me: 'me', now: NOW, maxPending: 64 };

describe('planReminders', () => {
  const lists = [
    list('L1', 'Home', [
      task('a', 'Soon', { remindAt: NOW + 5 * MIN }),
      task('b', 'Past', { remindAt: NOW - MIN }),
      task('c', 'Now exactly', { remindAt: NOW }),
      task('d', 'Done', { remindAt: NOW + MIN, done: true }),
      task('e', 'Not mine', { remindAt: NOW + MIN, assignee: 'anna' }),
      task('f', 'Assigned to me', { remindAt: NOW + 2 * MIN, assignee: 'me' }),
      task('g', 'No reminder'),
      { id: 'n', type: 'note', title: 'note', done: false, remindAt: NOW + MIN },
    ]),
  ];

  it('plans future reminders for open tasks that are the users, soonest first', () => {
    const plan = planReminders({ ...base, lists });
    expect(plan.map((r) => r.title)).toEqual(['Assigned to me', 'Soon']);
    expect(plan[0]).toEqual({ id: 'L1:f', at: new Date(NOW + 2 * MIN), title: 'Assigned to me', body: 'Home' });
  });

  it('keeps only the nearest maxPending', () => {
    const many = list('L', 'x', Array.from({ length: 100 }, (_, i) => task(`t${i}`, `T${i}`, { remindAt: NOW + (100 - i) * MIN })));
    const plan = planReminders({ ...base, lists: [many], maxPending: 64 });
    expect(plan).toHaveLength(64);
    expect(plan[0]!.title).toBe('T99');
    expect(plan[63]!.title).toBe('T36');
    expect(planReminders({ ...base, lists: [many], maxPending: 0 })).toEqual([]);
  });

  it('hides titles and list names when the privacy setting is on', () => {
    const [r] = planReminders({ ...base, lists, showTitle: false });
    expect(r).toEqual({ id: 'L1:f', at: new Date(NOW + 2 * MIN), title: GENERIC_REMINDER_TITLE });
    expect(planReminders({ ...base, lists, showTitle: false, genericTitle: 'Påmindelse' })[0]!.title).toBe('Påmindelse');
  });

  it('plans one reminder for a task an interrupted move left in two lists', () => {
    const dup = [list('B', 'Groceries', [task('a', 'Milk', { remindAt: NOW + MIN })]), list('A', 'Inbox', [task('a', 'Milk', { remindAt: NOW + MIN })])];
    expect(planReminders({ ...base, lists: dup }).map((r) => r.id)).toEqual(['A:a']);
  });

  it('feeds the notifier, which keeps at most maxPending', async () => {
    const notifier = new RecordingNotifier(2);
    const p = createMemoryPlatform({ notifications: notifier });
    const many = list('L', 'x', [task('a', 'A', { remindAt: NOW + MIN }), task('b', 'B', { remindAt: NOW + 2 * MIN }), task('c', 'C', { remindAt: NOW + 3 * MIN })]);
    await p.notifications.replaceAll(planReminders({ ...base, lists: [many], maxPending: p.notifications.maxPending }));
    expect(notifier.scheduled.map((r) => r.title)).toEqual(['A', 'B']);
  });
});
