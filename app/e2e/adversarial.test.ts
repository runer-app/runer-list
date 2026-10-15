import {
  ClientCore,
  toBase64url,
  type ClientCoreOptions,
  type HttpResponse,
  type ListId,
  type Platform,
  type Readable,
  type SecurityWarning,
} from '@runer/client-core';
import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { afterEach, describe, expect, test } from 'vitest';
import { createMemoryPlatform } from '../packages/client-core/test/platform-node/memory.js';
import { NodeVault } from '../packages/client-core/test/platform-node/vault.js';
import { device, server, transport } from './device.js';
import { ProxyTransport } from './proxy.js';

// The adversarial proxy (client-spec §11, E2EE §1): a hostile server between
// one device and the server. It can withhold, replay and rewrite; the
// client must notice, or refuse, and never read or trust what it forged.

const running: ClientCore[] = [];
const logs: string[] = [];
const OPTS: ClientCoreOptions = {
  sync: { backoff: { firstMs: 50, maxMs: 200, jitterMs: 10 }, snapshotDelayMs: () => 0, log: (m) => void logs.push(m) },
};

afterEach(async () => {
  for (const c of running.splice(0)) await c.stop();
  logs.length = 0;
});

interface LogRow {
  seq: number;
  entry: string;
  sig: string;
  hash: string;
}
interface GroupRow {
  id: string;
  head_seq: number;
  head_hash: string;
}

function proxied(from?: Platform): { platform: Platform; proxy: ProxyTransport } {
  const proxy = new ProxyTransport(transport());
  const platform =
    from === undefined
      ? createMemoryPlatform({ transport: proxy })
      : createMemoryPlatform({ vault: new NodeVault((from.vault as NodeVault).device), storage: from.storage, transport: proxy });
  return { platform, proxy };
}

async function signUp(platform: Platform = device()): Promise<{ core: ClientCore; words: string[] }> {
  const { core, accountKeyWords } = await ClientCore.signUp(platform, server.url, '', OPTS);
  running.push(core);
  return { core, words: accountKeyWords };
}

function value<T>(r: Readable<T>): T {
  let v!: T;
  r.subscribe((x) => (v = x))();
  return v;
}

async function until(what: string, ok: () => boolean | Promise<boolean>, ms = 10_000): Promise<void> {
  const end = Date.now() + ms;
  while (!(await ok())) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 20));
  }
}

const warned = (core: ClientCore, kind: SecurityWarning['kind']) => value(core.warnings).some((w) => w.kind === kind);
const titles = (core: ClientCore, list: ListId) => value(core.list(list).content).items.map((i) => i.title);
const b64sha = (raw: Uint8Array) => createHash('sha256').update(raw).digest('base64url');
const handle = async (p: Platform) => (await p.vault.storedAccounts())[0]!.account;
const data = <T>(res: HttpResponse) => (res.body as { data: T }).data;

/** Rewrites the head `GET /v1/groups` reports for `list`. */
function headOf(list: ListId, head: { seq: number; hash: string }) {
  return (method: string, path: string, res: HttpResponse): HttpResponse => {
    if (method === 'GET' && path === '/v1/groups' && res.status === 200) {
      for (const g of data<GroupRow[]>(res)) if (g.id === list) Object.assign(g, { head_seq: head.seq, head_hash: head.hash });
    }
    return res;
  };
}

/** Serves `rows` as `list`'s log, from the `from` asked for. */
function logOf(list: ListId, rows: LogRow[], also = headOf(list, { seq: rows.at(-1)!.seq, hash: rows.at(-1)!.hash })) {
  return (method: string, path: string, res: HttpResponse): HttpResponse => {
    const m = /^\/v1\/groups\/([0-9a-f]+)\/log\?from=(\d+)$/.exec(path);
    if (method === 'GET' && m?.[1] === list) return { ...res, status: 200, body: { data: rows.filter((r) => r.seq >= Number(m[2])) } };
    return also(method, path, res);
  };
}

async function logRows(p: Platform, list: ListId): Promise<LogRow[]> {
  return data<LogRow[]>(await p.transport.request('GET', `/v1/groups/${list}/log?from=0`));
}

describe('a hostile server', () => {
  test('serves an older identity bundle than this device has seen: bundle-rollback', async () => {
    const { platform, proxy } = proxied();
    const { core } = await signUp(platform);
    const old = await proxy.request('GET', '/v1/account/bundle');
    expect(data<{ bundle_gen: number }>(old).bundle_gen).toBe(1);
    await core.changeAccountKey(); // bundle_gen 2
    await core.stop();

    const again = proxied(platform);
    again.proxy.tamper.response = (method, path, res) => (method === 'GET' && path === '/v1/account/bundle' ? old : res);
    const [back] = await ClientCore.resume(again.platform, OPTS);
    running.push(back!);
    await until('the warning', () => warned(back!, 'bundle-rollback'));
  });

  test('reports a lower head, or another hash at a seq we hold: log-rollback, log-fork', async () => {
    const { platform, proxy } = proxied();
    const { core } = await signUp(platform);
    const list = await core.createList('L');
    await core.list(list).createInvite(); // seq 1
    const rows = await logRows(platform, list);
    expect(rows).toHaveLength(2);

    proxy.tamper.response = headOf(list, { seq: 0, hash: rows[0]!.hash });
    proxy.drop(); // the reconnect brings `group` frames, and every head is checked
    await until('log-rollback', () => warned(core, 'log-rollback'));

    proxy.tamper.response = headOf(list, { seq: 1, hash: toBase64url(randomBytes(32)) });
    proxy.drop();
    await until('log-fork', () => warned(core, 'log-fork'));
  });

  test('shows one device a history the other never had: log-fork once the histories meet', async () => {
    const owner = device();
    const { core: a1, words } = await signUp(owner);
    const list = await a1.createList('L');

    const { platform, proxy } = proxied();
    const a2 = (await ClientCore.signIn(platform, server.url, words, OPTS))!;
    running.push(a2);
    await until('the list on the second device', () => value(a2.lists).some((l) => l.id === list));

    // A validly signed seq 1 the server only shows the second device (the owner signed it, but it was never sent).
    const x = await owner.vault.rotate(await handle(owner), list);
    const real = await logRows(owner, list);
    proxy.tamper.response = logOf(list, [...real, { seq: 1, entry: toBase64url(x.raw), sig: toBase64url(x.sig), hash: b64sha(x.raw) }]);
    proxy.drop();
    await until('the second device took it', async () => (await platform.vault.listState(await handle(platform), list)).head.seq === 1);

    await a1.list(list).createInvite(); // the real seq 1
    proxy.tamper = {};
    proxy.drop();
    await until('log-fork', () => warned(a2, 'log-fork'));
  });

  test('hides a log entry from one device: an update citing it is applied, with log-fork', async () => {
    const owner = device();
    const { core: a1, words } = await signUp(owner);
    const list = await a1.createList('L');

    const { platform, proxy } = proxied();
    const a2 = (await ClientCore.signIn(platform, server.url, words, OPTS))!;
    running.push(a2);
    await until('the list on the second device', () => value(a2.lists).some((l) => l.id === list));

    // The second device keeps being shown the log as it was, while the first one's writes cite the new head.
    proxy.tamper.response = logOf(list, await logRows(owner, list));
    await a1.list(list).createInvite(); // seq 1, withheld
    a1.list(list).addTask('written after it');
    await until('the task', () => titles(a2, list).includes('written after it'));
    await until('log-fork', () => warned(a2, 'log-fork'));
    expect((await platform.vault.listState(await handle(platform), list)).head.seq).toBe(0);
  });

  test('adds a member of its own to a list: the forged entry is refused, nobody is added', async () => {
    const { platform, proxy } = proxied();
    const { core } = await signUp(platform);
    const list = await core.createList('L');
    const [genesis] = await logRows(platform, list);

    // The server's own identity, "added" in the owner's name; it can't make the owner's signature.
    const { publicKey, privateKey } = generateKeyPairSync('ed25519');
    const serverKey = Buffer.from(publicKey.export({ format: 'jwk' }).x!, 'base64url').toString('base64url');
    const raw = Buffer.from(
      JSON.stringify({
        v: 1,
        group: list,
        seq: 1,
        prev: genesis!.hash,
        type: 'add',
        author: core.me,
        subject: serverKey,
        subject_agree: toBase64url(randomBytes(32)),
        epoch: 0,
        envelopes: [{ recipient: serverKey, hpke_ciphertext: toBase64url(randomBytes(80)) }],
        ts: Date.now(),
      }),
    );
    const forged = { seq: 1, entry: raw.toString('base64url'), sig: sign(null, raw, privateKey).toString('base64url'), hash: b64sha(raw) };
    proxy.tamper.response = logOf(list, [genesis!, forged]);
    proxy.drop();
    await until('the refusal', () => logs.some((l) => /InvalidSignature/.test(l)));
    expect((await platform.vault.listState(await handle(platform), list)).members).toHaveLength(1);
    expect(await core.knownContacts()).toEqual([]);
  });

  test('swaps the invite package, or serves another list’s log for it: the join is refused', async () => {
    const alice = device();
    const { core: owner } = await signUp(alice);
    const list = await owner.createList('Groceries');
    const other = await owner.createList('Other');
    const { link } = await owner.list(list).createInvite();

    const { platform, proxy } = proxied();
    const { core: bob } = await signUp(platform);
    proxy.tamper.response = (method, path, res) => {
      if (method !== 'GET' || !/^\/v1\/invites\/[^/]+$/.test(path)) return res;
      const pkg = Buffer.from(data<{ package: string }>(res).package, 'base64url');
      pkg[pkg.length - 1]! ^= 1;
      return { ...res, body: { data: { package: pkg.toString('base64url') } } };
    };
    await expect(bob.invites.open(link)).rejects.toMatchObject({ code: 'bad-package' });

    const otherLog = await logRows(alice, other);
    proxy.tamper.response = (method, path, res) =>
      method === 'GET' && /^\/v1\/invites\/[^/]+\/log/.test(path) ? { ...res, body: { data: otherLog } } : res;
    const opened = await bob.invites.open(link);
    await expect(opened.join()).rejects.toMatchObject({ code: 'bad-package' });
    expect(value(bob.lists).some((l) => l.id === list)).toBe(false);
    expect(value(owner.list(list).content).name).toBe('Groceries');
  });

  test('relays a write under an epoch a removal ended: withheld-removal', async () => {
    const { platform, proxy } = proxied();
    const { core: alice } = await signUp(platform);
    const { core: bob } = await signUp();
    const list = await alice.createList('L');
    await (await bob.invites.open((await alice.list(list).createInvite()).link)).join();

    const updates: Record<string, unknown>[] = [];
    proxy.tamper.toClient = (f) => {
      if (f.t === 'update' && f.list === list) updates.push(f);
      return f;
    };
    bob.list(list).addTask('from epoch 0');
    await until('Alice has it', () => titles(alice, list).includes('from epoch 0'));

    const invite = await alice.list(list).createInvite();
    await alice.list(list).revokeInvite(invite.invitePub); // rotates: epoch 1
    const old = updates.at(-1)!;
    proxy.inject({ ...old, seq: (old.seq as number) + 1_000_000 });
    await until('withheld-removal', () => warned(alice, 'withheld-removal'));
  });

  test('alters content in flight: it is not applied', async () => {
    const { platform, proxy } = proxied();
    const { core: alice } = await signUp(platform);
    const { core: bob } = await signUp();
    const list = await alice.createList('L');
    await (await bob.invites.open((await alice.list(list).createInvite()).link)).join();
    await until('Bob is in', () => value(bob.lists).some((l) => l.id === list));

    proxy.tamper.toClient = (f) => {
      if (f.t !== 'update' || f.list !== list) return f;
      const ct = Buffer.from((f.msg as { ct: string }).ct, 'base64url');
      ct[0]! ^= 1;
      return { ...f, msg: { ...(f.msg as object), ct: ct.toString('base64url') } };
    };
    bob.list(list).addTask('tampered');
    await until('the rejection', () => logs.some((l) => /rejected content .*bad-content/.test(l)));
    expect(titles(alice, list)).not.toContain('tampered');
  });

  test('sees neither names, titles nor the invite secret (network canary)', async () => {
    const { platform, proxy } = proxied();
    const { core: alice } = await signUp(platform);
    const { core: bob } = await signUp();
    const canary = `CANARY-${crypto.randomUUID()}`;
    const list = await alice.createList(canary);
    alice.list(list).addTask(canary);
    const { link } = await alice.list(list).createInvite();
    await (await bob.invites.open(link)).join();
    bob.list(list).addTask(`${canary}-bob`);
    await until('Alice has Bob’s task', () => titles(alice, list).includes(`${canary}-bob`));

    const wire = proxy.seen.join('\n');
    expect(wire).not.toContain(canary);
    expect(wire).not.toContain(link.split('#')[1]);
  });
});
