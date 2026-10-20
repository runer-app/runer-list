import type { Lifecycle, LinkSource, LocalStore, Notifier, Platform, Reminder, Transport } from '../../src/platform/index.js';
import { NodeVault } from './vault.js';

/** In-memory `Platform` for tests. Not published. */

type Tables = Map<string, Map<string, Uint8Array>>;

export class MemoryLocalStore implements LocalStore {
  private tables: Tables = new Map();

  private table(name: string): Map<string, Uint8Array> {
    let t = this.tables.get(name);
    if (t === undefined) this.tables.set(name, (t = new Map()));
    return t;
  }

  async get(table: string, key: string) {
    const v = this.tables.get(table)?.get(key);
    return v === undefined ? null : v.slice();
  }
  async put(table: string, key: string, value: Uint8Array) {
    this.table(table).set(key, value.slice());
  }
  async delete(table: string, key: string) {
    this.tables.get(table)?.delete(key);
  }
  async *range(table: string, prefix: string, opts: { after?: string; limit?: number } = {}) {
    const keys = [...(this.tables.get(table)?.keys() ?? [])]
      .filter((k) => k.startsWith(prefix) && (opts.after === undefined || k > opts.after))
      .sort();
    for (const k of keys.slice(0, opts.limit)) yield [k, this.tables.get(table)!.get(k)!.slice()] as [string, Uint8Array];
  }
  /** Every value in every table, for tests that look for plaintext. */
  async dump(): Promise<Uint8Array[]> {
    return [...this.tables.values()].flatMap((t) => [...t.values()]);
  }
  /** Rolls back on throw. Transactions must not overlap (tests are sequential). */
  async tx<T>(fn: (tx: LocalStore) => Promise<T>): Promise<T> {
    const before: Tables = new Map([...this.tables].map(([n, t]) => [n, new Map(t)]));
    try {
      return await fn(this);
    } catch (e) {
      this.tables = before;
      throw e;
    }
  }
}

export class RecordingNotifier implements Notifier {
  scheduled: Reminder[] = [];
  calls = 0;
  constructor(
    readonly maxPending = 64,
    private perm: 'granted' | 'denied' | 'prompt' = 'granted',
  ) {}
  async replaceAll(reminders: Reminder[]) {
    this.calls++;
    this.scheduled = reminders.slice(0, this.maxPending);
  }
  async permission() {
    return this.perm;
  }
  async requestPermission() {
    if (this.perm === 'prompt') this.perm = 'granted';
    return this.perm;
  }
}

class Emitter {
  private subs = new Set<() => void>();
  on(cb: () => void) {
    this.subs.add(cb);
    return () => void this.subs.delete(cb);
  }
  emit() {
    for (const cb of [...this.subs]) cb();
  }
}

export class ManualLifecycle implements Lifecycle {
  online = true;
  private fg = new Emitter();
  private bg = new Emitter();
  private up = new Emitter();
  private down = new Emitter();
  isOnline = () => this.online;
  onForeground = (cb: () => void) => this.fg.on(cb);
  onBackground = (cb: () => void) => this.bg.on(cb);
  onOnline = (cb: () => void) => this.up.on(cb);
  onOffline = (cb: () => void) => this.down.on(cb);
  foreground = () => this.fg.emit();
  background = () => this.bg.emit();
  goOnline() {
    this.online = true;
    this.up.emit();
  }
  goOffline() {
    this.online = false;
    this.down.emit();
  }
}

export class ManualLinkSource implements LinkSource {
  private subs = new Set<(url: string) => void>();
  constructor(private initialLink: string | null = null) {}
  async initial() {
    return this.initialLink;
  }
  onLink(cb: (url: string) => void) {
    this.subs.add(cb);
    return () => void this.subs.delete(cb);
  }
  open(url: string) {
    for (const cb of [...this.subs]) cb(url);
  }
}

/** The Node transport (undici + ws, own cookie jar) arrives with the sync module. */
const noTransport: Transport = {
  async request() {
    throw new Error('MemoryPlatform has no transport yet');
  },
  async openSocket() {
    throw new Error('MemoryPlatform has no transport yet');
  },
};

export function createMemoryPlatform(over: Partial<Platform> = {}): Platform {
  return {
    vault: new NodeVault(),
    storage: new MemoryLocalStore(),
    transport: noTransport,
    links: new ManualLinkSource(),
    lifecycle: new ManualLifecycle(),
    notifications: new RecordingNotifier(),
    info: { kind: 'node' },
    ...over,
  };
}
