import type { HttpResponse, Socket, Transport } from '@runer/client-core';

/** The `Origin` the app sends (`APP_ORIGIN` in `src-tauri/src/net.rs`); the server under test must have it as `ORIGIN`. */
const ORIGIN = 'tauri://localhost';

/**
 * HTTP and WebSocket to a real server, as the Tauri shell does it (`src-tauri/src/net.rs`): `Origin` on every
 * request and upgrade, the session cookie kept per transport, a JSON body only when the response says JSON.
 */
export function httpTransport(server: string): Transport {
  const jar = new Map<string, string>();
  const cookie = () => [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
  const headers = (): Record<string, string> => ({ Origin: ORIGIN, ...(jar.size > 0 ? { Cookie: cookie() } : {}) });

  return {
    async request(method, path, body): Promise<HttpResponse> {
      const res = await fetch(server + path, {
        method,
        headers: { ...headers(), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      for (const c of res.headers.getSetCookie()) {
        const pair = c.split(';')[0]!;
        const name = pair.slice(0, pair.indexOf('='));
        const value = pair.slice(pair.indexOf('=') + 1);
        if (value === '' || /max-age=0|expires=thu, 01 jan 1970/i.test(c)) jar.delete(name);
        else jar.set(name, value);
      }
      const text = await res.text();
      const json = res.headers.get('content-type')?.includes('application/json') && text !== '';
      return { status: res.status, headers: Object.fromEntries(res.headers), body: json ? JSON.parse(text) : undefined };
    },

    openSocket(path): Promise<Socket> {
      const ws = new WebSocket(server.replace(/^http/, 'ws') + path, { headers: headers() } as never);
      return new Promise((resolve, reject) => {
        ws.onerror = () => reject(new Error('socket upgrade failed'));
        ws.onopen = () => {
          ws.onerror = null;
          resolve({
            send: (frame) => ws.send(JSON.stringify(frame)),
            onMessage(cb) {
              const on = (e: MessageEvent) => cb(JSON.parse(e.data as string));
              ws.addEventListener('message', on);
              return () => ws.removeEventListener('message', on);
            },
            onClose(cb) {
              const on = (e: { code: number; reason: string }) => cb({ code: e.code, reason: e.reason });
              ws.addEventListener('close', on);
              return () => ws.removeEventListener('close', on);
            },
            close: () => ws.close(),
          });
        };
      });
    },
  };
}
