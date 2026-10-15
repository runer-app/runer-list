import { afterEach, describe, expect, test, vi } from 'vitest';
import { formatInviteLink, parseInviteLink } from '../src/invite/link.js';

const secret = new Uint8Array(32).map((_, i) => i * 7);
const s = Buffer.from(secret).toString('base64url');

afterEach(() => vi.restoreAllMocks());

describe('invite links (client-spec §6.3)', () => {
  test('the link is the list server’s /join with the secret in the fragment', () => {
    expect(formatInviteLink('https://runer.test/', secret)).toBe(`https://runer.test/join#${s}`);
    expect(parseInviteLink(formatInviteLink('https://runer.test', secret))).toEqual({ server: 'https://runer.test', secret });
    expect(parseInviteLink(`http://localhost:8080/api/join#${s}`)).toEqual({ server: 'http://localhost:8080/api', secret });
    expect(parseInviteLink(`  https://runer.test/join#${s}\n`)).toEqual({ server: 'https://runer.test', secret });
  });

  test('the app’s own runer://join link names the server in host', () => {
    expect(parseInviteLink(`runer://join?host=runer.test#${s}`)).toEqual({ server: 'https://runer.test', secret });
    expect(parseInviteLink(`runer://join?host=${encodeURIComponent('http://localhost:8080')}#${s}`)).toEqual({ server: 'http://localhost:8080', secret });
    expect(parseInviteLink(`runer://join#${s}`)).toBeNull();
    expect(parseInviteLink(`runer://other?host=runer.test#${s}`)).toBeNull();
  });

  test('anything else is not a link', () => {
    for (const bad of [
      'not a url',
      'https://runer.test/join',
      `https://runer.test/join#${s}A`,
      `https://runer.test/join#${s.slice(1)}`,
      `https://runer.test/join#${s.slice(0, -1)}+`,
      `https://runer.test/other#${s}`,
      `ftp://runer.test/join#${s}`,
      `runer://join?host=${encodeURIComponent('https://user:pw@runer.test')}#${s}`,
    ]) {
      expect(parseInviteLink(bad), bad).toBeNull();
    }
  });

  test('parsing never logs: the fragment is a key', () => {
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => vi.spyOn(console, m));
    parseInviteLink(`https://runer.test/join#${s}`);
    parseInviteLink(`https://runer.test/join#${s}x`);
    parseInviteLink(`runer://join?host=%%%#${s}`);
    for (const spy of spies) expect(spy).not.toHaveBeenCalled();
  });
});
