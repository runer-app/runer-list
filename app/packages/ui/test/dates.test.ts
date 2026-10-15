import { describe, expect, test } from 'vitest';
import { parseTime, readTyped } from '../src/dates.js';

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

  test('a time that has passed is tomorrow’s', () => {
    expect(readTyped('Call mom at 9am', thursday)?.due).toBe('2026-10-09');
  });

  test('plain text is left alone', () => expect(readTyped('Buy 2 milk', thursday)).toBeNull());
});

test('parseTime reads a time field', () => {
  expect(parseTime('2:30 pm')).toBe('14:30');
  expect(parseTime('noon')).toBeNull();
});
