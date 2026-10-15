/**
 * The Tauri `Platform` (client-spec §4.1), desktop and mobile. One per
 * server: the transport names the server on every call.
 */

import type { Lifecycle, LinkSource, Notifier, Platform, PlatformInfo, Reminder } from '@runer/client-core';
import { tauriIpc, type Ipc } from './ipc.js';
import { TauriLocalStore } from './store.js';
import { TauriTransport } from './transport.js';
import { TauriVault } from './vault.js';

/** The event `src-tauri/src/lib.rs` emits for an incoming `runer://` link. */
const LINK_EVENT = 'runer://link';

/**
 * Which kind of device the webview runs on, from its user agent. An iPad
 * may say it is a Mac; a Mac has no touch screen.
 */
export function platformKind(
  userAgent: string = navigator.userAgent,
  touchPoints: number = navigator.maxTouchPoints,
): Exclude<PlatformInfo['kind'], 'node'> {
  if (/Android/.test(userAgent)) return 'android';
  if (/iPhone|iPad|iPod/.test(userAgent) || (/Macintosh/.test(userAgent) && touchPoints > 1)) return 'ios';
  return 'desktop';
}

/** Desktop: window focus and the browser's online events. */
class WindowLifecycle implements Lifecycle {
  isOnline = () => navigator.onLine;
  onForeground = (cb: () => void) => on(window, 'focus', cb);
  onBackground = (cb: () => void) => on(window, 'blur', cb);
  onOnline = (cb: () => void) => on(window, 'online', cb);
  onOffline = (cb: () => void) => on(window, 'offline', cb);
}

type Page = Pick<Document, 'visibilityState' | 'addEventListener' | 'removeEventListener'>;

/** Phones: the page is hidden when the app leaves the foreground; the window there never loses focus. */
export class PageLifecycle extends WindowLifecycle {
  constructor(private readonly page: Page = document) {
    super();
    this.onForeground = (cb) => on(this.page, 'visibilitychange', () => this.page.visibilityState === 'visible' && cb());
    this.onBackground = (cb) => on(this.page, 'visibilitychange', () => this.page.visibilityState === 'hidden' && cb());
  }
}

function on(target: Pick<EventTarget, 'addEventListener' | 'removeEventListener'>, event: string, cb: () => void): () => void {
  target.addEventListener(event, cb);
  return () => target.removeEventListener(event, cb);
}

class TauriLinks implements LinkSource {
  constructor(private readonly ipc: Ipc) {}

  initial(): Promise<string | null> {
    return this.ipc.invoke<string | null>('links_initial');
  }

  onLink(cb: (url: string) => void): () => void {
    let off: (() => void) | null = null;
    let stopped = false;
    void this.ipc.listen<string>(LINK_EVENT, cb).then((unlisten) => {
      if (stopped) unlisten();
      else off = unlisten;
    });
    return () => {
      stopped = true;
      off?.();
    };
  }
}

class TauriNotifier implements Notifier {
  readonly maxPending = 64;

  constructor(private readonly ipc: Ipc) {}

  replaceAll(reminders: Reminder[]): Promise<void> {
    // `offset` is this device's UTC offset at that time, in minutes: the webview knows the time zone, Rust doesn't (notify.rs).
    return this.ipc.invoke('notify_replace_all', {
      reminders: reminders.map((r) => ({ at: r.at.getTime(), offset: -r.at.getTimezoneOffset(), title: r.title, body: r.body ?? null })),
    });
  }

  permission(): Promise<'granted' | 'denied' | 'prompt'> {
    return this.ipc.invoke('notify_permission');
  }

  requestPermission(): Promise<'granted' | 'denied' | 'prompt'> {
    return this.ipc.invoke('notify_request_permission');
  }
}

/** The platform for one server (the API base URL the user typed, or an invite named). */
export function createTauriPlatform(server: string, ipc: Ipc = tauriIpc): Platform {
  const kind = platformKind();
  return {
    vault: new TauriVault(ipc),
    storage: new TauriLocalStore(ipc),
    transport: new TauriTransport(ipc, server),
    links: new TauriLinks(ipc),
    lifecycle: kind === 'desktop' ? new WindowLifecycle() : new PageLifecycle(),
    notifications: new TauriNotifier(ipc),
    info: { kind },
  };
}

/**
 * The servers of the accounts kept on this device, before any platform
 * exists: startup builds the platform for the account's server, then
 * resumes it. Rejects with a `VaultError` (`Storage`) if the vault couldn't
 * open, e.g. no keychain.
 */
export async function storedServers(ipc: Ipc = tauriIpc): Promise<string[]> {
  return [...new Set((await new TauriVault(ipc).storedAccounts()).map((a) => a.server))];
}
