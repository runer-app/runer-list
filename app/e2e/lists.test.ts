import { Account, ApiError, createList, deleteList, refreshLists } from '@runer/client-core';
import { describe, expect, test } from 'vitest';
import { device, restart, server } from './device.js';

async function twoDevices(): Promise<[Account, Account]> {
  const { account: a, words } = await Account.signUp(device(), server.url);
  const b = await Account.signIn(device(), server.url, words.join(' '));
  return [a, b!];
}

async function rejection(p: Promise<unknown>): Promise<ApiError> {
  const e = await p.then(
    () => undefined,
    (e: unknown) => e,
  );
  expect(e).toBeInstanceOf(ApiError);
  return e as ApiError;
}

const toB64 = (b: Uint8Array) => Buffer.from(b).toString('base64url');

describe('lists and logs (E2EE §4)', () => {
  test('the Inbox comes with the account, every device knows it, and it cannot be deleted', async () => {
    const [a, b] = await twoDevices();
    expect(b.inbox).toBe(a.inbox);
    const { states: [inbox] } = await refreshLists(b);
    expect(inbox!.id).toBe(a.inbox);
    expect(inbox!.owner).toEqual(a.identity);
    expect((await rejection(deleteList(a, a.inbox))).status).toBe(403);
  });

  test('create, then another device verifies the log from genesis and holds the key', async () => {
    const [a, b] = await twoDevices();
    const created = await createList(a);
    expect(created.epoch).toBe(0);
    expect(created.owner).toEqual(a.identity);

    const { states: seen } = await refreshLists(b);
    expect(seen.find((s) => s.id === created.id)).toEqual(created);

    // Rotating needs the current key: b opened K_0 from the genesis.
    const entry = await b.platform.vault.rotate(b.handle, created.id);
    await b.request('POST', `/v1/groups/${created.id}/log`, { entry: toB64(entry.raw), sig: toB64(entry.sig) });

    // a catches up from its own head.
    const { states: caught } = await refreshLists(a);
    expect(caught.find((s) => s.id === created.id)!.epoch).toBe(1);
    expect(a.warnings.value).toEqual([]);
  });

  test('delete: gone for every device', async () => {
    const [a, b] = await twoDevices();
    const { id } = await createList(a);
    await refreshLists(b);
    await deleteList(a, id);
    expect((await refreshLists(a)).states.map((s) => s.id)).toEqual([a.inbox]);
    expect((await refreshLists(b)).states.map((s) => s.id)).toEqual([a.inbox]);
    expect((await rejection(b.request('GET', `/v1/groups/${id}/log`))).status).toBe(403);
  });

  test('a restart keeps the verified logs and their keys; catching up needs nothing new', async () => {
    const [a] = await twoDevices();
    const created = await createList(a);
    const [back] = await Account.resume(restart(a.platform));
    const { vault } = back!.platform;
    expect(await vault.listState(back!.handle, created.id)).toEqual(created);
    expect((await refreshLists(back!)).states.find((s) => s.id === created.id)).toEqual(created);
    expect(back!.warnings.value).toEqual([]);
    await vault.rotate(back!.handle, created.id); // needs K_0, opened again from the stored log
  });
});
