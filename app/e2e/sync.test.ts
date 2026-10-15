import { Account, Sync, type ListDoc, type Platform, type SyncOptions } from '@runer/client-core';
import { afterEach, describe, expect, test } from 'vitest';
import type { MemoryLocalStore } from '../packages/client-core/test/platform-node/memory.js';
import { device, restart, server } from './device.js';

const FAST: SyncOptions = {
  backoff: { firstMs: 50, maxMs: 200, jitterMs: 10 },
  snapshotDelayMs: () => 0,
  // SYNC_LOG=1 prints what the engine rejects or drops.
  ...(process.env.SYNC_LOG ? { log: (m: string) => console.error(m) } : {}),
};
const running: Sync[] = [];

afterEach(async () => {
  for (const s of running.splice(0)) await s.stop();
});

async function started(account: Account, opts: SyncOptions = {}): Promise<Sync> {
  const s = new Sync(account, { ...FAST, ...opts });
  running.push(s);
  await s.start();
  return s;
}

async function signedUp(opts: SyncOptions = {}): Promise<{ sync: Sync; words: string }> {
  const { account, words } = await Account.signUp(device(), server.url);
  return { sync: await started(account, opts), words: words.join(' ') };
}

async function anotherDevice(words: string, opts: SyncOptions = {}): Promise<Sync> {
  return started((await Account.signIn(device(), server.url, words))!, opts);
}

async function twoDevices(opts: SyncOptions = {}): Promise<[Sync, Sync]> {
  const { sync, words } = await signedUp(opts);
  return [sync, await anotherDevice(words, opts)];
}

/** The same device after a restart, with no network. */
function offline(p: Platform): Platform {
  const fail = () => Promise.reject(new Error('offline'));
  return { ...restart(p), transport: { request: fail, openSocket: fail } };
}

/** Restarts a device's sync: stops it, resumes the account on `platform`, starts again. */
async function restarted(s: Sync, platform: (p: Platform) => Platform): Promise<Sync> {
  await s.stop();
  const [account] = await Account.resume(platform(s.account.platform));
  return started(account!);
}

async function until(what: string, ok: () => boolean | Promise<boolean>, ms = 10_000): Promise<void> {
  const end = Date.now() + ms;
  while (!(await ok())) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 20));
  }
}

const titles = (d: ListDoc | undefined) => d?.items().map((i) => i.title) ?? [];
const b64 = (x: Uint8Array) => Buffer.from(x).toString('base64url');

describe('sync (Sync protocol)', () => {
  test('a list made on one device shows up on the other, and edits flow both ways', async () => {
    const [a, b] = await twoDevices();
    const list = await a.createList('Groceries');
    a.doc(list)!.addTask('milk');
    await until('the list on b', () => titles(b.doc(list)).includes('milk'));
    expect(b.doc(list)!.name).toBe('Groceries');

    b.doc(list)!.addTask('bread');
    await until('bread on a', () => titles(a.doc(list)).includes('bread'));
    await Promise.all([a.settled(), b.settled()]);
    expect(titles(a.doc(list))).toEqual(titles(b.doc(list)));
  });

  test('concurrent edits to different tasks merge', async () => {
    const [a, b] = await twoDevices();
    const list = await a.createList('Chores');
    const one = a.doc(list)!.addTask('dishes');
    const two = a.doc(list)!.addTask('laundry');
    await until('both tasks on b', () => b.doc(list)?.items().length === 2);

    a.doc(list)!.toggleDone(one);
    b.doc(list)!.setTitle(two, 'laundry (darks)');
    await until('both edits on both devices', () =>
      [a, b].every((s) => s.doc(list)!.item(one)?.done === true && s.doc(list)!.item(two)?.title === 'laundry (darks)'),
    );
  });

  test('a restart renders from the cache with no network; offline edits go out later', async () => {
    const { sync: a, words } = await signedUp();
    const list = await a.createList('Trip');
    a.doc(list)!.addTask('passport');
    await a.settled();

    const cold = await restarted(a, offline);
    expect(cold.doc(list)!.name).toBe('Trip');
    expect(titles(cold.doc(list))).toEqual(['passport']);
    cold.doc(list)!.addTask('tickets');
    await until('the edit in the outbox', async () => (await cold.store.outbox(list)).length === 1);

    const warm = await restarted(cold, restart);
    expect(titles(warm.doc(list))).toEqual(['passport', 'tickets']);
    await warm.settled();
    const fresh = await anotherDevice(words);
    await until('tickets on a new device', () => titles(fresh.doc(list)).includes('tickets'));
  });

  test('a list made with no network is usable at once, kept over a restart, and reaches the server and other devices later', async () => {
    const { sync: a, words } = await signedUp();
    await a.settled();

    const cold = await restarted(a, offline);
    const list = await cold.createList('Camping');
    cold.doc(list)!.addTask('tent');
    await until('the edits in the outbox', async () => (await cold.store.outbox(list)).length > 0);

    const still = await restarted(cold, offline);
    expect(still.doc(list)!.name).toBe('Camping');
    expect(titles(still.doc(list))).toEqual(['tent']);

    const warm = await restarted(still, restart);
    await warm.settled();
    expect(await warm.store.created()).toEqual([]);
    const fresh = await anotherDevice(words);
    await until('the list on a new device', () => titles(fresh.doc(list)).includes('tent'));
    expect(fresh.doc(list)!.name).toBe('Camping');

    // The server got the list but this device never heard: telling it again is harmless.
    const again = await warm.createList('Twice');
    await warm.settled();
    await warm.store.create(again, (await warm.account.request<{ entry: string; sig: string }[]>('GET', `/v1/groups/${again}/log?from=0`))[0]!);
    const back = await restarted(warm, restart);
    await back.settled();
    expect(back.lists()).toContain(again);
  });

  test('stale epoch: the outbox is re-sealed under the new epoch and resent', async () => {
    const [a, b] = await twoDevices();
    const list = await a.createList('Shared');
    await until('the list on b', () => b.doc(list) !== undefined);
    await b.settled();

    // b edits offline under epoch 0, while a rotates to epoch 1.
    const cold = await restarted(b, offline);
    cold.doc(list)!.addTask('written under epoch 0');
    await until('the edit in the outbox', async () => (await cold.store.outbox(list)).length === 1);
    const entry = await a.account.platform.vault.rotate(a.account.handle, list);
    await a.account.request('POST', `/v1/groups/${list}/log`, { entry: b64(entry.raw), sig: b64(entry.sig) });

    const warm = await restarted(cold, restart);
    await until('the edit on a', () => titles(a.doc(list)).includes('written under epoch 0'));
    await warm.settled();
    expect((await warm.account.platform.vault.listState(warm.account.handle, list)).epoch).toBe(1);
  });

  test('snapshots: a device that writes alone uploads one, compacts, and a new device catches up from it', async () => {
    const opts = { snapshotAfter: { updates: 3, bytes: 1 << 30 } };
    const { sync: a, words } = await signedUp(opts);
    const list = await a.createList('Big');
    for (const t of ['one', 'two', 'three', 'four', 'five']) {
      a.doc(list)!.addTask(t);
      await a.settled();
    }
    await until('a snapshot replacing the stored updates', async () => (await a.store.load(list)).snapshots.length === 1);
    const stored = await a.store.load(list);
    expect(stored.updates.length).toBeLessThan(6);

    const b = await anotherDevice(words, opts);
    await until('everything on b', () => titles(b.doc(list)).length === 5);
    expect((await b.store.load(list)).snapshots).toHaveLength(1);
    expect(b.doc(list)!.name).toBe('Big');

    // And a restart rebuilds from the snapshot and what came after it.
    a.doc(list)!.addTask('six');
    await a.settled();
    const back = await restarted(a, restart);
    expect(titles(back.doc(list))).toEqual(['one', 'two', 'three', 'four', 'five', 'six']);
  });

  test('a deleted list disappears from the other device, cache included', async () => {
    const [a, b] = await twoDevices();
    const list = await a.createList('Temp');
    await until('the list on b', () => b.doc(list) !== undefined);
    await a.deleteList(list);
    expect(a.doc(list)).toBeUndefined();
    await until('gone on b', () => b.doc(list) === undefined);
    expect(await b.store.lists()).not.toContain(list);
  });

  test('plaintext canary: no name or title reaches local storage', async () => {
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
  });

  test('sign-out deletes the account’s content on this device', async () => {
    const { sync: a } = await signedUp();
    const list = await a.createList('Mine');
    a.doc(list)!.addTask('x');
    await a.settled();
    await a.signOut();
    expect(await (a.account.platform.storage as MemoryLocalStore).dump()).toEqual([]);
    expect(await a.account.platform.vault.storedAccounts()).toEqual([]);
  });
});
