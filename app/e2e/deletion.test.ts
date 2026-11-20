import { ClientCore, toBase64url, type ClientCoreOptions, type ListId, type Readable } from '@runer/client-core';
import { afterEach, describe, expect, test } from 'vitest';
import { createMemoryPlatform } from '../packages/client-core/test/platform-node/memory.js';
import { device, server, transport } from './device.js';
import { ProxyTransport } from './proxy.js';

// Deleting an account or a list against the server (E2EE §8.4): it is gone
// at once, the lists others keep still verify what the deleted account
// wrote, and its other devices stop.

const OPTS: ClientCoreOptions = { sync: { backoff: { firstMs: 50, maxMs: 200, jitterMs: 10 }, snapshotDelayMs: () => 0 } };
const running: ClientCore[] = [];

afterEach(async () => {
  const all = running.splice(0);
  // Deleting is ordinary use: none of it may raise a security warning.
  const warned = all.flatMap((c) => value(c.warnings));
  for (const c of all) await c.stop();
  expect(warned).toEqual([]);
});

function value<T>(r: Readable<T>): T {
  let v!: T;
  r.subscribe((x) => (v = x))();
  return v;
}

async function until(what: string, ok: () => boolean, ms = 10_000): Promise<void> {
  const end = Date.now() + ms;
  while (!ok()) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 20));
  }
}

/** A person on a device whose every request is recorded. */
async function person(): Promise<{ core: ClientCore; words: string[]; proxy: ProxyTransport }> {
  const proxy = new ProxyTransport(transport());
  const { core, accountKeyWords: words } = await ClientCore.signUp(createMemoryPlatform({ transport: proxy }), server.url, '', OPTS);
  running.push(core);
  return { core, words, proxy };
}

async function signIn(words: string[], proxy = new ProxyTransport(transport())): Promise<ClientCore> {
  const core = await ClientCore.signIn(createMemoryPlatform({ transport: proxy }), server.url, words, OPTS);
  expect(core).not.toBeNull();
  running.push(core!);
  return core!;
}

const summary = (core: ClientCore, list: ListId) => value(core.lists).find((l) => l.id === list);
const titles = (core: ClientCore, list: ListId) =>
  summary(core, list) === undefined ? [] : value(core.list(list).content).items.map((i) => i.title);
const lostAccess = (core: ClientCore, list: ListId) => value(core.notices).some((n) => n.kind === 'lost-access' && n.list === list);

async function joinByLink(owner: ClientCore, who: ClientCore, list: ListId): Promise<void> {
  const { link } = await owner.list(list).createInvite();
  await (await who.invites.open(link)).join();
}

describe('deletion', () => {
  test('a member is refused until it leaves; afterwards the list still verifies everything it wrote', async () => {
    const { core: alice, words: aliceWords } = await person();
    const { core: bob, proxy: bobNet } = await person();
    const list = await alice.createList('Groceries');
    await joinByLink(alice, bob, list);
    await until('Bob has the list', () => summary(bob, list) !== undefined);
    bob.list(list).addTask('from Bob');
    await until('Alice sees it', () => titles(alice, list).includes('from Bob'));

    // The server can't take Bob out of Alice's log, so it refuses.
    const refused = await bobNet.request('DELETE', '/v1/account');
    expect(refused.status).toBe(409);
    expect(refused.body).toMatchObject({ error: { code: 'account_has_lists' } });

    // The app leaves first, then deletes.
    await bob.deleteAccount();
    await until('Alice is told Bob left', () => value(alice.notices).some((n) => n.kind === 'left' && toBase64url(n.member) === bob.me));

    // A new device of Alice's fetches and verifies the whole history from the server: Bob's signed join and leave
    // and his update, signed by a key whose account is gone.
    const fresh = await signIn(aliceWords);
    await until('the history on the new device', () => titles(fresh, list).includes('from Bob'));
    expect(summary(fresh, list)!.members.map((m) => toBase64url(m.signPub))).toEqual([alice.me]);
    fresh.list(list).addTask('after');
    await until('the list takes writes', () => titles(alice, list).includes('after'));
  });

  test('a deleted owner takes its shared lists along: members lose access', async () => {
    const { core: alice } = await person();
    const { core: bob } = await person();
    const list = await alice.createList('Trip');
    await joinByLink(alice, bob, list);
    await until('Bob has the list', () => summary(bob, list) !== undefined);

    await alice.deleteAccount();
    await until('Bob loses the list', () => summary(bob, list) === undefined);
    expect(lostAccess(bob, list)).toBe(true);
  });

  test('the owner deletes one shared list: gone for the member, the rest stays', async () => {
    const { core: alice } = await person();
    const { core: bob } = await person();
    const [gone, kept] = [await alice.createList('Gone'), await alice.createList('Kept')];
    // One after the other: each join settled on both sides before the next invite.
    for (const l of [gone, kept]) {
      await joinByLink(alice, bob, l);
      await until('both see the join', () => [alice, bob].every((c) => summary(c, l)?.members.length === 2));
    }

    await alice.list(gone).delete();
    await until('Bob loses it', () => summary(bob, gone) === undefined);
    expect(lostAccess(bob, gone)).toBe(true);
    expect(summary(bob, kept)).toBeDefined();
  });

  test('a second device of a deleted account is signed out and never registers the keys again', async () => {
    const { core: a, words } = await person();
    const net = new ProxyTransport(transport());
    const b = await signIn(words, net);
    await until('b online', () => value(b.connection) === 'online');

    await a.deleteAccount();
    await until('b signed out', () => value(b.connection) === 'signed-out');
    expect((await net.request('GET', '/v1/account')).status).toBe(401);
    // Its logins were plain ones: no request carried the sign-up fields that could register the keys anew.
    const sent = net.seen.map((s) => JSON.parse(s) as { path?: string; body?: Record<string, unknown> });
    expect(sent.some((r) => r.path === '/v1/auth/login')).toBe(true);
    expect(sent.filter((r) => r.body?.identity_sign_pub !== undefined)).toEqual([]);
    // The words can't bring it back either.
    await expect(ClientCore.signIn(device(), server.url, words, OPTS)).rejects.toMatchObject({ status: 401 });
  });
});
