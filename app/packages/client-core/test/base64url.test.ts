import { randomBytes } from 'node:crypto';
import { expect, test } from 'vitest';
import { fromBase64url, toBase64url } from '../src/base64url.js';

test('matches Node for every length and refuses bad input', () => {
  for (let n = 0; n < 40; n++) {
    const b = new Uint8Array(randomBytes(n));
    const s = Buffer.from(b).toString('base64url');
    expect(toBase64url(b)).toBe(s);
    expect(fromBase64url(s)).toEqual(b);
  }
  expect(() => fromBase64url('ab+c')).toThrow();
  expect(() => fromBase64url('abcde')).toThrow();
});
