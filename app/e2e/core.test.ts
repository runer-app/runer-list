import { ClientCore, type ClientCoreOptions, type Readable } from '@runer/client-core';
import { afterEach, describe, expect, test } from 'vitest';
import { device, restart, server } from './device.js';

// The facade against the server, without the UI.

const OPTS: ClientCoreOptions = { sync: { backoff: { firstMs: 50, maxMs: 200, jitterMs: 10 }, snapshotDelayMs: () => 0 } };
const running: ClientCore[] = [];

afterEach(async () => {
  for (const c of running.splice(0)) await c.stop();
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

describe('ClientCore', () => {
  test('sign up, capture to the Inbox, restart, still there and on a second device', async () => {
    const p = device();
    const { core, accountKeyWords } = await ClientCore.signUp(p, server.url, '', OPTS);
    running.push(core);
    core.list(core.inbox).addTask('call the plumber');
    await until('online', () => value(core.connection) === 'online');
    await until('the task on the server', () => value(core.lists).some((l) => l.inbox && l.openTasks === 1));
    await core.stop();
    running.pop();

    const [back] = await ClientCore.resume(restart(p), OPTS);
    running.push(back!);
    expect(value(back!.list(back!.inbox).content).items.map((i) => i.title)).toEqual(['call the plumber']);

    const other = (await ClientCore.signIn(device(), server.url, accountKeyWords, OPTS))!;
    running.push(other);
    await until('the task on the other device', () => value(other.lists).some((l) => l.inbox && l.openTasks === 1));
  });
});
