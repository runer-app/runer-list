/**
 * The adversarial proxy: a `Transport` that sits between
 * one device and the server and plays a hostile server. Each test sets
 * `tamper` to rewrite responses and frames, drop them, or `inject` its own.
 * It also keeps a copy of everything that crossed it, for the network
 * canary. It is in-process, at the one interface the client trusts the
 * network through, so it sees exactly what a man in the middle of the
 * server would.
 */

import type { HttpResponse, Socket, Transport } from '@runer/client-core';

type Frame = Record<string, unknown>;

export interface Tamper {
  /** Rewrites an HTTP response before the client sees it. */
  response?(method: string, path: string, res: HttpResponse): HttpResponse;
  /** Server → client; `null` drops the frame. */
  toClient?(frame: Frame): Frame | null;
  /** Client → server; `null` drops the frame. */
  toServer?(frame: Frame): Frame | null;
}

export class ProxyTransport implements Transport {
  tamper: Tamper = {};
  /** Every request, response and frame that crossed, as JSON. */
  readonly seen: string[] = [];
  private readonly sockets = new Set<{ inject(f: Frame): void; close(): void }>();

  constructor(private readonly inner: Transport) {}

  async request(method: string, path: string, body?: unknown): Promise<HttpResponse> {
    this.seen.push(JSON.stringify({ method, path, body }));
    let res = await this.inner.request(method, path, body);
    if (this.tamper.response !== undefined) res = this.tamper.response(method, path, structuredClone(res));
    this.seen.push(JSON.stringify(res.body ?? null));
    return res;
  }

  async openSocket(path: string): Promise<Socket> {
    const inner = await this.inner.openSocket(path);
    const listeners = new Set<(f: unknown) => void>();
    const deliver = (f: Frame) => {
      this.seen.push(JSON.stringify(f));
      for (const cb of [...listeners]) cb(f);
    };
    inner.onMessage((raw) => {
      const f = this.tamper.toClient === undefined ? (raw as Frame) : this.tamper.toClient(structuredClone(raw as Frame));
      if (f !== null) deliver(f);
    });
    const handle = { inject: deliver, close: () => inner.close() };
    this.sockets.add(handle);
    inner.onClose(() => this.sockets.delete(handle));
    return {
      send: (frame) => {
        const f = this.tamper.toServer === undefined ? (frame as Frame) : this.tamper.toServer(structuredClone(frame as Frame));
        if (f === null) return;
        this.seen.push(JSON.stringify(f));
        inner.send(f);
      },
      onMessage(cb) {
        listeners.add(cb);
        return () => void listeners.delete(cb);
      },
      onClose: (cb) => inner.onClose(cb),
      close: () => inner.close(),
    };
  }

  /** Sends a frame the server never sent. */
  inject(frame: Frame): void {
    for (const s of this.sockets) s.inject(frame);
  }

  /** Closes the connection; the client reconnects and re-checks every list's head. */
  drop(): void {
    for (const s of [...this.sockets]) s.close();
  }
}
