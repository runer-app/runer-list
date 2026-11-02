import { describe, expect, test } from 'vitest';
import { Account } from '../src/account/account.js';
import { toBase64url } from '../src/base64url.js';
import type { Platform, Transport } from '../src/platform/index.js';
import { MemoryServer } from './platform-node/memory-server.js';
import { createMemoryPlatform } from './platform-node/memory.js';

// The account's settings (E2EE §3.3): sealed for the server, the same on
// every device, against the in-process server and the real vault.

const SERVER = 'https://runer.test';

const device = (transport: Transport): Platform => createMemoryPlatform({ transport });

/** The same device after a restart: keychain, vault store and local store kept. */
const restart = (p: Platform, transport: Transport): Platform => ({ ...p, transport });

async function signIn(server: MemoryServer, words: string[], transport = server.transport()): Promise<Account> {
  return (await Account.signIn(device(transport), SERVER, words.join(' ')))!;
}

/** Writes `settings` as another, perhaps newer, app would: straight to the server at its current `settings_gen`. */
async function writeAs(a: Account, settings: object): Promise<void> {
  const { settings_gen } = await a.request<{ settings_gen: number }>('GET', '/v1/account');
  const sealed = await a.platform.vault.sealAccountSettings(a.handle, JSON.stringify(settings));
  await a.request('PUT', '/v1/account/settings', { settings_sealed: toBase64url(sealed), settings_gen });
}

describe('account settings', () => {
  test('a change is there at once, reaches the server sealed, and a second device has it', async () => {
    const server = new MemoryServer();
    const { account: a, words } = await Account.signUp(device(server.transport()), SERVER);
    expect(a.settings.value).toEqual({ v: 1 });

    await a.changeSettings({ clock: '24h' });
    expect(a.settings.value).toEqual({ v: 1, clock: '24h' });
    await a.syncSettings();
    const kept = await a.request<{ settings_sealed: string; settings_gen: number }>('GET', '/v1/account');
    expect(kept.settings_gen).toBe(1);
    expect(Buffer.from(kept.settings_sealed, 'base64url').includes('24h')).toBe(false);

    const b = await signIn(server, words);
    expect(b.settings.value).toEqual({ v: 1, clock: '24h' });

    // No push: the first device hears of the second one's change when it next asks.
    await b.changeSettings({ clock: '12h' });
    await b.syncSettings();
    expect(a.settings.value.clock).toBe('24h');
    await a.syncSettings();
    expect(a.settings.value.clock).toBe('12h');
  });

  test('a change made offline survives a restart and is sent once the server is back', async () => {
    const server = new MemoryServer();
    const p = device(server.transport());
    const { words } = await Account.signUp(p, SERVER);
    const b = await signIn(server, words);

    server.offline = true;
    const [offline] = await Account.resume(restart(p, server.transport()));
    await offline!.changeSettings({ clock: '12h' });
    await expect(offline!.syncSettings()).rejects.toThrow('offline');
    const [still] = await Account.resume(restart(p, server.transport()));
    expect(still!.settings.value).toEqual({ v: 1, clock: '12h' });

    server.offline = false;
    await still!.syncSettings();
    await b.syncSettings();
    expect(b.settings.value).toEqual({ v: 1, clock: '12h' });
  });

  test("a newer app's fields are kept, and a write that lost the race is reapplied on top of the winner's", async () => {
    const server = new MemoryServer();
    const { account: a, words } = await Account.signUp(device(server.transport()), SERVER);
    await writeAs(a, { v: 2, clock: '12h', theme: 'dark' });

    // Another device writes between this one's fetch and its write, once.
    const inner = server.transport();
    let raced = false;
    const b = await signIn(server, words, {
      ...inner,
      request: async (method, path, body) => {
        if (method === 'PUT' && path === '/v1/account/settings' && !raced) {
          raced = true;
          await writeAs(a, { v: 2, clock: '12h', theme: 'light' });
        }
        return inner.request(method, path, body);
      },
    });
    expect(b.settings.value).toEqual({ v: 2, clock: '12h', theme: 'dark' });

    await b.changeSettings({ clock: '24h' });
    await b.syncSettings();
    expect(raced).toBe(true);
    expect(b.settings.value).toEqual({ v: 2, clock: '24h', theme: 'light' });
    await a.syncSettings();
    expect(a.settings.value).toEqual({ v: 2, clock: '24h', theme: 'light' });
  });

  test("settings that don't open are ignored, and signing out forgets them here", async () => {
    const server = new MemoryServer();
    const p = device(server.transport());
    const { account: a } = await Account.signUp(p, SERVER);
    await a.changeSettings({ clock: '24h' });
    await a.syncSettings();

    const { account: stranger } = await Account.signUp(device(server.transport()), SERVER);
    const { settings_sealed, settings_gen } = await a.request<{ settings_sealed: string; settings_gen: number }>('GET', '/v1/account');
    await stranger.request('PUT', '/v1/account/settings', { settings_sealed, settings_gen: 0 });
    await stranger.syncSettings();
    expect(stranger.settings.value).toEqual({ v: 1 });
    expect(settings_gen).toBe(1);

    await a.signOut();
    expect(await p.storage.get('account-settings', a.handle)).toBeNull();
    expect(await p.storage.get('account-settings-unsent', a.handle)).toBeNull();
  });

  test('sign-up hands over the words even when the name can not be kept or sent', async () => {
    const server = new MemoryServer();
    // The platform refuses to seal settings, as a shell that doesn't allow the command does.
    const broken = device(server.transport());
    broken.vault.sealAccountSettings = () => Promise.reject(new Error('not allowed'));
    const { account, words } = await Account.signUp(broken, SERVER, 'Anna');
    expect(words).toHaveLength(12);
    expect(account.name.value).toBe('Anna');

    // The server away right after the account is made: the name is kept here and sent by the next sync.
    const p = device(server.transport());
    const login = server.transport();
    let requests = 0;
    const flaky: Transport = { ...login, request: (...args) => (++requests > 2 ? Promise.reject(new Error('offline')) : login.request(...args)) };
    const { account: a, words: kept } = await Account.signUp({ ...p, transport: flaky }, SERVER, 'Bo');
    expect([kept.length, a.name.value]).toEqual([12, 'Bo']);
    expect(await p.storage.get('account-settings-unsent', a.handle)).not.toBeNull();
  });
});
