/**
 * The only thing each shell implements. client-core depends
 * on these interfaces and nothing else: no DOM, no Tauri, no Node.
 *
 * Keys and crypto are in the vault (`vault.ts`), which is Rust on every
 * platform. There is no secrets store here: only the vault touches the
 * keychain.
 */

import type { Vault } from './vault.js';

export * from './vault.js';

/** Key-value plus ordered ranges. Everything stored here is already ciphertext. */
export interface LocalStore {
  get(table: string, key: string): Promise<Uint8Array | null>;
  put(table: string, key: string, value: Uint8Array): Promise<void>;
  delete(table: string, key: string): Promise<void>;
  /** Entries of `table` whose key starts with `prefix`, in key order. */
  range(table: string, prefix: string, opts?: { after?: string; limit?: number }): AsyncIterable<[string, Uint8Array]>;
  /** All-or-nothing: if `fn` throws, none of its writes stay. */
  tx<T>(fn: (tx: LocalStore) => Promise<T>): Promise<T>;
}

export interface HttpResponse {
  status: number;
  headers: Record<string, string>;
  /** Parsed JSON body, or `undefined` when there is none. */
  body: unknown;
}

/** A WebSocket carrying JSON text frames (Sync §1). */
export interface Socket {
  send(frame: unknown): void;
  onMessage(cb: (frame: unknown) => void): () => void;
  onClose(cb: (info: { code: number; reason: string }) => void): () => void;
  close(): void;
}

/**
 * HTTP and WebSocket to the server. The implementation holds the session
 * cookie and sends the configured `Origin` on every mutation and upgrade.
 */
export interface Transport {
  request(method: string, path: string, body?: unknown): Promise<HttpResponse>;
  openSocket(path: string): Promise<Socket>;
}

/** Incoming invite links (`runer://join…`). */
export interface LinkSource {
  /** The link the app was opened with, if any. */
  initial(): Promise<string | null>;
  onLink(cb: (url: string) => void): () => void;
}

export interface Lifecycle {
  isOnline(): boolean;
  onForeground(cb: () => void): () => void;
  onBackground(cb: () => void): () => void;
  onOnline(cb: () => void): () => void;
  onOffline(cb: () => void): () => void;
}

/** A local notification the device should have scheduled. */
export interface Reminder {
  /** Stable, so the platform can tell what changed. */
  id: string;
  at: Date;
  title: string;
  body?: string;
}

export interface Notifier {
  /** Most reminders the OS keeps pending at once (64 on iOS). */
  readonly maxPending: number;
  /** Makes the scheduled set exactly `reminders`. */
  replaceAll(reminders: Reminder[]): Promise<void>;
  permission(): Promise<'granted' | 'denied' | 'prompt'>;
  /** Asks the OS (Settings → notifications); the state afterwards. */
  requestPermission(): Promise<'granted' | 'denied' | 'prompt'>;
}

export interface PlatformInfo {
  kind: 'desktop' | 'ios' | 'android' | 'node';
}

export interface Platform {
  vault: Vault;
  storage: LocalStore;
  transport: Transport;
  links: LinkSource;
  lifecycle: Lifecycle;
  notifications: Notifier;
  info: PlatformInfo;
}
