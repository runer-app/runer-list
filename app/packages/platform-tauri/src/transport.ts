/**
 * `Transport` over the `net_*` commands (`src-tauri/src/net.rs`). The Rust
 * side holds the session cookie and sends `Origin`; this side only names the
 * server, the path and the JSON.
 */

import type { HttpResponse, Socket, Transport } from '@runer/client-core';
import type { Ipc } from './ipc.js';

type SocketEvent = { t: 'message'; text: string } | { t: 'close'; code: number; reason: string };

export class TauriTransport implements Transport {
  constructor(
    private readonly ipc: Ipc,
    /** The API base URL, exactly as the user typed it or an invite named it. */
    readonly server: string,
  ) {}

  async request(method: string, path: string, body?: unknown): Promise<HttpResponse> {
    const r = await this.ipc.invoke<{ status: number; headers: Record<string, string>; body?: unknown }>('net_request', {
      server: this.server,
      method,
      path,
      body: body ?? null,
    });
    return { status: r.status, headers: r.headers, body: r.body };
  }

  async openSocket(path: string): Promise<Socket> {
    const socket = new TauriSocket(this.ipc);
    const id = await this.ipc.invoke<number>('net_socket_open', { server: this.server, path, events: this.ipc.channel<SocketEvent>((e) => socket.deliver(e)) });
    socket.opened(id);
    return socket;
  }
}

/**
 * Frames can arrive before the caller has subscribed (the Rust side reads as
 * soon as the upgrade is done), so they wait here for the first subscriber.
 */
class TauriSocket implements Socket {
  private id: number | null = null;
  private readonly messages = new Set<(frame: unknown) => void>();
  private readonly closes = new Set<(info: { code: number; reason: string }) => void>();
  private held: unknown[] = [];
  private closed: { code: number; reason: string } | null = null;

  constructor(private readonly ipc: Ipc) {}

  opened(id: number): void {
    this.id = id;
  }

  deliver(e: SocketEvent): void {
    if (e.t === 'close') {
      this.closed = { code: e.code, reason: e.reason };
      if (this.held.length === 0) for (const cb of [...this.closes]) cb(this.closed);
      return;
    }
    let frame: unknown;
    try {
      frame = JSON.parse(e.text);
    } catch {
      return; // Sync §1: JSON text frames only
    }
    if (this.messages.size === 0) this.held.push(frame);
    else for (const cb of [...this.messages]) cb(frame);
  }

  send(frame: unknown): void {
    if (this.id === null || this.closed !== null) return;
    void this.ipc.invoke('net_socket_send', { id: this.id, text: JSON.stringify(frame) }).catch(() => undefined);
  }

  onMessage(cb: (frame: unknown) => void): () => void {
    this.messages.add(cb);
    if (this.held.length > 0) {
      const held = this.held;
      this.held = [];
      for (const f of held) cb(f);
      if (this.closed !== null) for (const c of [...this.closes]) c(this.closed);
    }
    return () => void this.messages.delete(cb);
  }

  onClose(cb: (info: { code: number; reason: string }) => void): () => void {
    this.closes.add(cb);
    if (this.closed !== null && this.held.length === 0) queueMicrotask(() => cb(this.closed!));
    return () => void this.closes.delete(cb);
  }

  close(): void {
    if (this.id === null) return;
    void this.ipc.invoke('net_socket_close', { id: this.id }).catch(() => undefined);
  }
}
