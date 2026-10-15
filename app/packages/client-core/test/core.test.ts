import { afterEach, describe, expect, test } from 'vitest';
import { ClientCore, type ClientCoreOptions } from '../src/core/core.js';
import type { Platform, Transport } from '../src/platform/index.js';
import type { Readable } from '../src/readable.js';
import { MemoryServer } from './platform-node/memory-server.js';
import { createMemoryPlatform, RecordingNotifier } from './platform-node/memory.js';
import { NodeVault } from './platform-node/vault.js';

// The facade against the in-process server and the real vault: what the UI sees.

const SERVER = 'https://runer.test';
const NOW = new Date(2026, 9, 2, 12, 0);
const TODAY = '2026-10-02';
const OPTS: ClientCoreOptions = {
  sync: { backoff: { firstMs: 5, maxMs: 20, jitterMs: 1 }, snapshotDelayMs: () => 0 },
  now: () => NOW,
  reminderText: { generic: 'Påmindelse', inbox: 'Indbakke' },
};
const running: ClientCore[] = [];
let server: MemoryServer;

afterEach(async () => {
  for (const c of running.splice(0)) await c.stop();
});

function device(transport: Transport = server.transport()): Platform {
  return createMemoryPlatform({ transport });
}

/** The same device after a restart: keychain, vault store and local store kept. */
function restart(p: Platform, transport: Transport = server.transport()): Platform {
  return createMemoryPlatform({ vault: new NodeVault((p.vault as NodeVault).device), storage: p.storage, transport });
}

const failing: Transport = { request: () => Promise.reject(new Error('offline')), openSocket: () => Promise.reject(new Error('offline')) };

async function signedUp(p: Platform = device()): Promise<{ core: ClientCore; words: string[] }> {
  const { core, accountKeyWords } = await ClientCore.signUp(p, SERVER, '', OPTS);
  running.push(core);
  return { core, words: accountKeyWords };
}

function value<T>(r: Readable<T>): T {
  let v!: T;
  r.subscribe((x) => (v = x))();
  return v;
}

async function until(what: string, ok: () => boolean, ms = 5000): Promise<void> {
  const end = Date.now() + ms;
  while (!ok()) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 5));
  }
}

describe('ClientCore', () => {
  test('the thin slice: sign up, capture to the Inbox, see it in Today, restart offline, still there', async () => {
    server = new MemoryServer();
    const p = device();
    const { core, words } = await signedUp(p);
    expect(words).toHaveLength(12);

    const inbox = core.list(core.inbox);
    expect(inbox.inbox).toBe(true);
    expect(inbox.role).toBe('owner');
    inbox.addTask('call the plumber', { due: TODAY });
    inbox.addTask('someday');

    expect(value(core.today).map((t) => t.item.title)).toEqual(['call the plumber']);
    await until('the Inbox summary', () => value(core.lists).some((l) => l.inbox && l.openTasks === 2));
    expect(value(core.lists)).toEqual([expect.objectContaining({ id: core.inbox, name: '', inbox: true, role: 'owner', shared: false, openTasks: 2, doneTasks: 0 })]);
    await until('online', () => value(core.connection) === 'online');
    await core.stop();
    running.splice(running.indexOf(core), 1);

    const [back] = await ClientCore.resume(restart(p, failing), OPTS);
    running.push(back!);
    expect(back!.inbox).toBe(core.inbox);
    expect(value(back!.today).map((t) => t.item.title)).toEqual(['call the plumber']);
    expect(value(back!.list(back!.inbox).content).items.map((i) => i.title)).toEqual(['call the plumber', 'someday']);
    expect(value(back!.connection)).not.toBe('online');
  });

  test('lists, My tasks and search follow edits from another device', async () => {
    server = new MemoryServer();
    const { core: a, words } = await signedUp();
    const b = (await ClientCore.signIn(device(), SERVER, words, OPTS))!;
    running.push(b);

    const groceries = await a.createList('Groceries');
    a.list(groceries).addTask('milk', { due: '2026-10-05' });
    a.list(groceries).addTask('flour', { due: '2026-12-24' });

    await until('Groceries on b', () => value(b.lists).some((l) => l.name === 'Groceries' && l.openTasks === 2));
    const mine = value(b.myTasks);
    expect(mine.upcoming.map((t) => t.item.title)).toEqual(['milk']);
    expect(mine.planned.map((t) => t.item.title)).toEqual(['flour']);
    expect((await b.search('mil')).map((h) => h.item?.title)).toEqual(['milk']);
    expect(value(b.lists).map((l) => l.inbox)).toEqual([true, false]);
  });

  test('moving from the Inbox to a list keeps the item id', async () => {
    server = new MemoryServer();
    const { core } = await signedUp();
    const list = await core.createList('Home');
    const id = core.list(core.inbox).addTask('paint the fence');
    expect(core.list(core.inbox).moveToList(id, list)).toBe(true);
    expect(value(core.list(list).content).items.map((i) => i.id)).toEqual([id]);
    expect(value(core.list(core.inbox).content).items).toEqual([]);
  });

  test('warnings are a Readable, and the vault refuses what the owner of the Inbox may not do', async () => {
    server = new MemoryServer();
    const { core } = await signedUp();
    expect(value(core.warnings)).toEqual([]);
    expect(() => core.list('00'.repeat(32))).toThrow();
    await expect(core.list(core.inbox).leave()).rejects.toThrow(/owner can't leave/);
    await expect(core.list(core.inbox).createInvite()).rejects.toThrow(/Inbox is never shared/);
  });

  test('a device revoked from another one stops: it does not sign itself back in', async () => {
    server = new MemoryServer();
    const { core: a, words } = await signedUp();
    const b = (await ClientCore.signIn(device(), SERVER, words, OPTS))!;
    running.push(b);
    await until('b online', () => value(b.connection) === 'online');

    const theirs = (await a.sessions.list()).find((s) => !s.current)!;
    await a.sessions.revoke(theirs.id);
    server.drop(); // the real server closes the revoked socket

    await until('b signed out', () => value(b.connection) === 'signed-out');
    expect(await a.sessions.list()).toHaveLength(1);
    await until('a back online', () => value(a.connection) === 'online');
  });

  test('sign-out forgets the account on this device', async () => {
    server = new MemoryServer();
    const p = device();
    const { core } = await signedUp(p);
    await core.signOut();
    expect(value(core.connection)).toBe('signed-out');
    expect(await ClientCore.resume(restart(p), OPTS)).toEqual([]);
  });

  test('Today and My tasks keep the order the user dragged them into, across the user’s devices', async () => {
    server = new MemoryServer();
    const { core, words } = await signedUp();
    const inbox = core.list(core.inbox);
    const [a, b, c] = ['a', 'b', 'c'].map((t) => inbox.addTask(t, { due: TODAY })) as [string, string, string];
    expect(value(core.today).map((r) => r.item.title)).toEqual(['a', 'b', 'c']);
    core.setTaskOrder([c, a]);
    expect(value(core.today).map((r) => r.item.title)).toEqual(['c', 'a', 'b']);
    expect(value(core.myTasks).today.map((r) => r.item.title)).toEqual(['c', 'a', 'b']);

    const other = await ClientCore.signIn(device(), SERVER, words, OPTS);
    running.push(other!);
    await until('the order on the other device', () => value(other!.today).map((r) => r.item.title).join() === 'c,a,b');
    void b;
  });

  describe('reminders (client-spec §7.2)', () => {
    const at = NOW.getTime() + 3_600_000;

    test('are scheduled after every change, with the list name as body; the Inbox gets the UI’s name for it', async () => {
      server = new MemoryServer();
      const p = device();
      const notifier = p.notifications as RecordingNotifier;
      const { core } = await signedUp(p);
      const inbox = core.list(core.inbox);
      const id = inbox.addTask('water the plants', { remindAt: at });
      await until('scheduled', () => notifier.scheduled.length === 1);
      expect(notifier.scheduled[0]).toEqual({ id: `${core.inbox}:${id}`, at: new Date(at), title: 'water the plants', body: 'Indbakke' });

      const calls = notifier.calls;
      inbox.addNote('no reminder here');
      await new Promise((r) => setTimeout(r, 20));
      expect(notifier.calls).toBe(calls); // nothing changed for the OS

      inbox.toggleDone(id);
      await until('cleared', () => notifier.scheduled.length === 0);
    });

    test('the privacy setting hides titles, and is kept across restarts', async () => {
      server = new MemoryServer();
      const p = device();
      const { core } = await signedUp(p);
      expect(value(core.settings)).toEqual({ reminderTitles: true });
      await core.setReminderTitles(false);
      core.list(core.inbox).addTask('see the doctor', { remindAt: at });
      const notifier = p.notifications as RecordingNotifier;
      await until('scheduled', () => notifier.scheduled.length === 1);
      expect(notifier.scheduled[0]!.title).toBe('Påmindelse');
      expect(notifier.scheduled[0]).not.toHaveProperty('body');
      await core.stop();
      running.splice(running.indexOf(core), 1);

      const again = restart(p);
      const [back] = await ClientCore.resume(again, OPTS);
      running.push(back!);
      expect(value(back!.settings)).toEqual({ reminderTitles: false });
      await until('scheduled again', () => (again.notifications as RecordingNotifier).scheduled[0]?.title === 'Påmindelse');
    });

    test('sign-out unschedules them; permission is asked through the core', async () => {
      server = new MemoryServer();
      const p = device();
      p.notifications = new RecordingNotifier(64, 'prompt');
      const notifier = p.notifications as RecordingNotifier;
      const { core } = await signedUp(p);
      expect(await core.notificationPermission()).toBe('prompt');
      expect(await core.requestNotificationPermission()).toBe('granted');
      core.list(core.inbox).addTask('pay rent', { remindAt: at });
      await until('scheduled', () => notifier.scheduled.length === 1);
      await core.signOut();
      expect(notifier.scheduled).toEqual([]);
    });
  });
});
