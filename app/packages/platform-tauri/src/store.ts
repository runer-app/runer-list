/**
 * `LocalStore` over the `store_*` commands (`src-tauri/src/store.rs`).
 *
 * A transaction can't stay open across IPC calls without catching every
 * other caller's writes in it, so `tx` collects its writes here (reads see
 * them) and sends them in one `store_write`, which SQLite applies all or
 * nothing. If `fn` throws, nothing is sent.
 */

import { fromBase64url, toBase64url, type LocalStore } from '@runer/client-core';
import type { Ipc } from './ipc.js';

type Op = { op: 'put'; table: string; key: string; value: string } | { op: 'delete'; table: string; key: string };

export class TauriLocalStore implements LocalStore {
  constructor(private readonly ipc: Ipc) {}

  async get(table: string, key: string): Promise<Uint8Array | null> {
    const v = await this.ipc.invoke<string | null>('store_get', { table, key });
    return v === null ? null : fromBase64url(v);
  }

  put(table: string, key: string, value: Uint8Array): Promise<void> {
    return this.write([{ op: 'put', table, key, value: toBase64url(value) }]);
  }

  delete(table: string, key: string): Promise<void> {
    return this.write([{ op: 'delete', table, key }]);
  }

  async *range(table: string, prefix: string, opts: { after?: string; limit?: number } = {}): AsyncIterable<[string, Uint8Array]> {
    const rows = await this.ipc.invoke<[string, string][]>('store_range', { table, prefix, after: opts.after ?? null, limit: opts.limit ?? null });
    for (const [k, v] of rows) yield [k, fromBase64url(v)];
  }

  async tx<T>(fn: (tx: LocalStore) => Promise<T>): Promise<T> {
    const tx = new Tx(this);
    const result = await fn(tx);
    await this.write(tx.ops());
    return result;
  }

  write(ops: Op[]): Promise<void> {
    return ops.length === 0 ? Promise.resolve() : this.ipc.invoke('store_write', { ops });
  }
}

/** Writes held back until the transaction ends; `null` marks a delete. */
class Tx implements LocalStore {
  private readonly writes = new Map<string, Map<string, Uint8Array | null>>();

  constructor(private readonly base: TauriLocalStore) {}

  private table(name: string): Map<string, Uint8Array | null> {
    let t = this.writes.get(name);
    if (t === undefined) this.writes.set(name, (t = new Map()));
    return t;
  }

  async get(table: string, key: string): Promise<Uint8Array | null> {
    const w = this.writes.get(table);
    if (w?.has(key)) return w.get(key)?.slice() ?? null;
    return this.base.get(table, key);
  }

  async put(table: string, key: string, value: Uint8Array): Promise<void> {
    this.table(table).set(key, value.slice());
  }

  async delete(table: string, key: string): Promise<void> {
    this.table(table).set(key, null);
  }

  async *range(table: string, prefix: string, opts: { after?: string; limit?: number } = {}): AsyncIterable<[string, Uint8Array]> {
    const merged = new Map<string, Uint8Array | null>();
    for await (const [k, v] of this.base.range(table, prefix, opts.after === undefined ? {} : { after: opts.after })) merged.set(k, v);
    for (const [k, v] of this.writes.get(table) ?? []) {
      if (k.startsWith(prefix) && (opts.after === undefined || k > opts.after)) merged.set(k, v);
    }
    const keys = [...merged.keys()].filter((k) => merged.get(k) !== null).sort();
    for (const k of keys.slice(0, opts.limit)) yield [k, merged.get(k)!.slice()];
  }

  tx<T>(fn: (tx: LocalStore) => Promise<T>): Promise<T> {
    return fn(this);
  }

  ops(): Op[] {
    const out: Op[] = [];
    for (const [table, t] of this.writes) {
      for (const [key, v] of t) out.push(v === null ? { op: 'delete', table, key } : { op: 'put', table, key, value: toBase64url(v) });
    }
    return out;
  }
}
