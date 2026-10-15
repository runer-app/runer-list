import { PRODUCT_NAME } from './product.js';

export type DeviceKind = 'laptop' | 'phone' | 'browser';

/** Our app's own user agent: `runer-app/<version> (<os>)`, the OS as Rust names it (net.rs). */
const APP_OS: Record<string, [string, DeviceKind]> = {
  macos: ['Mac', 'laptop'],
  ios: ['iPhone', 'phone'],
  windows: ['Windows', 'laptop'],
  linux: ['Linux', 'laptop'],
  android: ['Android', 'phone'],
};

/** A session's device, from its user agent: "Mac · runer app", "iPhone · Safari". `null` when there's nothing to go on. */
export function describeDevice(ua: string | null): { name: string; kind: DeviceKind } | null {
  if (ua === null || ua.trim() === '') return null;
  const app = /^runer-app\/\S+(?: \((\w+)\))?/.exec(ua);
  if (app !== null) {
    const [os, kind] = APP_OS[app[1] ?? ''] ?? [null, 'laptop'];
    return { name: os === null ? `${PRODUCT_NAME} app` : `${os} · ${PRODUCT_NAME} app`, kind };
  }
  const os = /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android' : /Mac OS X|Macintosh/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : null;
  const browser = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : null;
  if (os === null && browser === null) return { name: ua.slice(0, 40), kind: 'browser' };
  return { name: [os, browser].filter((x) => x !== null).join(' · '), kind: os === 'iPhone' || os === 'Android' ? 'phone' : 'browser' };
}
