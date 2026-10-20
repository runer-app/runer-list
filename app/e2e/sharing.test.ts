import { ClientCore, toBase64url, type ClientCoreOptions, type ListId, type Readable } from '@runer/client-core';
import { afterEach, describe, expect, test } from 'vitest';
import { device, server } from './device.js';

// Sharing against the server: link invites, a direct
// add of a known contact, removal with rotation, leaving, revoking.

const OPTS: ClientCoreOptions = { sync: { backoff: { firstMs: 50, maxMs: 200, jitterMs: 10 }, snapshotDelayMs: () => 0 } };
const running: ClientCore[] = [];

afterEach(async () => {
  const all = running.splice(0);
  // Joining, removing, leaving and revoking are ordinary use: none of it may raise a security warning.
  const warned = all.flatMap((c) => value(c.warnings));
  for (const c of all) await c.stop();
  expect(warned).toEqual([]);
});

async function person(): Promise<ClientCore> {
  const { core } = await ClientCore.signUp(device(), server.url, '', OPTS);
  running.push(core);
  return core;
}

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

/** Empty until the device holds the list (`core.list` throws before that). */
const titles = (core: ClientCore, list: ListId) =>
  summary(core, list) === undefined ? [] : value(core.list(list).content).items.map((i) => i.title);
const summary = (core: ClientCore, list: ListId) => value(core.lists).find((l) => l.id === list);
const signPub = (core: ClientCore) => new Uint8Array(Buffer.from(core.me, 'base64url'));

/** `who` joins `list` from a fresh link of `owner`'s. */
async function joinByLink(owner: ClientCore, who: ClientCore, list: ListId): Promise<void> {
  const { link } = await owner.list(list).createInvite();
  const opened = await who.invites.open(link);
  expect(opened.name).toBe(value(owner.list(list).content).name);
  expect(toBase64url(opened.owner.signPub)).toBe(owner.me);
  await opened.join();
}

describe('sharing', () => {
  test('a link invite: Bob joins while Alice is told, reads the history, and the link is spent', async () => {
    const [alice, bob, carol] = [await person(), await person(), await person()];
    const list = await alice.createList('Groceries');
    alice.list(list).addTask('milk');
    await until('the task on the server', () => summary(alice, list)?.openTasks === 1);

    const { link } = await alice.list(list).createInvite();
    const opened = await bob.invites.open(link);
    expect(await opened.join()).toBe(list);
    await until('Bob reads the history', () => titles(bob, list).includes('milk'));
    await until('Alice is told', () => value(alice.notices).some((n) => n.kind === 'joined' && toBase64url(n.member) === bob.me));

    bob.list(list).addTask('eggs');
    await until('Alice sees Bob’s task', () => titles(alice, list).includes('eggs'));
    await expect(carol.invites.open(link)).rejects.toMatchObject({ code: 'not-usable' });

    // The safety code check (E2EE §6.4): Alice's code for the Bob her verified log admitted is the one Bob reads out.
    const bobInAlicesLog = summary(alice, list)!.members.find((m) => toBase64url(m.signPub) === bob.me)!;
    const bobInBobsLog = summary(bob, list)!.members.find((m) => toBase64url(m.signPub) === bob.me)!;
    const code = await alice.safetyCode(bobInAlicesLog);
    expect(code).toHaveLength(6);
    expect(await bob.safetyCode(bobInBobsLog)).toEqual(code);
  });

  test('share with two and remove one: the removed member loses the list, the others write under the new epoch', async () => {
    const [alice, bob, carol] = [await person(), await person(), await person()];
    const first = await alice.createList('First');
    await joinByLink(alice, bob, first);
    await joinByLink(alice, carol, first);
    await until('Alice knows both', () => summary(alice, first)?.members.length === 3);

    // Both are known contacts now: the next list needs no link (E2EE §6.3).
    const trip = await alice.createList('Trip');
    alice.list(trip).addTask('passports');
    for (const who of [bob, carol]) await alice.list(trip).addMember(signPub(who));
    await until('Bob and Carol have the list', () => titles(bob, trip).includes('passports') && titles(carol, trip).includes('passports'));

    await alice.list(trip).removeMember(signPub(bob));
    await until('Bob loses the list', () => summary(bob, trip) === undefined);
    alice.list(trip).addTask('tickets');
    carol.list(trip).addTask('hotel');
    await until('Carol sees Alice’s new task', () => titles(carol, trip).includes('tickets'));
    await until('Alice sees Carol’s', () => titles(alice, trip).includes('hotel'));
    expect(summary(alice, trip)!.members.map((m) => toBase64url(m.signPub))).toEqual([alice.me, carol.me]);
  });

  test('Bob leaves, and a remaining member rotates the key he held, once', async () => {
    const [alice, bob, carol] = [await person(), await person(), await person()];
    const list = await alice.createList('Groceries');
    await joinByLink(alice, bob, list);
    await joinByLink(alice, carol, list);
    await until('everyone sees everyone', () => [alice, bob, carol].every((c) => summary(c, list)?.members.length === 3));

    await bob.list(list).leave();
    expect(summary(bob, list)).toBeUndefined();
    await until('Alice is told', () => value(alice.notices).some((n) => n.kind === 'left'));
    // Whoever of the two rotates, the other sees it in the log; the loser of the race doesn't rotate again.
    const rotations = () => [alice, carol].flatMap((c) => value(c.notices).filter((n) => n.kind === 'rotated'));
    await until('a rotation', () => rotations().length > 0);
    expect(rotations()).toEqual([{ kind: 'rotated', list, epoch: 1 }]);

    // Writes after the rotation are accepted at the new epoch (a stale one would be refused).
    alice.list(list).addTask('after');
    await until('Carol sees it', () => titles(carol, list).includes('after'));
  });

  test('a revoked link can no longer be used', async () => {
    const [alice, bob] = [await person(), await person()];
    const list = await alice.createList('Groceries');
    const invite = await alice.list(list).createInvite();
    await alice.list(list).revokeInvite(invite.invitePub);
    expect(alice.list(list).openInvites).toEqual([]);
    await expect(bob.invites.open(invite.link)).rejects.toMatchObject({ code: 'not-usable' });
  });
});
