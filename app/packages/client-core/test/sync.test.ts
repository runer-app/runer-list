import { afterEach, describe, expect, test } from 'vitest';
import { Account } from '../src/account/account.js';
import type { ListDoc } from '../src/list/doc.js';
import type { Platform, Transport } from '../src/platform/index.js';
import { createInvite } from '../src/sync/members.js';
import { Sync, type SyncOptions } from '../src/sync/sync.js';
import { MemoryServer } from './platform-node/memory-server.js';
import { createMemoryPlatform, type MemoryLocalStore } from './platform-node/memory.js';
import { NodeVault } from './platform-node/vault.js';

// The sync engine against an in-process server (`memory-server.ts`) and the
// real vault. e2e/sync.test.ts runs more scenarios the same way.

const SERVER = 'https://runer.test';
const FAST: SyncOptions = { backoff: { firstMs: 5, maxMs: 20, jitterMs: 1 }, snapshotDelayMs: () => 0 };
const running: Sync[] = [];
let server: MemoryServer;

afterEach(async () => {
  for (const s of running.splice(0)) await s.stop();
});

function fresh(): MemoryServer {
  return (server = new MemoryServer());
}

async function started(account: Account, opts: SyncOptions = {}): Promise<Sync> {
  const s = new Sync(account, { ...FAST, ...opts });
  running.push(s);
  await s.start();
  return s;
}

async function signedUp(opts: SyncOptions = {}): Promise<{ sync: Sync; words: string }> {
  const { account, words } = await Account.signUp(createMemoryPlatform({ transport: server.transport() }), SERVER);
  return { sync: await started(account, opts), words: words.join(' ') };
}

async function anotherDevice(words: string, opts: SyncOptions = {}): Promise<Sync> {
  return started((await Account.signIn(createMemoryPlatform({ transport: server.transport() }), SERVER, words))!, opts);
}

async function twoDevices(opts: SyncOptions = {}): Promise<[Sync, Sync]> {
  const { sync, words } = await signedUp(opts);
  return [sync, await anotherDevice(words, opts)];
}

const failing: Transport = { request: () => Promise.reject(new Error('offline')), openSocket: () => Promise.reject(new Error('offline')) };

/** The same device after a restart: keychain, vault store and local store kept. */
function restart(p: Platform, transport: Transport = server.transport()): Platform {
  return createMemoryPlatform({ vault: new NodeVault((p.vault as NodeVault).device), storage: p.storage, transport });
}

async function restarted(s: Sync, transport?: Transport, opts: SyncOptions = {}): Promise<Sync> {
  await s.stop();
  const [account] = await Account.resume(restart(s.account.platform, transport));
  return started(account!, opts);
}

async function until(what: string, ok: () => boolean | Promise<boolean>, ms = 5000): Promise<void> {
  const end = Date.now() + ms;
  while (!(await ok())) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 5));
  }
}

const titles = (d: ListDoc | undefined) => d?.items().map((i) => i.title) ?? [];

describe('sync engine', () => {
  test('a list made on one device shows up on the other, and edits flow both ways', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const list = await a.createList('Groceries');
    a.doc(list)!.addTask('milk');
    await until('milk on b', () => titles(b.doc(list)).includes('milk'));
    expect(b.doc(list)!.name).toBe('Groceries');
    b.doc(list)!.addTask('bread');
    await until('bread on a', () => titles(a.doc(list)).includes('bread'));
    await Promise.all([a.settled(), b.settled()]);
    expect(titles(a.doc(list))).toEqual(titles(b.doc(list)));
  });

  test('edits in one tick go out as one update', async () => {
    fresh();
    const { sync: a } = await signedUp();
    const list = await a.createList('Batch');
    await a.settled();
    const before = server.received.filter((f) => f.t === 'update').length;
    const doc = a.doc(list)!;
    for (let i = 0; i < 10; i++) doc.addTask(`t${i}`);
    await a.settled();
    expect(server.received.filter((f) => f.t === 'update').length - before).toBe(1);
  });

  test('concurrent edits to different tasks merge', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const list = await a.createList('Chores');
    const one = a.doc(list)!.addTask('dishes');
    const two = a.doc(list)!.addTask('laundry');
    await until('both on b', () => b.doc(list)?.items().length === 2);
    a.doc(list)!.toggleDone(one);
    b.doc(list)!.setTitle(two, 'laundry (darks)');
    await until('both edits on both', () =>
      [a, b].every((s) => s.doc(list)!.item(one)?.done === true && s.doc(list)!.item(two)?.title === 'laundry (darks)'),
    );
  });

  test('a restart renders from the cache with no network; offline edits go out later', async () => {
    fresh();
    const { sync: a, words } = await signedUp();
    const list = await a.createList('Trip');
    a.doc(list)!.addTask('passport');
    await a.settled();

    const cold = await restarted(a, failing);
    expect(cold.doc(list)!.name).toBe('Trip');
    expect(titles(cold.doc(list))).toEqual(['passport']);
    expect(await new Promise((r) => cold.connection.subscribe(r))).not.toBe('online');
    cold.doc(list)!.addTask('tickets');
    await until('the edit in the outbox', async () => (await cold.store.outbox(list)).length === 1);

    const warm = await restarted(cold);
    expect(titles(warm.doc(list))).toEqual(['passport', 'tickets']);
    await warm.settled();
    expect(await warm.store.outbox()).toEqual([]);
    const b = await anotherDevice(words);
    await until('tickets on a new device', () => titles(b.doc(list)).includes('tickets'));
  });

  test('a list made with no network is usable at once, kept over a restart, and reaches the server and other devices later', async () => {
    fresh();
    const { sync: a, words } = await signedUp();
    await a.settled();

    const cold = await restarted(a, failing);
    const list = await cold.createList('Camping');
    cold.doc(list)!.addTask('tent');
    await until('the edits in the outbox', async () => (await cold.store.outbox(list)).length > 0);

    const still = await restarted(cold, failing);
    expect(still.doc(list)!.name).toBe('Camping');
    expect(titles(still.doc(list))).toEqual(['tent']);

    const warm = await restarted(still);
    await warm.settled();
    expect(await warm.store.created()).toEqual([]);
    expect(await warm.store.outbox()).toEqual([]);
    const b = await anotherDevice(words);
    await until('the list on a new device', () => titles(b.doc(list)).includes('tent'));
    expect(b.doc(list)!.name).toBe('Camping');

    // The server got the list but this device never heard: telling it again is harmless.
    const again = await warm.createList('Twice');
    await warm.settled();
    await warm.store.create(again, (await warm.account.request<{ entry: string; sig: string }[]>('GET', `/v1/groups/${again}/log?from=0`))[0]!);
    const back = await restarted(warm);
    await back.settled();
    expect(back.lists()).toContain(again);
  });

  test('a dropped connection reconnects, catches up from since, and resends the outbox', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const list = await a.createList('Flaky');
    await until('the list on b', () => b.doc(list) !== undefined);
    await b.settled();
    server.drop();
    a.doc(list)!.addTask('while dropped');
    b.doc(list)!.addTask('also while dropped');
    await until('both on both', () => [a, b].every((s) => titles(s.doc(list)).length === 2));
    await Promise.all([a.settled(), b.settled()]);
    // b's second sub asked only for what it lacked.
    const subs = server.received.filter((f) => f.t === 'sub') as { lists: { list: string; since: number }[] }[];
    expect(subs.flatMap((f) => f.lists).some((l) => l.list === list && l.since > 0)).toBe(true);
  });

  test('stale epoch: the outbox is re-sealed under the new epoch and resent', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const list = await a.createList('Shared');
    await until('the list on b', () => b.doc(list) !== undefined);
    await b.settled();

    const cold = await restarted(b, failing);
    cold.doc(list)!.addTask('written under epoch 0');
    await until('the edit in the outbox', async () => (await cold.store.outbox(list)).length === 1);
    const entry = await a.account.platform.vault.rotate(a.account.handle, list);
    const b64 = (x: Uint8Array) => Buffer.from(x).toString('base64url');
    await a.account.request('POST', `/v1/groups/${list}/log`, { entry: b64(entry.raw), sig: b64(entry.sig) });

    const warm = await restarted(cold);
    await until('the edit on a', () => titles(a.doc(list)).includes('written under epoch 0'));
    await warm.settled();
    expect(server.received.some((f) => f.t === 'update' && JSON.parse(Buffer.from(f.header as string, 'base64url').toString()).epoch === 1)).toBe(true);
  });

  test('snapshots: a lone writer uploads one, compacts, and a new device and a restart rebuild from it', async () => {
    fresh();
    const opts = { snapshotAfter: { updates: 3, bytes: 1 << 30 } };
    const { sync: a, words } = await signedUp(opts);
    const list = await a.createList('Big');
    for (const t of ['one', 'two', 'three', 'four', 'five']) {
      a.doc(list)!.addTask(t);
      await a.settled();
    }
    await until('a snapshot on the server', () => server.groups.get(list)!.snapshot !== undefined);
    await until('compacted locally', async () => (await a.store.load(list)).snapshots.length === 1);
    expect((await a.store.load(list)).updates.length).toBeLessThan(6);

    const b = await anotherDevice(words, opts);
    await until('everything on b', () => titles(b.doc(list)).length === 5);
    expect(b.doc(list)!.name).toBe('Big');
    expect((await b.store.load(list)).snapshots).toHaveLength(1);

    a.doc(list)!.addTask('six');
    await a.settled();
    const back = await restarted(a, server.transport(), opts);
    expect(titles(back.doc(list))).toEqual(['one', 'two', 'three', 'four', 'five', 'six']);
  });

  test('a deleted list disappears from the other device, cache included', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const list = await a.createList('Temp');
    await until('the list on b', () => b.doc(list) !== undefined);
    await a.deleteList(list);
    expect(a.doc(list)).toBeUndefined();
    await until('gone on b', () => b.doc(list) === undefined);
    expect(await b.store.lists()).not.toContain(list);
  });

  test('a live update under an older epoch is applied, with a withheld-removal warning', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const list = await a.createList('Old');
    await until('the list on b', () => b.doc(list) !== undefined);
    await b.settled();
    // b seals under epoch 0; a then learns of epoch 1 before the update reaches it.
    const sealed = await b.account.platform.vault.sealContent(b.account.handle, list, 'update', new Uint8Array([0, 0]));
    const entry = await a.account.platform.vault.rotate(a.account.handle, list);
    await a.account.platform.vault.applyLog(a.account.handle, list, [entry]);
    const s = [...server.sockets].find((x) => x.subs.has(list))!;
    const b64 = (x: Uint8Array) => Buffer.from(x).toString('base64url');
    s.push({ t: 'update', list, seq: 10_000, msg: { header: b64(sealed.header), nonce: b64(sealed.nonce), ct: b64(sealed.ct), sig: b64(sealed.sig) } });
    await until('the warning', () => [a, b].some((x) => x.account.warnings.value.some((w) => w.kind === 'withheld-removal')));
  });

  test('an update citing a log head the server never shows us is applied, with a log-fork warning', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const list = await a.createList('Split');
    await until('the list on b', () => b.doc(list) !== undefined);
    await b.settled();
    // a holds an entry the server doesn't have: to b, a history it can't place.
    const { vault } = a.account.platform;
    const { entry } = await vault.createInvite(a.account.handle, list, Date.now() + 60_000, 'Split', 'A');
    await vault.applyLog(a.account.handle, list, [entry]);
    a.doc(list)!.addTask('from the other history');
    await until('the task on b', () => titles(b.doc(list)).includes('from the other history'));
    await until('the warning', () => b.account.warnings.value.some((w) => w.kind === 'log-fork' && w.list === list));
    // Once is enough: every later update from a cites that history too.
    a.doc(list)!.addTask('and another');
    await until('the second task on b', () => titles(b.doc(list)).includes('and another'));
    await b.settled();
    expect(b.account.warnings.value).toHaveLength(1);
    expect(a.account.warnings.value).toEqual([]);
  });

  test('an update citing a log head we are behind on brings the log up to date, without a warning', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const list = await a.createList('Ahead');
    await until('the list on b', () => b.doc(list) !== undefined);
    await b.settled();
    // No `group` frame: b learns that the log grew only from the head a's update cites.
    (server as unknown as { notify: () => void }).notify = () => undefined;
    await createInvite(a.account, list, 'Ahead', 'A');
    a.doc(list)!.addTask('after the invite');
    await until('the task on b', () => titles(b.doc(list)).includes('after the invite'));
    await until('the log on b', async () => (await b.account.platform.vault.listState(b.account.handle, list)).head.seq === 1);
    await b.settled();
    expect(b.account.warnings.value).toEqual([]);
  });

  test('plaintext canary: no name or title reaches local storage or the wire', async () => {
    fresh();
    const [a, b] = await twoDevices();
    const canary = `CANARY-${crypto.randomUUID()}`;
    const list = await a.createList(canary);
    a.doc(list)!.addTask(canary);
    await until('the canary on b', () => titles(b.doc(list)).includes(canary));
    await Promise.all([a.settled(), b.settled()]);
    for (const s of [a, b]) {
      const dump = await (s.account.platform.storage as MemoryLocalStore).dump();
      expect(dump.length).toBeGreaterThan(0);
      expect(Buffer.concat(dump).includes(canary)).toBe(false);
    }
    expect(JSON.stringify(server.received)).not.toContain(canary);
  });

  test('sign-out deletes the account’s content on this device', async () => {
    fresh();
    const { sync: a } = await signedUp();
    const list = await a.createList('Mine');
    a.doc(list)!.addTask('x');
    await a.settled();
    await a.signOut();
    expect(await (a.account.platform.storage as MemoryLocalStore).dump()).toEqual([]);
    expect(await a.account.platform.vault.storedAccounts()).toEqual([]);
  });
});
