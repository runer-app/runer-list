import { Account, ApiError, fromBase64url, type Session } from '@runer/client-core';
import { describe, expect, test } from 'vitest';
import { device, restart, server } from './device.js';

const signUp = () => Account.signUp(device(), server.url);

async function signIn(words: string[]): Promise<Account> {
  const a = await Account.signIn(device(), server.url, words.join(' '));
  expect(a).not.toBeNull();
  return a!;
}

async function rejection(p: Promise<unknown>): Promise<ApiError> {
  const e = await p.then(
    () => undefined,
    (e: unknown) => e,
  );
  expect(e).toBeInstanceOf(ApiError);
  return e as ApiError;
}

const oldest = (sessions: Session[]) => sessions.reduce((x, y) => (x.createdAt <= y.createdAt ? x : y));

/** The identity the server has on file for the account. */
async function serverIdentity(a: Account): Promise<Uint8Array> {
  return fromBase64url((await a.request<{ identity_sign_pub: string }>('GET', '/v1/account')).identity_sign_pub);
}

describe('accounts (E2EE §3, §9)', () => {
  test('sign up, then sign in on a second device with the words', async () => {
    const { account: a, words } = await signUp();
    expect(words).toHaveLength(12);
    expect(await serverIdentity(a)).toEqual(a.identity.signPub);

    const b = await signIn(words);
    expect(b.identity).toEqual(a.identity);
    expect(await b.showWords()).toEqual(words);
    expect(await a.sessions()).toHaveLength(2);
  });

  test('the name: optional at sign-up, kept sealed in the settings and never in plain text, there on a second device, changed and cleared', async () => {
    const { account: a, words } = await Account.signUp(device(), server.url, ' Anna Åberg ');
    expect(a.name.value).toBe('Anna Åberg');
    // The server has no personal information about the account: no name, no email, only a blob it can't read.
    const kept = await a.request<{ settings_sealed: string; name?: unknown; email?: unknown }>('GET', '/v1/account');
    expect([kept.name, kept.email]).toEqual([undefined, undefined]);
    expect(Buffer.from(kept.settings_sealed, 'base64url').includes('Anna')).toBe(false);
    expect((await rejection(a.request('PUT', '/v1/account/name', { name: 'Anna' }))).status).toBe(404);
    expect((await rejection(a.request('PUT', '/v1/account/email', { email: 'anna@example.test' }))).status).toBe(404);

    const b = await signIn(words);
    expect(b.name.value).toBe('Anna Åberg');

    // No push: the first device hears of a change when it next asks.
    await b.setName('Anna B');
    await b.syncSettings();
    await a.syncSettings();
    expect(a.name.value).toBe('Anna B');
    await a.setName('');
    await a.syncSettings();
    await b.syncSettings();
    expect(b.name.value).toBe('');

    // An account needs no name at all.
    const { account: nameless, words: theirs } = await signUp();
    expect(nameless.name.value).toBe('');
    expect((await nameless.request<{ settings_sealed: string | null }>('GET', '/v1/account')).settings_sealed ?? null).toBeNull();
    expect((await signIn(theirs)).name.value).toBe('');
  });

  test('the account settings: opaque to the server, there on a second device, and a stale write is refused', async () => {
    const { account: a, words } = await signUp();
    const onServer = () => a.request<{ settings_sealed: string | null; settings_gen: number }>('GET', '/v1/account');
    expect(await onServer()).toMatchObject({ settings_sealed: null, settings_gen: 0 });

    await a.changeSettings({ clock: '24h' });
    await a.syncSettings();
    const first = await onServer();
    expect(first.settings_gen).toBe(1);
    expect(Buffer.from(fromBase64url(first.settings_sealed!)).includes('24h')).toBe(false);

    const b = await signIn(words);
    expect(b.settings.value).toEqual({ v: 1, clock: '24h' });
    await b.changeSettings({ clock: '12h' });
    await b.syncSettings();
    expect((await onServer()).settings_gen).toBe(2);
    await a.syncSettings();
    expect(a.settings.value).toEqual({ v: 1, clock: '12h' });

    // The server takes a write only at the current settings_gen, and no blob too short to be sealed.
    const stale = await rejection(a.request('PUT', '/v1/account/settings', { settings_sealed: first.settings_sealed, settings_gen: 1 }));
    expect([stale.status, stale.code]).toEqual([409, 'conflict']);
    expect((await rejection(a.request('PUT', '/v1/account/settings', { settings_sealed: '', settings_gen: 2 }))).status).toBe(400);
    expect((await onServer()).settings_gen).toBe(2);
  });

  test('a typo is caught before any request; unknown words are refused', async () => {
    expect(await Account.signIn(device(), server.url, 'legal winner thank')).toBeNull();
    const unknown = 'legal winner thank year wave sausage worth useful legal winner thank yellow';
    expect((await rejection(Account.signIn(device(), server.url, unknown))).status).toBe(401);
  });

  test('a revoked session is not replaced: the device stops until the user signs in again', async () => {
    const { account: a, words } = await signUp();
    const b = await signIn(words);
    await b.revokeSession(oldest(await b.sessions()).id); // a's

    const e = await rejection(a.sessions());
    expect([e.status, e.code]).toEqual([401, 'session_revoked']);
    expect(await b.sessions()).toHaveLength(1);
  });

  test('sessions are listed and revoked', async () => {
    const { account: a, words } = await signUp();
    await signIn(words);
    const sessions = await a.sessions();
    expect(sessions).toHaveLength(2);
    const other = sessions.find((s) => s.id !== oldest(sessions).id)!;
    await a.revokeSession(other.id);
    expect((await a.sessions()).map((s) => s.id)).not.toContain(other.id);
  });

  test('change account key: same identity, the old words and the other sessions stop working', async () => {
    const { account: a, words: oldWords } = await signUp();
    const b = await signIn(oldWords);

    const words = await a.changeAccountKey();
    expect(words).not.toEqual(oldWords);
    expect(await a.showWords()).toEqual(words);
    expect(await serverIdentity(a)).toEqual(a.identity.signPub);

    // b's session was revoked, and its key no longer signs in.
    expect((await rejection(b.sessions())).status).toBe(401);
    expect((await rejection(Account.signIn(device(), server.url, oldWords.join(' ')))).status).toBe(401);

    const c = await signIn(words);
    expect(c.identity).toEqual(a.identity);
  });

  test('a restart keeps the account: offline from the vault, then online again', async () => {
    const { account: a } = await signUp();
    const [back] = await Account.resume(restart(a.platform));
    expect(back!.handle).toBe(a.handle);
    expect(back!.identity).toEqual(a.identity);
    expect(back!.inbox).toBe(a.inbox);
    expect(await back!.showWords()).toEqual(await a.showWords());
    // No cookie after the restart: the first request signs in again from the key in the vault.
    expect(await serverIdentity(back!)).toEqual(a.identity.signPub);
  });

  test('a sign-in on a new device is kept too, with its verified Inbox', async () => {
    const { account: a, words } = await signUp();
    const b = await signIn(words);
    const [back] = await Account.resume(restart(b.platform));
    expect(back!.inbox).toBe(a.inbox);
  });

  test('sign-out forgets the account on the device', async () => {
    const { account: a } = await signUp();
    await a.signOut();
    expect(await Account.resume(restart(a.platform))).toEqual([]);
  });
});
