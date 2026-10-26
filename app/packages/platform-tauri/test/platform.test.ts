import { fromBase64url, toBase64url, VaultError } from '@runer/client-core';
import { describe, expect, it } from 'vitest';
import type { Ipc } from '../src/ipc.js';
import { createTauriPlatform, PageLifecycle, platformKind } from '../src/platform.js';
import { TauriLocalStore } from '../src/store.js';
import { TauriTransport } from '../src/transport.js';
import { TauriVault } from '../src/vault.js';

// The adapters against a fake IPC that plays the Rust commands' contracts.

type Handler = (args: Record<string, unknown>) => unknown;

function fakeIpc(handlers: Record<string, Handler>): Ipc & {
  channels: ((m: unknown) => void)[];
  calls: [string, Record<string, unknown>][];
  events: Map<string, (payload: unknown) => void>;
} {
  const channels: ((m: unknown) => void)[] = [];
  const calls: [string, Record<string, unknown>][] = [];
  const events = new Map<string, (payload: unknown) => void>();
  return {
    channels,
    calls,
    events,
    async invoke<T>(cmd: string, args: Record<string, unknown> = {}) {
      calls.push([cmd, args]);
      const h = handlers[cmd];
      if (h === undefined) throw { code: 'unknown', message: cmd };
      return h(args) as T;
    },
    channel<T>(onMessage: (m: T) => void) {
      channels.push(onMessage as (m: unknown) => void);
      return { id: channels.length };
    },
    async listen<T>(event: string, cb: (payload: T) => void) {
      events.set(event, cb as (payload: unknown) => void);
      return () => void events.delete(event);
    },
  };
}

/** `store_*` as `src-tauri/src/store.rs` implements them, over a sorted map. */
function storeIpc() {
  const rows = new Map<string, string>();
  const k = (t: unknown, key: unknown) => `${t as string}\u0000${key as string}`;
  return fakeIpc({
    store_get: ({ table, key }) => rows.get(k(table, key)) ?? null,
    store_range: ({ table, prefix, after, limit }) => {
      const out: [string, string][] = [];
      for (const [full, v] of [...rows].sort(([a], [b]) => (a < b ? -1 : 1))) {
        const [t, key] = full.split('\u0000') as [string, string];
        if (t !== table || !key.startsWith(prefix as string) || (after !== null && key <= (after as string))) continue;
        if (limit !== null && out.length >= (limit as number)) break;
        out.push([key, v]);
      }
      return out;
    },
    store_write: ({ ops }) => {
      for (const op of ops as { op: string; table: string; key: string; value?: string }[]) {
        if (op.op === 'put') rows.set(k(op.table, op.key), op.value!);
        else rows.delete(k(op.table, op.key));
      }
    },
  });
}

const bytes = (...n: number[]) => new Uint8Array(n);

async function keys(store: TauriLocalStore | Parameters<Parameters<TauriLocalStore['tx']>[0]>[0], prefix: string, opts?: { after?: string; limit?: number }) {
  const out: string[] = [];
  for await (const [key] of store.range('t', prefix, opts)) out.push(key);
  return out;
}

describe('TauriLocalStore', () => {
  it('stores bytes and ranges over a prefix', async () => {
    const store = new TauriLocalStore(storeIpc());
    for (const key of ['a/3', 'a/1', 'b/1', 'a/2']) await store.put('t', key, bytes(1, 2));
    expect(await store.get('t', 'a/1')).toEqual(bytes(1, 2));
    expect(await keys(store, 'a/', { after: 'a/1', limit: 1 })).toEqual(['a/2']);
  });

  it('a transaction reads its own writes and sends them in one batch, or none if it throws', async () => {
    const ipc = storeIpc();
    const store = new TauriLocalStore(ipc);
    await store.put('t', 'a/1', bytes(1));
    await store.put('t', 'a/2', bytes(2));
    await store.tx(async (tx) => {
      await tx.delete('t', 'a/1');
      await tx.put('t', 'a/3', bytes(3));
      expect(await tx.get('t', 'a/1')).toBeNull();
      expect(await keys(tx, 'a/')).toEqual(['a/2', 'a/3']);
      expect(await keys(tx, 'a/', { after: 'a/2', limit: 1 })).toEqual(['a/3']);
    });
    expect(ipc.calls.filter(([c]) => c === 'store_write')).toHaveLength(3);
    expect(await keys(store, 'a/')).toEqual(['a/2', 'a/3']);

    await expect(
      store.tx(async (tx) => {
        await tx.put('t', 'a/9', bytes(9));
        throw new Error('boom');
      }),
    ).rejects.toThrow('boom');
    expect(await store.get('t', 'a/9')).toBeNull();
  });
});

describe('TauriVault', () => {
  it('maps base64url to bytes and the vault error code to VaultError', async () => {
    const ipc = fakeIpc({
      vault_seal_content: ({ coversSeq, body }) => {
        expect(coversSeq).toBe(7);
        expect(fromBase64url(body as string)).toEqual(bytes(9));
        return { header: toBase64url(bytes(1)), nonce: toBase64url(bytes(2)), ct: toBase64url(bytes(3)), sig: toBase64url(bytes(4)) };
      },
      vault_open_contents: () => [{ body: toBase64url(bytes(5)), epoch: 2, author: toBase64url(bytes(6)), coversSeq: null, headKnown: false }, { rejected: 'no-key' }],
      vault_list_state: () => {
        throw { code: 'UnknownList', message: 'UnknownList' };
      },
    });
    const vault = new TauriVault(ipc);
    const sealed = await vault.sealContent('a' as never, 'l', { snapshot: { coversSeq: 7 } }, bytes(9));
    expect(sealed.ct).toEqual(bytes(3));
    expect(await vault.openContents('a' as never, 'l', [sealed])).toEqual([{ body: bytes(5), epoch: 2, author: bytes(6), coversSeq: null, headKnown: false }, { rejected: 'no-key' }]);
    const e = await vault.listState('a' as never, 'l').catch((x: unknown) => x);
    expect(e).toBeInstanceOf(VaultError);
    expect((e as VaultError).code).toBe('UnknownList');
  });

  it('carries invites and identities across as the commands name them', async () => {
    const id = { signPub: toBase64url(bytes(1)), agreePub: toBase64url(bytes(2)) };
    const ipc = fakeIpc({
      vault_create_invite: ({ expires, name, ownerName }) => {
        expect([expires, name, ownerName]).toEqual([99, 'L', 'Alice']);
        const e = toBase64url(bytes(3));
        return { entry: { raw: e, sig: e }, secret: toBase64url(bytes(4)), id: toBase64url(bytes(5)), invitePub: toBase64url(bytes(6)), sealedPackage: toBase64url(bytes(7)) };
      },
      vault_read_invite_package: ({ invite, sealed }) => {
        expect([invite, fromBase64url(sealed as string)]).toEqual(['inv_1', bytes(7)]);
        return { list: 'l', name: 'L', ownerName: 'Alice', owner: id };
      },
      vault_safety_code: ({ signPub, agreePub }) => {
        expect([signPub, agreePub]).toEqual([id.signPub, id.agreePub]);
        return ['00000', '11111', '22222', '33333', '44444', '55555'];
      },
    });
    const vault = new TauriVault(ipc);
    const made = await vault.createInvite('a' as never, 'l', 99, 'L', 'Alice');
    expect([made.secret, made.id, made.invitePub, made.sealedPackage]).toEqual([bytes(4), bytes(5), bytes(6), bytes(7)]);
    expect(await vault.readInvitePackage('inv_1' as never, bytes(7))).toEqual({ list: 'l', name: 'L', ownerName: 'Alice', owner: { signPub: bytes(1), agreePub: bytes(2) } });
    expect(await vault.safetyCode({ signPub: bytes(1), agreePub: bytes(2) })).toHaveLength(6);
  });
});

describe('TauriTransport', () => {
  it('names the server on every call, and holds frames that arrive before the subscriber', async () => {
    const sent: unknown[] = [];
    const ipc = fakeIpc({
      net_request: (a) => ({ status: 200, headers: {}, body: { data: a } }),
      net_socket_open: () => {
        // The Rust side starts reading as soon as the upgrade is done.
        ipc.channels[0]!({ t: 'message', text: '{"t":"group","group":"x","head_seq":0}' });
        return 4;
      },
      net_socket_send: (a) => void sent.push(a),
      net_socket_close: () => undefined,
    });
    const t = new TauriTransport(ipc, 'http://localhost:8080');
    const res = await t.request('POST', '/v1/auth/challenge', { a: 1 });
    expect(res.body).toEqual({ data: { server: 'http://localhost:8080', method: 'POST', path: '/v1/auth/challenge', body: { a: 1 } } });

    const socket = await t.openSocket('/v1/sync');
    const frames: unknown[] = [];
    const closes: number[] = [];
    socket.onClose((c) => closes.push(c.code));
    socket.onMessage((f) => frames.push(f));
    expect(frames).toEqual([{ t: 'group', group: 'x', head_seq: 0 }]);
    socket.send({ t: 'sub', lists: [] });
    await Promise.resolve();
    expect(sent).toEqual([{ id: 4, text: '{"t":"sub","lists":[]}' }]);
    ipc.channels[0]!({ t: 'close', code: 1000, reason: '' });
    expect(closes).toEqual([1000]);
  });
});

describe('platformKind', () => {
  it('tells a phone from a desktop by the user agent', () => {
    expect(platformKind('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)', 0)).toBe('desktop');
    expect(platformKind('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0 Safari/537.36 Edg/140.0', 10)).toBe('desktop');
    expect(platformKind('Mozilla/5.0 (iPhone; CPU iPhone OS 27_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148', 5)).toBe('ios');
    expect(platformKind('Mozilla/5.0 (Linux; Android 16; Pixel 9) AppleWebKit/537.36 Chrome/140.0 Mobile Safari/537.36', 5)).toBe('android');
  });

  it('an iPad that says it is a Mac is still iOS: it has a touch screen', () => {
    expect(platformKind('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko)', 5)).toBe('ios');
  });
});

describe('PageLifecycle', () => {
  it('follows the page being shown and hidden, and stops when unhooked', () => {
    const page = Object.assign(new EventTarget(), { visibilityState: 'visible' as DocumentVisibilityState });
    const life = new PageLifecycle(fakeIpc({}), page);
    const seen: string[] = [];
    const offFore = life.onForeground(() => seen.push('fore'));
    const offBack = life.onBackground(() => seen.push('back'));
    const turn = (state: DocumentVisibilityState) => {
      page.visibilityState = state;
      page.dispatchEvent(new Event('visibilitychange'));
    };
    turn('hidden');
    turn('visible');
    expect(seen).toEqual(['back', 'fore']);
    offFore();
    offBack();
    turn('hidden');
    expect(seen).toEqual(['back', 'fore']);
  });
});

describe('onWake', () => {
  const settle = () => new Promise((r) => setTimeout(r, 0));
  // The debug log lines aren't part of the handshake.
  const handshake = (ipc: { calls: [string, unknown][] }) => ipc.calls.map(([c]) => c).filter((c) => c !== 'debug_log_append');

  it('runs a wake that came before the listener, and one after, and reports each done once the work settles', async () => {
    let pending = true;
    const ipc = fakeIpc({ wake_pending: () => [pending, (pending = false)][0], wake_done: () => undefined });
    const life = new PageLifecycle(ipc, Object.assign(new EventTarget(), { visibilityState: 'hidden' as DocumentVisibilityState }));
    let finish!: () => void;
    let runs = 0;
    life.onWake(() => {
      runs++;
      return new Promise<void>((r) => (finish = r));
    });
    await settle();
    expect(runs).toBe(1);
    expect(handshake(ipc)).toEqual(['wake_pending']);
    finish();
    await settle();
    expect(handshake(ipc)).toEqual(['wake_pending', 'wake_done']);

    pending = true;
    ipc.events.get('runer://wake')!(null);
    await settle();
    expect(runs).toBe(2);
    finish();
    await settle();
    expect(handshake(ipc)).toEqual(['wake_pending', 'wake_done', 'wake_pending', 'wake_done']);
  });

  it('runs a wake once when its event comes after the start claimed it', async () => {
    let pending = true;
    const ipc = fakeIpc({ wake_pending: () => [pending, (pending = false)][0], wake_done: () => undefined });
    const life = new PageLifecycle(ipc, Object.assign(new EventTarget(), { visibilityState: 'hidden' as DocumentVisibilityState }));
    let runs = 0;
    life.onWake(async () => void runs++);
    await settle();
    // The event for the wake the start already claimed.
    ipc.events.get('runer://wake')!(null);
    await settle();
    expect(runs).toBe(1);
    expect(handshake(ipc).filter((c) => c === 'wake_done')).toHaveLength(1);
  });

  it('reports done when the work fails, and runs nothing without a wake', async () => {
    let pending = false;
    const ipc = fakeIpc({ wake_pending: () => [pending, (pending = false)][0], wake_done: () => undefined });
    const life = new PageLifecycle(ipc, Object.assign(new EventTarget(), { visibilityState: 'hidden' as DocumentVisibilityState }));
    let runs = 0;
    life.onWake(async () => {
      runs++;
      throw new Error('sync did not settle');
    });
    await settle();
    expect(runs).toBe(0);
    pending = true;
    ipc.events.get('runer://wake')!(null);
    await settle();
    expect(runs).toBe(1);
    expect(handshake(ipc)).toEqual(['wake_pending', 'wake_pending', 'wake_done']);
  });
});

describe('TauriNotifier', () => {
  it("sends each reminder's time with the device's UTC offset at that time", async () => {
    const ipc = fakeIpc({ notify_replace_all: () => undefined });
    const at = new Date('2026-10-08T12:00:00Z');
    await createTauriPlatform('http://localhost:8080', ipc).notifications.replaceAll([{ id: 'l:1', at, title: 'Call Anna', body: 'Home' }]);
    expect(ipc.calls).toEqual([['notify_replace_all', { reminders: [{ at: at.getTime(), offset: -at.getTimezoneOffset(), title: 'Call Anna', body: 'Home' }] }]]);
  });
});
