import { describe, expect, test } from 'vitest';
import { ApiError } from '../src/account/account.js';
import { logReason } from '../src/log-reason.js';

describe('logReason', () => {
  test('a failure says its code, never its message', () => {
    expect(logReason(new ApiError(403, 'forbidden', 'the list groceries is not yours'))).toBe('403 forbidden');
    expect(logReason(Object.assign(new Error('https://runer.test/join#secret'), { code: 'network' }))).toBe('network');
  });

  test('a code that is not one, or an error without one, comes down to its kind', () => {
    expect(logReason(Object.assign(new TypeError('buy milk'), { code: 'buy milk at the shop' }))).toBe('TypeError');
    expect(logReason(new SyntaxError('Unexpected token in "my secret title"'))).toBe('SyntaxError');
    expect(logReason('buy milk')).toBe('unknown');
    expect(logReason(null)).toBe('unknown');
  });

  test('our own fixed texts stay', () => {
    expect(logReason(new Error('sync did not settle'))).toBe('sync did not settle');
  });
});
