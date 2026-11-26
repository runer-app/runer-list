import { describe, expect, test } from 'vitest';
import { parseTime, readTyped, repeatLabel } from '../src/dates.js';

// The tables of what's understood are in client-core (`test/nl.test.ts`); this is the device's clock around them.
describe('readTyped', () => {
  const thursday = new Date(2026, 9, 8, 10, 0);

  test('a date and a time become a due date and a reminder', () => {
    expect(readTyped('Call mom tomorrow 9:30', thursday)).toEqual({
      title: 'Call mom',
      due: '2026-10-09',
      remindAt: new Date(2026, 9, 9, 9, 30).getTime(),
      label: expect.stringMatching(/^Tomorrow /),
    });
  });

  test('a date alone has no reminder', () => {
    expect(readTyped('Dentist 12 oct', thursday)).toEqual({ title: 'Dentist', due: '2026-10-12', label: 'Monday' });
  });

  test('a day and month with a slash is day first', () => {
    expect(readTyped('Dentist 12/10', thursday)).toEqual({ title: 'Dentist', due: '2026-10-12', label: 'Monday' });
  });

  test('a time that has passed is tomorrow’s', () => {
    expect(readTyped('Call mom at 9am', thursday)?.due).toBe('2026-10-09');
  });

  test('plain text is left alone', () => expect(readTyped('Buy 2 milk', thursday)).toBeNull());
});

test('parseTime reads a time field', () => {
  expect(parseTime('2:30 pm')).toBe('14:30');
  expect(parseTime('noon')).toBeNull();
});

describe('repeatLabel', () => {
  test.each([
    ['FREQ=DAILY', 'Every day'],
    ['FREQ=DAILY;INTERVAL=3', 'Every 3 days'],
    ['FREQ=WEEKLY', 'Every week'],
    ['FREQ=WEEKLY;INTERVAL=2', 'Every 2 weeks'],
    ['FREQ=WEEKLY;BYDAY=MO', 'Every Monday'],
    ['FREQ=WEEKLY;BYDAY=MO,TH', 'Every Mon, Thu'],
    ['FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR', 'Every weekday'],
    ['FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TU,WE,TH,FR', 'Every 2 weeks on Mon, Tue, Wed, Thu, Fri'],
    ['FREQ=MONTHLY', 'Every month'],
    ['FREQ=MONTHLY;BYMONTHDAY=1', 'Every month on the 1st'],
    ['FREQ=MONTHLY;BYMONTHDAY=22', 'Every month on the 22nd'],
    ['FREQ=MONTHLY;BYMONTHDAY=13', 'Every month on the 13th'],
    ['FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=31', 'Every 3 months on the 31st'],
    ['FREQ=YEARLY', 'Every year'],
    ['FREQ=YEARLY;INTERVAL=2', 'Every 2 years'],
  ])('%s is %s', (rule, label) => expect(repeatLabel(rule)).toBe(label));

  test('no label for no rule, or one this app does not support', () => {
    expect(repeatLabel(undefined)).toBeUndefined();
    expect(repeatLabel('FREQ=DAILY;COUNT=2')).toBeUndefined();
  });
});

describe('readTyped with a repeat', () => {
  test('the rule, its first date and the time, said in the chip', () => {
    const thursday = new Date(2026, 9, 8, 10, 0);
    expect(readTyped('Gym every monday at 9', thursday)).toEqual({
      title: 'Gym',
      due: '2026-10-12',
      remindAt: new Date(2026, 9, 12, 9, 0).getTime(),
      repeat: 'FREQ=WEEKLY;BYDAY=MO',
      label: expect.stringMatching(/^Every Monday · Monday (09:00|9:00 AM)$/),
    });
  });
});
