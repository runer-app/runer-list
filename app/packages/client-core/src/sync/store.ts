/**
 * List content on this device (client-spec §9, Sync §4): the server's
 * ciphertext as delivered, the highest `seq` held per list, and the outbox.
 * Everything here is ciphertext; plaintext lives only in memory.
 *
 * Tables in `LocalStore`, keys prefixed by the account's vault handle:
 * - `content`: `<acct>/<list>/u/<seq>` an update's `msg`, `<acct>/<list>/s/<covers_seq>` a snapshot's
 * - `cursor`:  `<acct>/<list>` the highest `seq` delivered (`since` for the next `sub`)
 * - `outbox`:  `<acct>/<list>/<cid>` a sealed update not yet acknowledged
 * - `created`: `<acct>/<list>` the genesis of a list made here that the server hasn't accepted yet
 */

import { fromBase64url, toBase64url } from '../base64url.js';
import type { LocalStore } from '../platform/index.js';
import type { ListId, SealedContent } from '../platform/vault.js';

/** `{header, nonce, ct, sig}` as base64url strings, exactly as frames carry them (Sync §3). */
export interface WireMsg {
  header: string;
  nonce: string;
  ct: string;
  sig: string;
}

/** A log entry as requests carry it. */
export interface WireEntry {
  entry: string;
  sig: string;
}

export interface OutboxItem {
  cid: string;
  list: ListId;
  msg: WireMsg;
}

/** A list's stored content, oldest first: snapshots (by `covers_seq`), then updates (by `seq`). */
export interface StoredContent {
  since: number;
  snapshots: { coversSeq: number; msg: WireMsg }[];
  updates: { seq: number; msg: WireMsg }[];
}

const enc = new TextEncoder();
const dec = new TextDecoder();
const toBytes = (v: unknown) => enc.encode(JSON.stringify(v));
const fromBytes = <T>(b: Uint8Array) => JSON.parse(dec.decode(b)) as T;

/** 16 digits hold every seq up to 2^53, so key order is seq order. */
const pad = (n: number) => String(n).padStart(16, '0');

export const toWire = (m: SealedContent): WireMsg => ({
  header: toBase64url(m.header),
  nonce: toBase64url(m.nonce),
  ct: toBase64url(m.ct),
  sig: toBase64url(m.sig),
});

export const fromWire = (m: WireMsg): SealedContent => ({
  header: fromBase64url(m.header),
  nonce: fromBase64url(m.nonce),
  ct: fromBase64url(m.ct),
  sig: fromBase64url(m.sig),
});

export class ContentStore {
  constructor(
    private readonly store: LocalStore,
    /** The account's vault handle. */
    private readonly account: string,
  ) {}

  /** Lists with content or a cursor on this device. */
  async lists(): Promise<ListId[]> {
    const out: ListId[] = [];
    for await (const [key] of this.store.range('cursor', `${this.account}/`)) out.push(key.slice(this.account.length + 1));
    return out;
  }

  /** Makes the list known here (cursor 0) if it isn't yet. */
  async track(list: ListId): Promise<void> {
    if ((await this.store.get('cursor', this.cursorKey(list))) === null) await this.store.put('cursor', this.cursorKey(list), toBytes(0));
  }

  /** A list made here: known at once, with its genesis kept until the server has it. */
  async create(list: ListId, genesis: WireEntry): Promise<void> {
    await this.store.tx(async (tx) => {
      await tx.put('created', this.cursorKey(list), toBytes(genesis));
      await tx.put('cursor', this.cursorKey(list), toBytes(0));
    });
  }

  /** Lists made here that the server hasn't accepted yet, with their genesis. */
  async created(): Promise<[ListId, WireEntry][]> {
    const out: [ListId, WireEntry][] = [];
    for await (const [key, v] of this.store.range('created', `${this.account}/`)) out.push([key.slice(this.account.length + 1), fromBytes<WireEntry>(v)]);
    return out;
  }

  async announced(list: ListId): Promise<void> {
    await this.store.delete('created', this.cursorKey(list));
  }

  async since(list: ListId): Promise<number> {
    const v = await this.store.get('cursor', this.cursorKey(list));
    return v === null ? 0 : fromBytes<number>(v);
  }

  async load(list: ListId): Promise<StoredContent> {
    const snapshots: StoredContent['snapshots'] = [];
    const updates: StoredContent['updates'] = [];
    for await (const [key, v] of this.store.range('content', this.prefix(list))) {
      const [kind, n] = key.slice(this.prefix(list).length).split('/');
      if (kind === 's') snapshots.push({ coversSeq: Number(n), msg: fromBytes<WireMsg>(v) });
      else updates.push({ seq: Number(n), msg: fromBytes<WireMsg>(v) });
    }
    return { since: await this.since(list), snapshots, updates };
  }

  /** Content the server delivered, and the cursor moved to `since` (never back), in one transaction. */
  async delivered(list: ListId, since: number, updates: { seq: number; msg: WireMsg }[], snapshot?: { coversSeq: number; msg: WireMsg }): Promise<void> {
    await this.store.tx(async (tx) => {
      if (snapshot !== undefined) await tx.put('content', `${this.prefix(list)}s/${pad(snapshot.coversSeq)}`, toBytes(snapshot.msg));
      for (const u of updates) await tx.put('content', `${this.prefix(list)}u/${pad(u.seq)}`, toBytes(u.msg));
      const old = await tx.get('cursor', this.cursorKey(list));
      if (old === null || fromBytes<number>(old) < since) await tx.put('cursor', this.cursorKey(list), toBytes(since));
    });
  }

  /**
   * Our own snapshot was stored by the server: it holds everything we had,
   * so it replaces every stored snapshot and every update it covers.
   * Snapshots from others never replace anything (E2EE §7: a snapshot is a cache).
   */
  async compact(list: ListId, coversSeq: number, msg: WireMsg): Promise<void> {
    await this.store.tx(async (tx) => {
      for await (const [key] of tx.range('content', this.prefix(list))) {
        const [kind, n] = key.slice(this.prefix(list).length).split('/');
        if (kind === 's' || Number(n) <= coversSeq) await tx.delete('content', key);
      }
      await tx.put('content', `${this.prefix(list)}s/${pad(coversSeq)}`, toBytes(msg));
    });
  }

  async outbox(list?: ListId): Promise<OutboxItem[]> {
    const out: OutboxItem[] = [];
    const prefix = list === undefined ? `${this.account}/` : this.prefix(list);
    for await (const [key, v] of this.store.range('outbox', prefix)) {
      const [l, cid] = key.slice(this.account.length + 1).split('/') as [ListId, string];
      out.push({ list: l, cid, msg: fromBytes<WireMsg>(v) });
    }
    return out;
  }

  async queue(item: OutboxItem): Promise<void> {
    await this.store.put('outbox', `${this.prefix(item.list)}${item.cid}`, toBytes(item.msg));
  }

  /** The server stored an outbox item at `seq`: it becomes ordinary content, and the cursor moves to `since` if given. */
  async acked(item: OutboxItem, seq: number, since?: number): Promise<void> {
    await this.store.tx(async (tx) => {
      await tx.put('content', `${this.prefix(item.list)}u/${pad(seq)}`, toBytes(item.msg));
      await tx.delete('outbox', `${this.prefix(item.list)}${item.cid}`);
      if (since !== undefined) await tx.put('cursor', this.cursorKey(item.list), toBytes(since));
    });
  }

  /** Replaces outbox items, e.g. re-sealed under a new epoch. */
  async requeue(old: OutboxItem[], fresh: OutboxItem[]): Promise<void> {
    await this.store.tx(async (tx) => {
      for (const o of old) await tx.delete('outbox', `${this.prefix(o.list)}${o.cid}`);
      for (const f of fresh) await tx.put('outbox', `${this.prefix(f.list)}${f.cid}`, toBytes(f.msg));
    });
  }

  async dropOutbox(item: OutboxItem): Promise<void> {
    await this.store.delete('outbox', `${this.prefix(item.list)}${item.cid}`);
  }

  /** Everything about one list: content, cursor, outbox. */
  async forget(list: ListId): Promise<void> {
    await this.store.tx(async (tx) => {
      for (const table of ['content', 'outbox']) {
        for await (const [key] of tx.range(table, this.prefix(list))) await tx.delete(table, key);
      }
      await tx.delete('cursor', this.cursorKey(list));
      await tx.delete('created', this.cursorKey(list));
    });
  }

  /** Everything this account has here (sign-out, client-spec §9). */
  async clear(): Promise<void> {
    await this.store.tx(async (tx) => {
      for (const table of ['content', 'outbox', 'cursor', 'created']) {
        for await (const [key] of tx.range(table, `${this.account}/`)) await tx.delete(table, key);
      }
    });
  }

  private prefix(list: ListId) {
    return `${this.account}/${list}/`;
  }

  private cursorKey(list: ListId) {
    return `${this.account}/${list}`;
  }
}
