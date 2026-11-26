import * as Y from 'yjs';
import { afterAll, describe, expect, it, test } from 'vitest';
import { canonicalRule, firstDue, ListDoc, ListError, moveReminder, nextDue, parseRule } from '../src/list/index.js';

// Reminders keep their wall-clock time in the device's zone; this one has summer time.
const zone = process.env['TZ'];
process.env['TZ'] = 'Europe/Copenhagen';
afterAll(() => {
  if (zone === undefined) delete process.env['TZ'];
  else process.env['TZ'] = zone;
});

const local = (y: number, m: number, d: number, h: number, min = 0) => new Date(y, m - 1, d, h, min).getTime();
const clock = (at: number) => {
  const d = new Date(at);
  return [d.getHours(), d.getMinutes()];
};

describe('parseRule', () => {
  test.each([
    ['FREQ=DAILY', 'FREQ=DAILY'],
    ['FREQ=DAILY;INTERVAL=1', 'FREQ=DAILY'],
    ['INTERVAL=2;FREQ=WEEKLY', 'FREQ=WEEKLY;INTERVAL=2'],
    ['FREQ=WEEKLY;BYDAY=TH,MO', 'FREQ=WEEKLY;BYDAY=MO,TH'],
    ['FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR', 'FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR'],
    ['FREQ=MONTHLY;BYMONTHDAY=31', 'FREQ=MONTHLY;BYMONTHDAY=31'],
    ['FREQ=YEARLY;INTERVAL=999', 'FREQ=YEARLY;INTERVAL=999'],
  ])('%s', (s, canonical) => expect(canonicalRule(s)).toBe(canonical));

  test.each([
    '', 'DAILY', 'freq=daily', 'FREQ=HOURLY', 'FREQ=DAILY;FREQ=DAILY', 'FREQ=DAILY;INTERVAL=0', 'FREQ=DAILY;INTERVAL=1000',
    'FREQ=DAILY;INTERVAL=02', 'FREQ=DAILY;COUNT=3', 'FREQ=DAILY;UNTIL=20270101', 'FREQ=WEEKLY;BYDAY=1MO', 'FREQ=WEEKLY;BYDAY=MO,MO',
    'FREQ=WEEKLY;BYDAY=', 'FREQ=WEEKLY;WKST=SU', 'FREQ=MONTHLY;BYDAY=MO', 'FREQ=MONTHLY;BYMONTHDAY=-1', 'FREQ=MONTHLY;BYMONTHDAY=32',
    'FREQ=MONTHLY;BYMONTHDAY=1,15', 'FREQ=DAILY;BYMONTHDAY=1', 'FREQ=YEARLY;BYMONTH=3', 'FREQ=DAILY;', 'RRULE:FREQ=DAILY',
  ])('%s is not a rule', (s) => expect(parseRule(s)).toBeNull());

  it('is not a rule when not a string', () => {
    for (const v of [undefined, null, 7, ['FREQ=DAILY'], { freq: 'DAILY' }]) expect(parseRule(v)).toBeNull();
  });
});

describe('nextDue', () => {
  test.each([
    // Intervals.
    ['FREQ=DAILY', '2026-10-08', '2026-10-08', '2026-10-09'],
    ['FREQ=DAILY;INTERVAL=3', '2026-10-08', '2026-10-08', '2026-10-11'],
    ['FREQ=WEEKLY', '2026-10-08', '2026-10-08', '2026-10-15'],
    ['FREQ=WEEKLY;INTERVAL=2', '2026-10-08', '2026-10-08', '2026-10-22'],
    ['FREQ=MONTHLY;INTERVAL=3', '2026-10-08', '2026-10-08', '2027-01-08'],
    ['FREQ=YEARLY', '2026-10-08', '2026-10-08', '2027-10-08'],
    ['FREQ=YEARLY;INTERVAL=2', '2026-10-08', '2026-10-08', '2028-10-08'],
    // BYDAY sets: a Thursday due, Monday to Friday, Monday and Thursday, every second week.
    ['FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR', '2026-10-08', '2026-10-08', '2026-10-09'],
    ['FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR', '2026-10-09', '2026-10-09', '2026-10-12'],
    ['FREQ=WEEKLY;BYDAY=MO,TH', '2026-10-08', '2026-10-08', '2026-10-12'],
    ['FREQ=WEEKLY;BYDAY=MO,TH', '2026-10-12', '2026-10-12', '2026-10-15'],
    ['FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TH', '2026-10-08', '2026-10-08', '2026-10-19'],
    ['FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TH', '2026-10-05', '2026-10-05', '2026-10-08'],
    ['FREQ=WEEKLY;BYDAY=SU', '2026-10-11', '2026-10-11', '2026-10-18'],
    // Month ends: the month's last day, and back to the 31st after it.
    ['FREQ=MONTHLY;BYMONTHDAY=31', '2027-01-31', '2027-01-31', '2027-02-28'],
    ['FREQ=MONTHLY;BYMONTHDAY=31', '2027-02-28', '2027-02-28', '2027-03-31'],
    ['FREQ=MONTHLY;BYMONTHDAY=31', '2026-03-31', '2026-03-31', '2026-04-30'],
    ['FREQ=MONTHLY;BYMONTHDAY=15', '2026-10-08', '2026-10-08', '2026-10-15'],
    ['FREQ=MONTHLY', '2026-12-15', '2026-12-15', '2027-01-15'],
    ['FREQ=MONTHLY', '2027-01-31', '2027-01-31', '2027-02-28'],
    // Leap years.
    ['FREQ=MONTHLY;BYMONTHDAY=29', '2028-01-29', '2028-01-29', '2028-02-29'],
    ['FREQ=MONTHLY;BYMONTHDAY=30', '2028-01-30', '2028-01-30', '2028-02-29'],
    ['FREQ=MONTHLY;BYMONTHDAY=29', '2027-01-29', '2027-01-29', '2027-02-28'],
    ['FREQ=YEARLY', '2028-02-29', '2028-02-29', '2029-02-28'],
    ['FREQ=YEARLY;INTERVAL=4', '2028-02-29', '2028-02-29', '2032-02-29'],
    ['FREQ=DAILY', '2028-02-28', '2028-02-28', '2028-02-29'],
    // Missed dates are skipped, counted from the old date, not from today.
    ['FREQ=DAILY', '2026-10-01', '2026-10-08', '2026-10-09'],
    ['FREQ=DAILY;INTERVAL=3', '2026-10-01', '2026-10-08', '2026-10-10'],
    ['FREQ=WEEKLY', '2026-09-03', '2026-10-08', '2026-10-15'],
    ['FREQ=WEEKLY', '2026-09-03', '2026-10-07', '2026-10-08'],
    ['FREQ=WEEKLY;INTERVAL=2', '2026-09-03', '2026-10-08', '2026-10-15'],
    ['FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TH', '2026-09-03', '2026-10-06', '2026-10-12'],
    ['FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TH', '2026-09-03', '2026-10-12', '2026-10-15'],
    ['FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TH', '2026-09-03', '2026-10-15', '2026-10-26'],
    ['FREQ=MONTHLY;BYMONTHDAY=15', '2026-01-15', '2026-10-08', '2026-10-15'],
    ['FREQ=MONTHLY;BYMONTHDAY=15', '2026-01-15', '2026-10-15', '2026-11-15'],
    ['FREQ=MONTHLY;INTERVAL=2;BYMONTHDAY=15', '2026-01-15', '2026-10-20', '2026-11-15'],
    ['FREQ=YEARLY', '2020-03-01', '2026-10-08', '2027-03-01'],
    // A date ahead of today counts from that date.
    ['FREQ=DAILY', '2026-12-24', '2026-10-08', '2026-12-25'],
  ])('%s from %s on %s is %s', (rule, due, today, next) => expect(nextDue(rule, due, today)).toBe(next));

  it('refuses what it cannot count', () => {
    expect(() => nextDue('FREQ=HOURLY', '2026-10-08', '2026-10-08')).toThrow(RangeError);
    expect(() => nextDue('FREQ=DAILY', '2026-02-30', '2026-10-08')).toThrow(RangeError);
  });
});

describe('firstDue', () => {
  test.each([
    ['FREQ=DAILY', '2026-10-08', '2026-10-08'],
    ['FREQ=WEEKLY', '2026-10-08', '2026-10-08'],
    ['FREQ=WEEKLY;BYDAY=TH', '2026-10-08', '2026-10-08'],
    ['FREQ=WEEKLY;BYDAY=MO', '2026-10-08', '2026-10-12'],
    ['FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR', '2026-10-10', '2026-10-12'],
    ['FREQ=MONTHLY;BYMONTHDAY=15', '2026-10-08', '2026-10-15'],
    ['FREQ=MONTHLY;BYMONTHDAY=1', '2026-10-08', '2026-11-01'],
    ['FREQ=MONTHLY;BYMONTHDAY=31', '2026-11-30', '2026-11-30'],
  ])('%s from %s is %s', (rule, from, first) => expect(firstDue(rule, from)).toBe(first));
});

describe('moveReminder', () => {
  it('keeps the wall-clock time across a change to and from summer time', () => {
    // Summer time ends on 25 October 2026 and starts on 28 March 2027 in Copenhagen.
    const autumn = moveReminder(local(2026, 10, 24, 9, 30), '2026-10-26');
    expect(new Date(autumn).getDate()).toBe(26);
    expect(clock(autumn)).toEqual([9, 30]);
    expect(autumn - local(2026, 10, 24, 9, 30)).toBe(49 * 3600_000);
    const spring = moveReminder(local(2027, 3, 27, 9, 30), '2027-03-29');
    expect(clock(spring)).toEqual([9, 30]);
    expect(spring - local(2027, 3, 27, 9, 30)).toBe(47 * 3600_000);
  });
});

describe('a repeating task', () => {
  let today = '2026-10-08';
  const mk = () => new ListDoc(new Y.Doc(), { today: () => today });

  it('moves on to the next date when ticked, and stays not done', () => {
    today = '2026-10-08';
    const l = mk();
    const id = l.addTask('Water plants', { due: '2026-10-08', repeat: 'FREQ=WEEKLY;BYDAY=MO,TH' });
    expect(l.item(id)).toMatchObject({ due: '2026-10-08', repeat: 'FREQ=WEEKLY;BYDAY=MO,TH', done: false });
    expect(l.toggleDone(id)).toEqual({ done: false, next: '2026-10-12' });
    expect(l.item(id)).toMatchObject({ due: '2026-10-12', done: false, title: 'Water plants' });
    expect(l.toggleDone(id, true)).toEqual({ done: false, next: '2026-10-15' });
    // Not done already: nothing to undo.
    expect(l.toggleDone(id, false)).toEqual({ done: false });
    expect(l.item(id)?.due).toBe('2026-10-15');
  });

  it('skips the dates it missed', () => {
    today = '2026-10-08';
    const l = mk();
    const id = l.addTask('Stand-up', { due: '2026-09-28', repeat: 'FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR' });
    expect(l.toggleDone(id)).toEqual({ done: false, next: '2026-10-09' });
  });

  it('keeps its reminder at the same time of day across summer time', () => {
    today = '2026-10-24';
    const l = mk();
    const id = l.addTask('Pills', { due: '2026-10-24', remindAt: local(2026, 10, 24, 8, 0), repeat: 'FREQ=DAILY' });
    l.toggleDone(id);
    l.toggleDone(id);
    const t = l.item(id)!;
    expect(t.due).toBe('2026-10-26');
    expect(new Date(t.remindAt!).getDate()).toBe(26);
    expect(clock(t.remindAt!)).toEqual([8, 0]);
  });

  it('gets a date when it had none, and moves to a date the rule names', () => {
    today = '2026-10-08';
    const l = mk();
    const undated = l.addTask('Rent', { repeat: 'FREQ=MONTHLY;BYMONTHDAY=1' });
    expect(l.item(undated)).toMatchObject({ due: '2026-11-01', repeat: 'FREQ=MONTHLY;BYMONTHDAY=1' });
    const dated = l.addTask('Gym', { due: '2026-10-08', remindAt: local(2026, 10, 8, 18, 0) });
    expect(l.setRepeat(dated, 'FREQ=WEEKLY;BYDAY=MO')).toBe(true);
    const gym = l.item(dated)!;
    expect(gym.due).toBe('2026-10-12');
    expect(new Date(gym.remindAt!).getDate()).toBe(12);
    expect(clock(gym.remindAt!)).toEqual([18, 0]);
  });

  it('keeps a monthly date on its day: the 31st stays the last day of the month', () => {
    today = '2027-01-01';
    const l = mk();
    const id = l.addTask('Invoice', { due: '2027-01-31' });
    l.setRepeat(id, 'FREQ=MONTHLY');
    expect(l.item(id)?.repeat).toBe('FREQ=MONTHLY;BYMONTHDAY=31');
    const dates = [1, 2, 3].map(() => (l.toggleDone(id) as { next: string }).next);
    expect(dates).toEqual(['2027-02-28', '2027-03-31', '2027-04-30']);
  });

  it('stops repeating, and stops when its date is cleared', () => {
    today = '2026-10-08';
    const l = mk();
    const id = l.addTask('Run', { due: '2026-10-08', repeat: 'FREQ=DAILY' });
    expect(l.setRepeat(id, null)).toBe(true);
    expect(l.item(id)?.repeat).toBeUndefined();
    expect(l.toggleDone(id)).toEqual({ done: true });
    const again = l.addTask('Run', { due: '2026-10-08', repeat: 'FREQ=DAILY' });
    l.setDue(again, null);
    expect(l.item(again)).not.toHaveProperty('repeat');
    expect(l.item(again)).not.toHaveProperty('due');
  });

  it('refuses an unsupported rule on write', () => {
    const l = mk();
    const id = l.addTask('Run', { due: '2026-10-08' });
    for (const rule of ['FREQ=DAILY;COUNT=3', 'FREQ=MONTHLY;BYDAY=1MO', 'daily']) {
      expect(() => l.setRepeat(id, rule)).toThrow(ListError);
      expect(() => l.addTask('Run', { repeat: rule })).toThrow(ListError);
    }
    expect(l.setRepeat(l.addNote('a note'), 'FREQ=DAILY')).toBe(false);
    expect(l.items().filter((i) => i.type === 'task')).toHaveLength(1);
  });

  it('ignores an unsupported rule, one without a date, and one on a done task on read', () => {
    today = '2026-10-08';
    const doc = new Y.Doc();
    const items = doc.getMap<Y.Map<unknown>>('items');
    const task = (id: string, fields: Record<string, unknown>) => {
      const m = new Y.Map<unknown>();
      for (const [k, v] of Object.entries({ type: 'task', title: id, done: false, ...fields })) m.set(k, v);
      items.set(id, m);
    };
    task('unsupported', { due: '2026-10-08', repeat: 'FREQ=MONTHLY;BYDAY=1MO' });
    task('undated', { repeat: 'FREQ=DAILY' });
    task('done', { due: '2026-10-08', repeat: 'FREQ=DAILY', done: true });
    task('number', { due: '2026-10-08', repeat: 7 });
    const l = new ListDoc(doc, { today: () => today });
    for (const id of ['unsupported', 'undated', 'done', 'number']) expect(l.item(id)).not.toHaveProperty('repeat');
    // Shown without a repeat, so it ticks as a plain task.
    expect(l.toggleDone('unsupported')).toEqual({ done: true });
    expect(l.toggleDone('done')).toEqual({ done: false });
    expect(l.item('done')?.repeat).toBe('FREQ=DAILY');
  });

  it('converges when two devices tick it offline', () => {
    today = '2026-10-08';
    const a = new ListDoc(new Y.Doc(), { today: () => today });
    const b = new ListDoc(new Y.Doc(), { today: () => today });
    const sync = () => {
      Y.applyUpdate(b.doc, Y.encodeStateAsUpdate(a.doc));
      Y.applyUpdate(a.doc, Y.encodeStateAsUpdate(b.doc));
    };
    const id = a.addTask('Water plants', { due: '2026-10-08', remindAt: local(2026, 10, 8, 9, 0), repeat: 'FREQ=WEEKLY;BYDAY=MO,TH' });
    sync();
    expect(a.toggleDone(id)).toEqual({ done: false, next: '2026-10-12' });
    expect(b.toggleDone(id)).toEqual({ done: false, next: '2026-10-12' });
    sync();
    for (const d of [a, b]) {
      expect(d.item(id)).toMatchObject({ due: '2026-10-12', done: false, remindAt: local(2026, 10, 12, 9, 0) });
    }
    expect(a.item(id)).toEqual(b.item(id));
  });
});
