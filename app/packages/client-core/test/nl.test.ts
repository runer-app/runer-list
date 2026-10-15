import { describe, expect, test } from 'vitest';
import { comingWeekend, en, nextWeek, parseTask, parseTime } from '../src/index.js';

const at = { ...en.examplesAt, lang: en };

describe('parseTask, English', () => {
  test.each(en.examples)('$text', ({ text, title, due, time }) => {
    const p = parseTask(text, at);
    expect(p).toMatchObject({ title, due });
    expect(p?.time).toBe(time);
  });

  test.each(en.plain)('%s holds no date', (text) => expect(parseTask(text, at)).toBeNull());

  test('says where the words were', () => {
    const text = 'Call mom on friday at 2 pm, please';
    const p = parseTask(text, at)!;
    expect(text.slice(...p.match)).toBe('on friday at 2 pm,');
    expect(p).toMatchObject({ title: 'Call mom please', due: '2026-10-09', time: '14:00' });
  });

  test.each([
    ['Move friday meeting to monday', 'Move friday meeting to', '2026-10-12'], // the last date wins
    ['Call mom sun 9:30', 'Call mom', '2026-10-11'], // a short weekday beside a time
    ['Party 31 feb', null, null], // not a day
    ['Leap 29 feb', null, null], // not next year either
    ['Tax 1/2/2027', 'Tax', '2027-02-01'],
    ['tomorrow', '', '2026-10-09'], // no title left: for the caller to refuse
  ])('%s', (text, title, due) => {
    const p = parseTask(text, at);
    if (title === null) expect(p).toBeNull();
    else expect(p).toMatchObject({ title, due });
  });

  test('month ends and weekends', () => {
    expect(parseTask('Pay in a month', { today: '2027-01-31', now: '10:00', lang: en })?.due).toBe('2027-02-28');
    expect(parseTask('Pay next month', { today: '2026-12-15', now: '10:00', lang: en })?.due).toBe('2027-01-01');
    expect(nextWeek('2026-10-12')).toBe('2026-10-19');
    expect(comingWeekend('2026-10-10')).toBe('2026-10-17');
  });
});

describe('parseTime', () => {
  test.each([
    ['14:30', '14:30'],
    ['1430', '14:30'],
    ['930', '09:30'],
    ['9', '09:00'],
    ['9.15', '09:15'],
    ['2pm', '14:00'],
    ['2p', '14:00'],
    ['2:30 PM', '14:30'],
    ['12am', '00:00'],
    ['12 p.m.', '12:00'],
  ])('%s is %s', (typed, hm) => expect(parseTime(typed, en)).toBe(hm));

  test.each(['', '24:00', '9:60', '13pm', '0am', 'noon', '1:5'])('%s is not a time', (typed) => expect(parseTime(typed, en)).toBeNull());
});
