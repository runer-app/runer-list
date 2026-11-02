/**
 * The IPC to the Rust side (`apps/tauri/src-tauri`), behind a small
 * interface so the adapters can be tested without Tauri. Bytes travel as
 * base64url strings; a failed command rejects with `{ code, message }`.
 */

import { Channel, invoke } from '@tauri-apps/api/core';
import { listen } from '@tauri-apps/api/event';

export interface Ipc {
  invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T>;
  /** A channel argument for a command: `onMessage` gets each message the Rust side sends on it. */
  channel<T>(onMessage: (message: T) => void): unknown;
  /** Subscribes to an app event; resolves to the unsubscribe function. */
  listen<T>(event: string, cb: (payload: T) => void): Promise<() => void>;
}

/** A command's failure, as the Rust side serializes it (`wire::Error`). */
export interface IpcError {
  code: string;
  message: string;
}

export function isIpcError(e: unknown): e is IpcError {
  return typeof e === 'object' && e !== null && typeof (e as IpcError).code === 'string' && typeof (e as IpcError).message === 'string';
}

export const tauriIpc: Ipc = {
  // An `Error`, so whoever shows it gets the message, not `[object Object]`; `code` stays for `isIpcError`.
  invoke: <T>(cmd: string, args?: Record<string, unknown>) =>
    invoke<T>(cmd, args).catch((e: unknown) => {
      throw isIpcError(e) ? Object.assign(new Error(e.message), { code: e.code }) : e;
    }),
  channel<T>(onMessage: (message: T) => void) {
    const ch = new Channel<T>();
    ch.onmessage = onMessage;
    return ch;
  },
  listen: (event, cb) => listen(event, (e) => cb(e.payload as never)),
};
