/**
 * The sync engine (Sync protocol): one WebSocket per
 * account, every list subscribed, catch-up from the stored `since`, the
 * outbox, snapshots, and a Yjs document per list that the UI edits through
 * `ListDoc`. Membership comes from the vault's verified logs; `group`
 * frames, reconnects and an hourly timer bring them up to date.
 *
 * Everything that changes state runs one step at a time on a single queue
 * (`run`), in frame order, so the handlers never interleave.
 */

import * as Y from 'yjs';
import { loginRefused, type Account } from '../account/account.js';
import { ListDoc } from '../list/doc.js';
import type { Socket } from '../platform/index.js';
import { Writable, type Readable } from '../readable.js';
import { toBase64url } from '../base64url.js';
import { joinInvite, type OpenedInvite } from '../invite/join.js';
import type { ListId, ListState, OpenedContent } from '../platform/vault.js';
import { announceList, catchUp, deleteList, newList, refreshLists } from './logs.js';
import { leaveList, rotateIfDue, rotationDue } from './members.js';
import { ContentStore, fromWire, toWire, type OutboxItem, type WireEntry, type WireMsg } from './store.js';

export type ConnectionState = 'offline' | 'connecting' | 'online' | 'signed-out';

export interface SyncOptions {
  /** Sync §7: snapshot after this many updates or bytes since the last snapshot. */
  snapshotAfter?: { updates: number; bytes: number };
  /** Sync §7: the random wait before uploading a snapshot. */
  snapshotDelayMs?: () => number;
  /** Sync §2: reconnect backoff. */
  backoff?: { firstMs: number; maxMs: number; jitterMs: number };
  /** E2EE §4.4: refetch every list's head at least this often. */
  headRefreshMs?: number;
  /** How long a background wake may take to catch up (`wake`). */
  wakeMs?: number;
  /** Where to report content the vault rejected and frames that can't be used. */
  log?: (message: string) => void;
}

/** Marks Yjs transactions that came from the server or the cache, so they aren't sent back. */
const REMOTE = Symbol('remote');

/** Server → client frames (Sync §3). */
type Frame =
  | { t: 'snapshot'; list: ListId; epoch: number; covers_seq: number; msg: WireMsg }
  | { t: 'update'; list: ListId; seq: number; msg: WireMsg }
  | { t: 'synced'; list: ListId; head: number }
  | { t: 'ack'; cid: string; seq: number }
  | { t: 'error'; cid?: string; code: string; detail?: string }
  | { t: 'group'; group: ListId; head_seq: number }
  | { t: 'bye'; reason: string };

class Replica {
  readonly doc = new ListDoc();
  since = 0;
  /** `synced` arrived for it on the current connection, and it wasn't unsubscribed since (Sync §4, ordering per connection). */
  synced = false;
  /** Between `sub` and `synced`: frames wait in `held` and are opened in one batch. */
  catchingUp = false;
  held: { snapshot?: { coversSeq: number; msg: WireMsg }; updates: { seq: number; msg: WireMsg }[] } = { updates: [] };
  /** Local Yjs updates not yet sealed. */
  local: Uint8Array[] = [];
  /** The newest snapshot this device knows of, and what has accumulated since (Sync §7). */
  snapshotCovers = 0;
  sinceSnapshot = { updates: 0, bytes: 0 };
  snapshotTimer: ReturnType<typeof setTimeout> | undefined;
  /** False until the stored content opened (the vault may need the log from the server first). */
  loaded = false;

  constructor(readonly list: ListId) {}

  apply(opened: OpenedContent[]): void {
    this.doc.doc.transact(() => {
      for (const o of opened) if ('body' in o) Y.applyUpdate(this.doc.doc, o.body, REMOTE);
    }, REMOTE);
  }

  counted(updates: { msg: WireMsg }[]): void {
    this.sinceSnapshot.updates += updates.length;
    for (const u of updates) this.sinceSnapshot.bytes += u.msg.ct.length;
  }
}

const DEFAULTS = {
  snapshotAfter: { updates: 200, bytes: 1024 * 1024 },
  snapshotDelayMs: () => Math.random() * 30_000,
  backoff: { firstMs: 1000, maxMs: 30_000, jitterMs: 1000 },
  headRefreshMs: 60 * 60 * 1000,
  wakeMs: 20_000,
  log: () => undefined,
} satisfies Required<SyncOptions>;

let cidCounter = 0;
const newCid = () => `${Date.now().toString(36).padStart(9, '0')}-${(cidCounter++).toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

export class Sync {
  readonly store: ContentStore;
  private readonly opts: Required<SyncOptions>;
  private readonly replicas = new Map<ListId, Replica>();
  private readonly state = new Writable<ConnectionState>('offline');
  private readonly listeners = new Set<(list: ListId | null) => void>();
  private socket: Socket | null = null;
  /** Sent and not yet acknowledged, by `cid`. Snapshots carry what they cover. */
  private readonly inflight = new Map<string, OutboxItem & { coversSeq?: number }>();
  /** Lists made here that the server hasn't accepted yet, with their genesis. They aren't subscribed and nothing of theirs is sent until it has. */
  private readonly unannounced = new Map<ListId, WireEntry>();
  /** Lists being left on this device: our own leave makes the server drop them, which is no news to us. */
  private readonly leaving = new Set<ListId>();
  private queue: Promise<void> = Promise.resolve();
  private reconcileQueued = false;
  private attempt = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  private refreshTimer: ReturnType<typeof setInterval> | undefined;
  private readonly unhook: (() => void)[] = [];
  private stopped = true;
  /** Mobile, in the background: the socket stays closed except during a wake. */
  private hidden = false;

  constructor(
    readonly account: Account,
    opts: SyncOptions = {},
  ) {
    this.opts = { ...DEFAULTS, ...opts };
    this.store = new ContentStore(account.platform.storage, account.handle);
  }

  get connection(): Readable<ConnectionState> {
    return this.state;
  }

  /** Lists on this device, in no particular order. */
  lists(): ListId[] {
    return [...this.replicas.keys()];
  }

  /** The list's document; edits made through it are sealed and sent. */
  doc(list: ListId): ListDoc | undefined {
    return this.replicas.get(list)?.doc;
  }

  /** The list's content as the server has it reached this device, on this connection or an earlier one (then it is in the cache). */
  hasContent(list: ListId): boolean {
    const r = this.replicas.get(list);
    return r !== undefined && (r.synced || r.since > 0);
  }

  /** Calls `cb` when a list's content changes (with its id) or the set of lists changes (`null`). */
  onChange(cb: (list: ListId | null) => void): () => void {
    this.listeners.add(cb);
    return () => void this.listeners.delete(cb);
  }

  /**
   * Startup (Sync §8): renders every stored list from the cache straight
   * away, then connects. Works offline; edits queue in the outbox.
   */
  async start(): Promise<void> {
    this.stopped = false;
    await this.run(async () => {
      for (const [list, genesis] of await this.store.created()) this.unannounced.set(list, genesis);
      for (const list of await this.store.lists()) await this.addReplica(list);
    });
    const { lifecycle, info } = this.account.platform;
    const mobile = info.kind === 'ios' || info.kind === 'android';
    this.unhook.push(
      this.state.subscribe((s) => this.opts.log(`sync: ${s}`)),
      lifecycle.onOnline(() => this.reconnectNow()),
      lifecycle.onForeground(() => {
        this.opts.log('sync: foreground');
        this.hidden = false;
        this.reconnectNow();
        this.reconcileSoon();
      }),
    );
    // Mobile: close the socket in the background and keep it closed until the foreground or a wake.
    if (mobile)
      this.unhook.push(
        lifecycle.onBackground(() => {
          this.opts.log('sync: background, closing the socket');
          this.hidden = true;
          this.socket?.close();
        }),
      );
    this.refreshTimer = setInterval(() => this.reconcileSoon(), this.opts.headRefreshMs);
    void this.connect();
  }

  /** Closes the socket and stops every timer. The outbox stays for next time. */
  async stop(): Promise<void> {
    this.stopped = true;
    clearTimeout(this.reconnectTimer);
    clearInterval(this.refreshTimer);
    for (const off of this.unhook.splice(0)) off();
    for (const r of this.replicas.values()) clearTimeout(r.snapshotTimer);
    this.socket?.close();
    this.socket = null;
    await this.queue;
  }

  /** Sign-out: stops, deletes this account's content here, then signs out of the account. */
  async signOut(): Promise<void> {
    await this.stop();
    await this.store.clear();
    await this.account.signOut();
    this.state.set('signed-out');
  }

  /** Creates a list with this name; it is usable at once, also offline, and syncs like any other once the server has it. */
  async createList(name: string): Promise<ListId> {
    const { state, genesis } = await newList(this.account);
    const { id } = state;
    await this.run(async () => {
      await this.store.create(id, genesis);
      this.unannounced.set(id, genesis);
      const r = await this.addReplica(id);
      r.doc.setName(name);
      // Known to be offline: don't wait on a request that can't get through; the next connect announces it.
      if (this.state.value !== 'offline') await this.announce();
    });
    return id;
  }

  /**
   * Starts syncing a list whose log the vault has verified but this device
   * doesn't hold yet, without waiting for the server's `group` frame: the
   * Inbox right after sign-up or sign-in.
   */
  async track(list: ListId): Promise<void> {
    await this.run(async () => {
      if (this.replicas.has(list)) return;
      await this.store.track(list);
      this.subscribe([await this.addReplica(list)]);
    });
  }

  /** Owner only: deletes the list for everyone. */
  async deleteList(list: ListId): Promise<void> {
    await deleteList(this.account, list);
    await this.run(() => this.forget(list));
  }

  /** Any member but the owner: leaves the list and forgets it here. */
  async leaveList(list: ListId): Promise<void> {
    this.leaving.add(list);
    try {
      await leaveList(this.account, list);
      await this.run(() => this.forget(list));
    } finally {
      this.leaving.delete(list);
    }
  }

  /** Joins from an opened link invite and starts syncing the list. */
  async join(opened: OpenedInvite): Promise<ListId> {
    const list = await joinInvite(this.account, opened);
    await this.track(list);
    return list;
  }

  /**
   * Resolves once nothing is in flight: connected, every list caught up, the
   * outbox empty and no list refresh waiting. For tests and "all saved" indicators.
   */
  async settled(timeoutMs = 10_000): Promise<void> {
    const end = Date.now() + timeoutMs;
    for (;;) {
      const queue = this.queue;
      await queue;
      const busy =
        this.state.value !== 'online' ||
        this.reconcileQueued ||
        this.unannounced.size > 0 ||
        this.inflight.size > 0 ||
        [...this.replicas.values()].some((r) => r.catchingUp || r.local.length > 0) ||
        (await this.store.outbox()).length > 0;
      // Something queued meanwhile (a connect that just came up subscribes on the queue): look again.
      if (!busy && queue === this.queue) return;
      if (Date.now() > end) {
        const r = [...this.replicas.values()];
        this.opts.log(
          `sync: not settled: state ${this.state.value}, reconcile ${this.reconcileQueued}, unannounced ${this.unannounced.size}, ` +
            `inflight ${this.inflight.size}, catching up ${r.filter((x) => x.catchingUp).length}/${r.length}, ` +
            `local edits ${r.filter((x) => x.local.length > 0).length}, outbox ${(await this.store.outbox()).length}`,
        );
        throw new Error('sync did not settle');
      }
      await new Promise((r) => setTimeout(r, 20));
    }
  }

  /**
   * A background wake (iOS): connect, catch up and reconcile as on coming
   * to the foreground, wait until settled, then close again if still in the
   * background. Rejects if it doesn't settle within `wakeMs`.
   */
  async wake(): Promise<void> {
    const started = Date.now();
    const took = () => `${((Date.now() - started) / 1000).toFixed(1)} s`;
    this.opts.log('wake: start');
    this.reconnectNow();
    this.reconcileSoon();
    try {
      await this.settled(this.opts.wakeMs);
      this.opts.log(`wake: done in ${took()}`);
    } catch (e) {
      this.opts.log(`wake: failed after ${took()}: ${e instanceof Error ? e.message : String(e)}`);
      throw e;
    } finally {
      if (this.hidden) {
        this.opts.log('wake: still in the background, closing the socket');
        this.socket?.close();
      }
    }
  }

  // --- the queue -------------------------------------------------------

  /** Runs `fn` after everything queued before it. A failure is logged and doesn't stop the queue. */
  private run(fn: () => Promise<void>): Promise<void> {
    const next = this.queue.then(fn).catch((e: unknown) => this.opts.log(`sync: ${e instanceof Error ? e.message : String(e)}`));
    this.queue = next;
    return next;
  }

  private changed(list: ListId | null): void {
    for (const cb of [...this.listeners]) cb(list);
  }

  // --- lists -----------------------------------------------------------

  private async addReplica(list: ListId): Promise<Replica> {
    const r = new Replica(list);
    this.replicas.set(list, r);
    r.doc.doc.on('update', (update: Uint8Array, origin: unknown) => {
      if (origin === REMOTE) return;
      if (r.local.push(update) === 1) queueMicrotask(() => void this.run(() => this.seal(r)));
    });
    r.doc.subscribe(() => this.changed(list));
    await this.load(r);
    this.changed(null);
    return r;
  }

  /** Opens the stored content and the outbox into the document. */
  private async load(r: Replica): Promise<void> {
    const c = await this.store.load(r.list);
    const outbox = await this.store.outbox(r.list);
    const msgs = [...c.snapshots.map((s) => s.msg), ...c.updates.map((u) => u.msg), ...outbox.map((o) => o.msg)];
    let opened: OpenedContent[];
    try {
      opened = await this.account.platform.vault.openContents(this.account.handle, r.list, msgs.map(fromWire));
    } catch {
      return; // the vault has no verified log for it yet; the next reconcile fetches it and loads again
    }
    r.apply(opened);
    r.since = c.since;
    r.snapshotCovers = Math.max(0, ...c.snapshots.map((s) => s.coversSeq));
    r.counted(c.updates.filter((u) => u.seq > r.snapshotCovers));
    r.loaded = true;
  }

  private async forget(list: ListId): Promise<void> {
    const r = this.replicas.get(list);
    if (r !== undefined) clearTimeout(r.snapshotTimer);
    this.replicas.delete(list);
    this.unannounced.delete(list);
    for (const [cid, item] of this.inflight) if (item.list === list) this.inflight.delete(cid);
    this.socket?.send({ t: 'unsub', lists: [{ list }] });
    await this.store.forget(list);
    await this.account.platform.vault.forgetList(this.account.handle, list).catch(() => undefined);
    this.changed(null);
  }

  /**
   * Tells the server about the lists made here that it doesn't have yet,
   * then subscribes them and sends what was written in them meanwhile. One
   * that fails stays for the next connect or reconcile.
   */
  private async announce(): Promise<void> {
    for (const [list, genesis] of [...this.unannounced]) {
      try {
        await announceList(this.account, genesis);
      } catch (e) {
        this.opts.log(`sync: creating ${list} on the server failed: ${e instanceof Error ? e.message : String(e)}`);
        continue;
      }
      await this.store.announced(list);
      this.unannounced.delete(list);
      const r = this.replicas.get(list);
      if (r === undefined) continue;
      this.subscribe([r]);
      for (const item of await this.store.outbox(list)) this.send(item);
    }
  }

  /** A `group` frame, a reconnect or the hourly timer: verify every log against the server (E2EE §4.4). */
  private reconcileSoon(): void {
    if (this.reconcileQueued) return;
    this.reconcileQueued = true;
    void this.run(async () => {
      this.reconcileQueued = false;
      await this.reconcile();
    });
  }

  /**
   * Brings every log up to date, drops lists the server no longer lists for
   * us (removed, left, deleted), and adds and subscribes new ones. A list
   * whose log can't be fetched or verified keeps its last verified state and
   * is tried again next time; it doesn't hold up the others.
   */
  private async reconcile(): Promise<void> {
    await this.announce();
    const { listed, states } = await refreshLists(this.account, (list, e) =>
      this.opts.log(`sync: refreshing ${list} failed: ${e instanceof Error ? e.message : String(e)}`),
    );
    for (const list of [...this.replicas.keys()]) {
      if (listed.has(list) || this.unannounced.has(list)) continue;
      const left = this.leaving.has(list);
      const name = this.replicas.get(list)?.doc.name ?? '';
      const owner = await this.account.platform.vault.listState(this.account.handle, list).then(
        (s) => toBase64url(s.owner.signPub),
        () => null,
      );
      await this.forget(list);
      // Someone else's list: deleted, or we were removed; which is not for us to know. Our own just goes.
      if (!left && owner !== null && owner !== toBase64url(this.account.identity.signPub)) this.account.lostAccess(list, name);
    }
    const fresh: Replica[] = [];
    for (const s of states) {
      const r = this.replicas.get(s.id);
      if (r === undefined) {
        await this.store.track(s.id);
        fresh.push(await this.addReplica(s.id));
      } else if (!r.loaded) {
        await this.load(r);
        fresh.push(r);
      }
    }
    this.subscribe(fresh);
    await this.rotateDue(states);
    // Membership may have changed without the set of lists changing.
    this.changed(null);
  }

  /** After a leave, a revoked link or an unused one that expired, someone outside may hold the key (E2EE §6.1, §8.2). */
  private async rotateDue(states: ListState[]): Promise<void> {
    const me = toBase64url(this.account.identity.signPub);
    const now = Date.now();
    for (const s of states) {
      if (!rotationDue(s, me, now)) continue;
      await rotateIfDue(this.account, s.id, now).catch((e: unknown) =>
        this.opts.log(`sync: rotating ${s.id} failed: ${e instanceof Error ? e.message : String(e)}`),
      );
    }
  }

  // --- connection ------------------------------------------------------

  private async connect(): Promise<void> {
    if (this.stopped || this.socket !== null || this.state.value === 'signed-out') return;
    this.state.set('connecting');
    const started = Date.now();
    let socket: Socket;
    try {
      socket = await this.account.platform.transport.openSocket('/v1/sync');
    } catch (e) {
      this.opts.log(`sync: socket failed (${e instanceof Error ? e.message : String(e)}), signing in again`);
      // Perhaps the session expired: any request signs in again (Account.request), then retry.
      // A refused login means the key changed or the account is gone: stop until the user signs in again.
      let refused = false;
      const reauthed = await this.account.request('GET', '/v1/account').then(
        () => true,
        (e: unknown) => {
          refused = loginRefused(e);
          return false;
        },
      );
      this.opts.log(`sync: signing in again ${reauthed ? 'worked' : refused ? 'refused' : 'failed'}`);
      if (refused) return this.state.set('signed-out');
      try {
        if (!reauthed || this.stopped) throw new Error('offline');
        socket = await this.account.platform.transport.openSocket('/v1/sync');
      } catch (e) {
        this.opts.log(`sync: socket failed again: ${e instanceof Error ? e.message : String(e)}`);
        this.state.set('offline');
        return this.reconnectLater();
      }
    }
    if (this.stopped) return socket.close();
    this.opts.log(`sync: socket open in ${Date.now() - started} ms`);
    this.attempt = 0;
    this.socket = socket;
    socket.onMessage((f) => void this.run(() => this.handle(f as Frame)));
    socket.onClose(() => {
      if (this.socket !== socket) return;
      this.opts.log('sync: socket closed');
      this.socket = null;
      this.inflight.clear();
      for (const r of this.replicas.values()) r.catchingUp = r.synced = false;
      if (this.state.value !== 'signed-out') {
        this.state.set('offline');
        this.reconnectLater();
      }
    });
    this.state.set('online');
    void this.run(async () => {
      this.subscribe([...this.replicas.values()].filter((r) => r.loaded));
      for (const item of await this.store.outbox()) this.send(item);
      await this.announce();
    });
  }

  /** Sync §2: 1 s, 2 s, 4 s … capped at 30 s, plus up to 1 s of jitter. */
  private reconnectLater(): void {
    if (this.stopped) return;
    if (this.hidden) return this.opts.log('sync: in the background, not reconnecting');
    const { firstMs, maxMs, jitterMs } = this.opts.backoff;
    const delay = Math.min(maxMs, firstMs * 2 ** this.attempt++) + Math.random() * jitterMs;
    clearTimeout(this.reconnectTimer);
    this.reconnectTimer = setTimeout(() => void this.connect(), delay);
  }

  private reconnectNow(): void {
    if (this.stopped || this.socket !== null) return;
    clearTimeout(this.reconnectTimer);
    this.attempt = 0;
    void this.connect();
  }

  private subscribe(all: Replica[]): void {
    const replicas = all.filter((r) => !this.unannounced.has(r.list));
    if (this.socket === null || replicas.length === 0) return;
    for (const r of replicas) {
      r.catchingUp = true;
      r.synced = false;
      r.held = { updates: [] };
    }
    this.opts.log(`sync: subscribing to ${replicas.length} lists`);
    // At most 500 subscriptions per connection (Sync §6); lists are few.
    this.socket.send({ t: 'sub', lists: replicas.map((r) => ({ list: r.list, since: r.since })) });
  }

  // --- outgoing --------------------------------------------------------

  /** Seals the local edits made since the last call as one update, keeps it in the outbox, and sends it. */
  private async seal(r: Replica): Promise<void> {
    if (r.local.length === 0 || !this.replicas.has(r.list)) return;
    const update = Y.mergeUpdates(r.local.splice(0));
    const sealed = await this.account.platform.vault.sealContent(this.account.handle, r.list, 'update', update);
    const item: OutboxItem = { cid: newCid(), list: r.list, msg: toWire(sealed) };
    await this.store.queue(item);
    this.send(item);
  }

  private send(item: OutboxItem & { coversSeq?: number }): void {
    if (this.socket === null || this.unannounced.has(item.list)) return;
    this.inflight.set(item.cid, item);
    this.socket.send({ t: item.coversSeq === undefined ? 'update' : 'snapshot', cid: item.cid, ...item.msg });
  }

  /** Sync §7: after enough has accumulated, and after a random wait, upload the full state. */
  private maybeSnapshot(r: Replica): void {
    const { updates, bytes } = this.opts.snapshotAfter;
    if (r.catchingUp || r.snapshotTimer !== undefined || r.since <= r.snapshotCovers) return;
    if (r.sinceSnapshot.updates < updates && r.sinceSnapshot.bytes < bytes) return;
    r.snapshotTimer = setTimeout(
      () =>
        void this.run(async () => {
          r.snapshotTimer = undefined;
          if (this.socket === null || !this.replicas.has(r.list)) return;
          // Covers what was delivered in order; the state may hold more (our own edits), which merging tolerates.
          const coversSeq = r.since;
          const body = Y.encodeStateAsUpdate(r.doc.doc);
          const sealed = await this.account.platform.vault.sealContent(this.account.handle, r.list, { snapshot: { coversSeq } }, body);
          this.send({ cid: newCid(), list: r.list, msg: toWire(sealed), coversSeq });
        }),
      this.opts.snapshotDelayMs(),
    );
  }

  // --- incoming --------------------------------------------------------

  private async handle(f: Frame): Promise<void> {
    switch (f.t) {
      case 'snapshot': {
        const r = this.replicas.get(f.list);
        if (r?.catchingUp) r.held.snapshot = { coversSeq: f.covers_seq, msg: f.msg };
        return;
      }
      case 'update': {
        const r = this.replicas.get(f.list);
        if (r === undefined || f.seq <= r.since) return;
        if (r.catchingUp) r.held.updates.push({ seq: f.seq, msg: f.msg });
        else {
          this.opts.log(`sync: live update ${f.seq} for ${f.list}`);
          await this.live(r, f.seq, f.msg);
        }
        return;
      }
      case 'synced': {
        const r = this.replicas.get(f.list);
        if (r?.catchingUp) {
          this.opts.log(
            `sync: ${f.list} caught up to ${f.head}: ${r.held.updates.length} updates${r.held.snapshot ? ', a snapshot' : ''}`,
          );
          await this.caughtUp(r, f.head);
        }
        return;
      }
      case 'ack':
        return this.acked(f.cid, f.seq);
      case 'error':
        return this.failed(f);
      case 'group':
        this.opts.log(`sync: group ${f.group} changed`);
        return this.reconcileSoon();
      case 'bye':
        this.opts.log(`sync: bye ${f.reason}`);
        // session-revoked: the reconnect logs in again, which a key change refuses. remove / leave: it refetches the lists.
        return;
    }
  }

  /** Catch-up is complete: open everything it brought in one batch (Sync §4). */
  private async caughtUp(r: Replica, head: number): Promise<void> {
    const { snapshot, updates } = r.held;
    r.held = { updates: [] };
    r.catchingUp = false;
    r.synced = true;
    const fresh = updates.filter((u) => u.seq > r.since);
    const msgs = [...(snapshot === undefined ? [] : [snapshot.msg]), ...fresh.map((u) => u.msg)];
    if (msgs.length > 0) r.apply(await this.open(r.list, msgs));
    const since = Math.max(r.since, head);
    await this.store.delivered(r.list, since, fresh, snapshot);
    r.since = since;
    if (snapshot !== undefined && snapshot.coversSeq > r.snapshotCovers) {
      r.snapshotCovers = snapshot.coversSeq;
      r.sinceSnapshot = { updates: 0, bytes: 0 };
    }
    r.counted(fresh.filter((u) => u.seq > r.snapshotCovers));
    this.maybeSnapshot(r);
    // Also when it brought nothing: the list is known to be up to date from here (`hasContent`).
    this.changed(r.list);
  }

  /** A live update. One with an older epoch than ours is applied, with a warning (E2EE §4.4, §7). */
  private async live(r: Replica, seq: number, msg: WireMsg): Promise<void> {
    const [opened] = await this.open(r.list, [msg]);
    if (opened !== undefined && 'body' in opened) {
      r.apply([opened]);
      const { epoch } = await this.account.platform.vault.listState(this.account.handle, r.list);
      if (opened.epoch < epoch) {
        this.account.warn([{ kind: 'withheld-removal', list: r.list }]);
        this.reconcileSoon();
      }
    }
    await this.store.delivered(r.list, seq, [{ seq, msg }]);
    r.since = seq;
    r.counted([{ msg }]);
    this.maybeSnapshot(r);
  }

  /**
   * Opens content in one vault call. Content under an epoch, by a member,
   * or citing a log head the verified log doesn't know yet means the log
   * is behind: catch it up and try those once more. A head still unknown
   * after that is one the server doesn't show us: the content is applied,
   * with a `log-fork` warning (E2EE §4.4).
   */
  private async open(list: ListId, msgs: WireMsg[]): Promise<OpenedContent[]> {
    const { vault } = this.account.platform;
    const opened = await vault.openContents(this.account.handle, list, msgs.map(fromWire));
    const behind = (o: OpenedContent) => ('rejected' in o ? o.rejected === 'no-key' || o.rejected === 'not-member' : !o.headKnown);
    const retry = opened.flatMap((o, i) => (behind(o) ? [i] : []));
    if (retry.length > 0) {
      await catchUp(this.account, list);
      const again = await vault.openContents(this.account.handle, list, retry.map((i) => fromWire(msgs[i]!)));
      retry.forEach((i, j) => (opened[i] = again[j]!));
    }
    for (const o of opened) if ('rejected' in o) this.opts.log(`sync: rejected content in ${list}: ${o.rejected}`);
    // Once per list: every later update from that member cites the same history.
    const warned = this.account.warnings.value.some((w) => w.kind === 'log-fork' && w.list === list);
    if (!warned && opened.some((o) => 'body' in o && !o.headKnown)) this.account.warn([{ kind: 'log-fork', list }]);
    return opened;
  }

  private async acked(cid: string, seq: number): Promise<void> {
    const item = this.inflight.get(cid);
    if (item === undefined) return;
    this.inflight.delete(cid);
    const r = this.replicas.get(item.list);
    if (item.coversSeq !== undefined) {
      await this.store.compact(item.list, item.coversSeq, item.msg);
      if (r !== undefined && item.coversSeq > r.snapshotCovers) {
        r.snapshotCovers = item.coversSeq;
        r.sinceSnapshot = { updates: 0, bytes: 0 };
      }
      return;
    }
    // Sync §4, ordering per connection: once `synced` for the list has
    // arrived on this connection, an ack comes after every update to the
    // list with a lower seq, so `since` may move to ours. Otherwise a device
    // that writes alone would never move it, nor ever snapshot.
    const advance = r !== undefined && r.synced && seq > r.since;
    await this.store.acked(item, seq, advance ? seq : undefined);
    if (r !== undefined) {
      if (advance) r.since = seq;
      r.counted([item]);
      this.maybeSnapshot(r);
    }
  }

  private async failed(f: Extract<Frame, { t: 'error' }>): Promise<void> {
    const item = f.cid === undefined ? undefined : this.inflight.get(f.cid);
    if (item === undefined) {
      // Not about one of our writes: a subscription was refused or its catch-up failed.
      this.opts.log(`sync: ${f.code} ${f.detail ?? ''}`);
      if (f.code === 'forbidden' || f.code === 'bad-request') this.reconcileSoon();
      return;
    }
    this.inflight.delete(item.cid);
    if (item.coversSeq !== undefined) return; // a snapshot is only a cache; the next one will do
    switch (f.code) {
      case 'stale-epoch':
        return this.reseal(item.list);
      case 'rate-limited':
        setTimeout(() => void this.run(async () => this.send(item)), 1000);
        return;
      case 'forbidden':
        // Not a member any more, the list is gone, or our identity changed: the write can never land.
        await this.store.dropOutbox(item);
        this.reconcileSoon();
        return;
      default:
        // too-large, bad-request: a bug or an oversized edit; the edit stays in the document and in the next snapshot.
        this.opts.log(`sync: ${f.code} for an update to ${item.list}; dropped`);
        await this.store.dropOutbox(item);
    }
  }

  /** Sync §5 `stale-epoch`: fetch the log, then re-seal the list's outbox under the new epoch and resend. */
  private async reseal(list: ListId): Promise<void> {
    const { vault } = this.account.platform;
    const { epoch } = await catchUp(this.account, list);
    const items = await this.store.outbox(list);
    const opened = await vault.openContents(this.account.handle, list, items.map((i) => fromWire(i.msg)));
    const old: OutboxItem[] = [];
    const fresh: OutboxItem[] = [];
    for (const [i, o] of opened.entries()) {
      if (!('body' in o) || o.epoch >= epoch) continue;
      old.push(items[i]!);
      fresh.push({ cid: newCid(), list, msg: toWire(await vault.sealContent(this.account.handle, list, 'update', o.body)) });
    }
    await this.store.requeue(old, fresh);
    for (const o of old) this.inflight.delete(o.cid);
    // Items already under the current epoch were rejected by a server that is ahead of the log it serves; they go again on reconnect.
    for (const item of fresh) this.send(item);
  }
}
