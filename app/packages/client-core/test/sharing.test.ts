import { afterEach, describe, expect, test } from 'vitest';
import { toBase64url } from '../src/base64url.js';
import { ClientCore, type ClientCoreOptions } from '../src/core/core.js';
import { InviteError } from '../src/invite/join.js';
import type { ListId, ListState } from '../src/platform/vault.js';
import { rotationDue } from '../src/sync/members.js';
import type { Sync } from '../src/sync/sync.js';
import type { Readable } from '../src/readable.js';
import { MemoryServer } from './platform-node/memory-server.js';
import { createMemoryPlatform } from './platform-node/memory.js';

// Sharing through the facade (client-spec §3.4): two or three people, each
// on their own device and session, against the in-process server and the real vault.

const SERVER = 'https://runer.test';
const OPTS: ClientCoreOptions = { sync: { backoff: { firstMs: 5, maxMs: 20, jitterMs: 1 }, snapshotDelayMs: () => 0 } };
const running: ClientCore[] = [];
let server: MemoryServer;

afterEach(async () => {
  for (const c of running.splice(0)) await c.stop();
});

async function person(name = 'Someone', transport = server.transport()): Promise<ClientCore> {
  const { core } = await ClientCore.signUp(createMemoryPlatform({ transport }), SERVER, name, OPTS);
  running.push(core);
  return core;
}

function value<T>(r: Readable<T>): T {
  let v!: T;
  r.subscribe((x) => (v = x))();
  return v;
}

async function until(what: string, ok: () => boolean | Promise<boolean>, ms = 5000): Promise<void> {
  const end = Date.now() + ms;
  while (!(await ok())) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 5));
  }
}

const titles = (core: ClientCore, list: ListId) => value(core.list(list).content).items.map((i) => i.title);
const has = (core: ClientCore, list: ListId) => value(core.lists).some((l) => l.id === list);
/** Nothing left in flight on this device (the facade doesn't expose it). */
const settled = (core: ClientCore) => (core as unknown as { sync: Sync }).sync.settled();
const signPub = (core: ClientCore) => new Uint8Array(Buffer.from(core.me, 'base64url'));

/** Alice owns "Groceries" with one task; Bob joins it by link. */
async function aliceSharesWithBob() {
  server = new MemoryServer();
  const [alice, bob] = [await person(), await person()];
  const list = await alice.createList('Groceries');
  alice.list(list).addTask('milk');
  const invite = await alice.list(list).createInvite();
  const opened = await bob.invites.open(invite.link);
  await opened.join();
  await until('Alice sees Bob in the list', () => value(alice.lists).find((l) => l.id === list)!.members.length === 2);
  return { alice, bob, list, invite };
}

describe('sharing', () => {
  test('a link invite: Bob sees the list and its history, Alice is told he joined, edits flow both ways', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person(), await person()];
    const list = await alice.createList('Groceries');
    alice.list(list).addTask('milk');
    const invite = await alice.list(list).createInvite();
    expect(invite.link.startsWith(`${SERVER}/join#`)).toBe(true);
    expect(invite.expires - Date.now()).toBeGreaterThan(47 * 3600_000);
    expect(alice.list(list).openInvites.map((i) => toBase64url(i.invitePub))).toEqual([toBase64url(invite.invitePub)]);

    const opened = await bob.invites.open(invite.link);
    expect({ list: opened.list, name: opened.name, owner: toBase64url(opened.owner.signPub) }).toEqual({ list, name: 'Groceries', owner: alice.me });
    expect(await opened.join()).toBe(list);

    await until('Bob has the history', () => titles(bob, list).includes('milk'));
    expect(bob.list(list).role).toBe('member');
    expect(bob.list(list).members.map((m) => toBase64url(m.signPub))).toEqual([alice.me, bob.me]);
    await until('Alice is told', () => value(alice.notices).some((n) => n.kind === 'joined' && toBase64url(n.member) === bob.me));
    expect(value(bob.notices)).toEqual([]);

    bob.list(list).addTask('eggs');
    await until('Alice sees Bob’s task', () => titles(alice, list).includes('eggs'));
    await until('the invite is closed', () => alice.list(list).openInvites.length === 0);

    // Single use: the link is spent.
    const carol = await person();
    await expect(carol.invites.open(invite.link)).rejects.toMatchObject({ code: 'not-usable' });
  });

  test('members go by their account names, in every list they share', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person('Alice'), await person('Bob')];
    const list = await alice.createList('Groceries');
    const invite = await alice.list(list).createInvite();
    expect(alice.list(list).names).toEqual({});
    // Before joining, Bob can't read the list: the invite itself says who it is from.
    const opened = await bob.invites.open(invite.link);
    expect([opened.name, opened.ownerName]).toEqual(['Groceries', 'Alice']);
    await opened.join();

    const names = (core: ClientCore) => value(core.lists).find((l) => l.id === list)?.names ?? {};
    const both = { [alice.me]: 'Alice', [bob.me]: 'Bob' };
    await until('both see both by their own names', () => [alice, bob].every((c) => JSON.stringify(names(c)) === JSON.stringify(both)));

    await bob.setName('  Robert ');
    expect(value(bob.name)).toBe('Robert');
    await until('a rename reaches the list', () => names(alice)[bob.me] === 'Robert');
  });

  test('clearing a name removes it from the lists we share, and a device that missed the change does not write it back', async () => {
    server = new MemoryServer();
    const platform = createMemoryPlatform({ transport: server.transport() });
    const { core: alice } = await ClientCore.signUp(platform, SERVER, 'Alice', OPTS);
    running.push(alice);
    const bob = await person('Bob');
    const list = await alice.createList('Groceries');
    await (await bob.invites.open((await alice.list(list).createInvite()).link)).join();
    const names = (core: ClientCore) => value(core.lists).find((l) => l.id === list)?.names ?? {};
    await until('Bob sees Alice by name', () => names(bob)[alice.me] === 'Alice');

    // Settings aren't pushed: when Alice clears the name on one device, her other one still has "Alice" until its next start.
    const laptop = (await ClientCore.signIn(createMemoryPlatform({ transport: server.transport() }), SERVER, await alice.showAccountKey(), OPTS))!;
    running.push(laptop);
    await until('the laptop has the list', () => names(laptop)[alice.me] === 'Alice');
    // The laptop may have filled in the name itself before the list's content arrived: wait until the first device has everything it wrote.
    laptop.list(list).addTask('from the laptop');
    await until('the first device has the laptop’s edits', () => titles(alice, list).includes('from the laptop'));
    await alice.setName('  ');
    expect(value(alice.name)).toBe('');
    await until('the name is gone for Bob', () => names(bob)[alice.me] === undefined);
    await until('and on the laptop, in the list', () => names(laptop)[alice.me] === undefined);
    expect(value(laptop.name)).toBe('Alice');

    // A membership change makes every device fill in missing names: the laptop must leave the cleared one alone.
    const carol = await person('Carol');
    await (await carol.invites.open((await alice.list(list).createInvite()).link)).join();
    await until('everyone sees Carol', () => [alice, bob, laptop].every((c) => names(c)[carol.me] === 'Carol'));
    expect([alice, bob, laptop, carol].map((c) => names(c)[alice.me])).toEqual([undefined, undefined, undefined, undefined]);
  });

  test('a device that meets a list for the first time waits for its content before filling in a name', async () => {
    server = new MemoryServer();
    const { core: phone, accountKeyWords } = await ClientCore.signUp(createMemoryPlatform({ transport: server.transport() }), SERVER, 'Alice', OPTS);
    running.push(phone);
    // The laptop's settings can be held back, so it keeps an old name for as long as the test needs.
    const transport = server.transport();
    let settings = Promise.resolve();
    const other = createMemoryPlatform({
      transport: { ...transport, request: async (method, path, body) => (method === 'GET' && path === '/v1/account' && (await settings), transport.request(method, path, body)) },
    });
    await (await ClientCore.signIn(other, SERVER, accountKeyWords, OPTS))!.stop();

    // While the laptop is off, the phone shares a list and then clears the name: the laptop still has "Alice".
    const bob = await person('Bob');
    const list = await phone.createList('Groceries');
    await (await bob.invites.open((await phone.list(list).createInvite()).link)).join();
    const names = (core: ClientCore) => value(core.lists).find((l) => l.id === list)?.names ?? {};
    await until('Bob sees Alice by name', () => names(bob)[phone.me] === 'Alice');
    await phone.setName('');
    await until('the name is gone for Bob', () => names(bob)[phone.me] === undefined);

    let release!: () => void;
    settings = new Promise((r) => (release = r));
    const sent = () => server['groups'].get(list)!.updates.length;
    const before = sent();
    const [laptop] = await ClientCore.resume(other, OPTS);
    running.push(laptop!);
    await until('the laptop has the list’s content', () => names(laptop!)[bob.me] === 'Bob');
    await new Promise((r) => setTimeout(r, 50));
    // It saw the cleared name in the list, and wrote nothing over it.
    expect(sent()).toBe(before);
    release();
    await until('the laptop hears of the cleared name', () => value(laptop!.name) === '');
    expect(names(bob)[phone.me]).toBeUndefined();
  });

  test('deleting an account: its own lists are deleted for everyone, it leaves the others, and neither the server nor the device knows it afterwards', async () => {
    server = new MemoryServer();
    const platform = createMemoryPlatform({ transport: server.transport() });
    const { core: alice, accountKeyWords } = await ClientCore.signUp(platform, SERVER, 'Alice', OPTS);
    const bob = await person('Bob');
    const hers = await alice.createList('Groceries');
    await (await bob.invites.open((await alice.list(hers).createInvite()).link)).join();
    const his = await bob.createList('Trip');
    await (await alice.invites.open((await bob.list(his).createInvite()).link)).join();
    const lists = (core: ClientCore) => value(core.lists);
    await until('each has both lists', () => [alice, bob].every((c) => [hers, his].every((id) => lists(c).some((l) => l.id === id && l.members.length === 2))));

    await alice.deleteAccount();
    await until('her list is gone for Bob', () => !lists(bob).some((l) => l.id === hers));
    await until('and she is out of his', () => lists(bob).find((l) => l.id === his)?.members.length === 1);
    expect(await ClientCore.resume(platform, OPTS)).toEqual([]);
    await expect(ClientCore.signIn(createMemoryPlatform({ transport: server.transport() }), SERVER, accountKeyWords, OPTS)).rejects.toThrow();
  });

  test('an account without a name: sign-up works, invites carry no owner name, and the member has no name in the list', async () => {
    server = new MemoryServer();
    const { core: alice } = await ClientCore.signUp(createMemoryPlatform({ transport: server.transport() }), SERVER, undefined, OPTS);
    running.push(alice);
    expect(value(alice.name)).toBe('');
    const bob = await person('Bob');
    const list = await alice.createList('Groceries');
    const opened = await bob.invites.open((await alice.list(list).createInvite()).link);
    expect(opened.ownerName).toBe('');
    await opened.join();
    const names = (core: ClientCore) => value(core.lists).find((l) => l.id === list)?.names ?? {};
    await until('Alice sees Bob by name', () => names(alice)[bob.me] === 'Bob');
    expect(names(bob)).toEqual({ [bob.me]: 'Bob' });

    // A name given later reaches the list.
    await alice.setName('Alice');
    await until('Bob sees the new name', () => names(bob)[alice.me] === 'Alice');
  });

  test('the name is there on a second device and after a restart offline; a rename made offline elsewhere arrives at the next start, and reaches the lists from there', async () => {
    server = new MemoryServer();
    const platform = createMemoryPlatform({ transport: server.transport() });
    const { core: phone, accountKeyWords } = await ClientCore.signUp(platform, SERVER, ' Anna ', OPTS);
    running.push(phone);
    // The server holds the name sealed, in the settings, and beside it only the account's public key.
    expect(Object.keys(((await platform.transport.request('GET', '/v1/account')).body as { data: object }).data).sort()).toEqual(['identity_sign_pub', 'inbox_id', 'settings_gen', 'settings_sealed']);
    const other = createMemoryPlatform({ transport: server.transport() });
    const laptop = (await ClientCore.signIn(other, SERVER, accountKeyWords, OPTS))!;
    expect(value(laptop.name)).toBe('Anna');

    const bob = await person('Bob');
    const list = await phone.createList('Groceries');
    await (await bob.invites.open((await phone.list(list).createInvite()).link)).join();
    const names = (core: ClientCore) => value(core.lists).find((l) => l.id === list)?.names ?? {};
    await until('Bob sees Anna', () => names(bob)[phone.me] === 'Anna');

    // The laptop renames offline: there at once, and sent when it next starts with the server.
    await laptop.stop();
    server.offline = true;
    const [away] = await ClientCore.resume(other, OPTS);
    await away!.setName('Anna B');
    expect(value(away!.name)).toBe('Anna B');
    await away!.stop();
    server.offline = false;
    const [sent] = await ClientCore.resume(other, OPTS);
    running.push(sent!);
    await until('Bob sees the rename', () => names(bob)[phone.me] === 'Anna B');

    // The phone, restarted offline, still has the old name; with the server it hears of the new one.
    await phone.stop();
    server.offline = true;
    const [offline] = await ClientCore.resume(platform, OPTS);
    expect(value(offline!.name)).toBe('Anna');
    await offline!.stop();
    server.offline = false;
    const [back] = await ClientCore.resume(platform, OPTS);
    running.push(back!);
    await until('the phone hears of the rename', () => value(back!.name) === 'Anna B');
  });

  test('the clock follows the account: set offline, sent at the next start and there on the other device at its next; the reminder setting stays on its device', async () => {
    server = new MemoryServer();
    const platform = createMemoryPlatform({ transport: server.transport() });
    const { core: phone, accountKeyWords } = await ClientCore.signUp(platform, SERVER, 'Anna', OPTS);
    const other = createMemoryPlatform({ transport: server.transport() });
    const laptop = (await ClientCore.signIn(other, SERVER, accountKeyWords, OPTS))!;
    expect(value(laptop.settings)).toEqual({ reminderTitles: true });

    await phone.stop();
    server.offline = true;
    const [offline] = await ClientCore.resume(platform, OPTS);
    await offline!.setClock('24h');
    await offline!.setReminderTitles(false);
    expect(value(offline!.settings)).toEqual({ reminderTitles: false, clock: '24h' });
    await offline!.stop();

    server.offline = false;
    const [back] = await ClientCore.resume(platform, OPTS);
    running.push(back!);
    expect(value(back!.settings)).toEqual({ reminderTitles: false, clock: '24h' });
    await laptop.stop();
    let again: ClientCore | undefined;
    await until('the laptop has the clock after a start', async () => {
      await again?.stop();
      [again] = await ClientCore.resume(other, OPTS);
      await new Promise((r) => setTimeout(r, 20));
      return value(again!.settings).clock === '24h';
    });
    running.push(again!);
    expect(value(again!.settings)).toEqual({ reminderTitles: true, clock: '24h' });
  });

  test('a known contact is added directly, and removed with a new epoch', async () => {
    const { alice, bob } = await aliceSharesWithBob();
    expect((await alice.knownContacts()).map((c) => toBase64url(c.signPub))).toEqual([bob.me]);

    const trip = await alice.createList('Trip');
    alice.list(trip).addTask('passports');
    await alice.list(trip).addMember(signPub(bob));
    await until('Bob gets the list', () => has(bob, trip) && titles(bob, trip).includes('passports'));
    expect(alice.list(trip).members).toHaveLength(2);

    await alice.list(trip).removeMember(signPub(bob));
    expect(server.groups.get(trip)!.epoch).toBe(1);
    await until('Bob loses the list', () => !has(bob, trip));
    expect(alice.list(trip).members).toHaveLength(1);
    alice.list(trip).addTask('after');
    await until('Alice’s edit is accepted under the new epoch', () => server.groups.get(trip)!.updates.length >= 2);
  });

  test('when Alice deletes the list, Bob is told he no longer has access; Alice is not', async () => {
    const { alice, bob, list } = await aliceSharesWithBob();
    await alice.list(list).delete();
    await until('Bob loses the list', () => !has(bob, list));
    expect(value(bob.notices).filter((n) => n.kind === 'lost-access')).toEqual([{ kind: 'lost-access', list, name: 'Groceries' }]);
    expect(value(alice.notices).some((n) => n.kind === 'lost-access')).toBe(false);
  });

  test('a list whose log Bob can’t fetch doesn’t keep a deleted one on his device', async () => {
    server = new MemoryServer();
    const blocked = new Set<string>();
    const inner = server.transport();
    const transport = { ...inner, request: (m: string, path: string, body?: unknown) =>
      [...blocked].some((id) => path.startsWith(`/v1/groups/${id}/log`)) ? Promise.reject(new Error('offline')) : inner.request(m, path, body) } as typeof inner;
    const [alice, bob] = [await person(), await person('Bob', transport)];
    const [gone, stuck] = [await alice.createList('Gone'), await alice.createList('Stuck')];
    for (const list of [gone, stuck]) await (await bob.invites.open((await alice.list(list).createInvite()).link)).join();
    await until('Bob has both', () => has(bob, gone) && has(bob, stuck));
    await settled(bob);

    // Stuck's log grows, but Bob can't fetch it; Gone is deleted.
    blocked.add(stuck);
    await alice.list(stuck).createInvite();
    await alice.list(gone).delete();
    await until('Gone is gone for Bob', () => !has(bob, gone));
    expect(has(bob, stuck)).toBe(true);
  });

  test('when Bob leaves, Alice’s app rotates the key', async () => {
    const { alice, bob, list } = await aliceSharesWithBob();
    await expect(alice.list(list).leave()).rejects.toThrow(/owner can't leave/);
    await bob.list(list).leave();
    expect(has(bob, list)).toBe(false);
    await until('Alice rotated', () => server.groups.get(list)!.epoch === 1);
    await until('Alice is told', () => value(alice.notices).some((n) => n.kind === 'left'));
    await settled(bob);
    expect(value(bob.notices).some((n) => n.kind === 'lost-access')).toBe(false);
  });

  test('revoking a link closes it and rotates; an unused link that expired is rotated away too', async () => {
    server = new MemoryServer();
    const [alice, bob] = [await person(), await person()];
    const list = await alice.createList('Groceries');
    const invite = await alice.list(list).createInvite();
    await alice.list(list).revokeInvite(invite.invitePub);
    expect(server.groups.get(list)!.epoch).toBe(1);
    expect(alice.list(list).openInvites).toEqual([]);
    await expect(bob.invites.open(invite.link)).rejects.toMatchObject({ code: 'not-usable' });

    await alice.list(list).createInvite({ ttlMs: 30 });
    await new Promise((r) => setTimeout(r, 40));
    server.drop(); // the reconnect reconciles every list, and finds the expired invite
    await until('Alice rotated', () => server.groups.get(list)!.epoch === 2);
  });

  test('a link for another server, or not a link at all, is refused without saying what it held', async () => {
    server = new MemoryServer();
    const core = await person();
    const secret = 'A'.repeat(43);
    for (const [link, code] of [
      [`https://elsewhere.test/join#${secret}`, 'other-server'],
      [`${SERVER}/join#short`, 'bad-link'],
      [`${SERVER}/elsewhere#${secret}`, 'bad-link'],
    ] as const) {
      const e = await core.invites.open(link).catch((x: unknown) => x);
      expect(e).toBeInstanceOf(InviteError);
      expect(e).toMatchObject({ code });
      expect(String((e as Error).message)).not.toContain(secret);
    }
  });
});

describe('who rotates', () => {
  const owner = { signPub: new Uint8Array([1]), agreePub: new Uint8Array([2]) };
  const member = { signPub: new Uint8Array([3]), agreePub: new Uint8Array([4]) };
  const state = (rotationDue: ListState['rotationDue'], expires = Infinity): ListState => ({
    id: 'l' as ListId,
    owner,
    members: [owner, member],
    epoch: 0,
    head: { seq: 3, hash: new Uint8Array(32) },
    openInvites: expires === Infinity ? [] : [{ invitePub: new Uint8Array(32), expires }],
    linkJoins: [],
    created: 0,
    rotationDue,
  });
  const [o, m, outsider] = [owner, member, { signPub: new Uint8Array([9]) }].map((i) => toBase64url(i.signPub)) as [string, string, string];

  test('after a leave any member; after a revoked or expired link only the owner; never someone outside', () => {
    expect([o, m, outsider].map((me) => rotationDue(state('member-left'), me, 0))).toEqual([true, true, false]);
    expect([o, m].map((me) => rotationDue(state('invite-revoked'), me, 0))).toEqual([true, false]);
    expect([o, m].map((me) => rotationDue(state(null, 10), me, 11))).toEqual([true, false]);
    expect(rotationDue(state(null, 10), o, 9)).toBe(false);
  });
});
