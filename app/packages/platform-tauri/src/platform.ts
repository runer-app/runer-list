/**
 * The Tauri `Platform`, desktop and mobile. One per
 * server: the transport names the server on every call.
 */

import type { Lifecycle, LinkSource, Notifier, Platform, PlatformInfo, Reminder } from '@runer/client-core';
import { tauriIpc, type Ipc } from './ipc.js';
import { TauriLocalStore } from './store.js';
import { TauriTransport } from './transport.js';
import { TauriVault } from './vault.js';

/** The event `src-tauri/src/lib.rs` emits for an incoming `runer://` link. */
const LINK_EVENT = 'runer://link';
/** The event it emits when iOS runs the background refresh task. */
const WAKE_EVENT = 'runer://wake';

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
  private readonly wakes = new Set<() => Promise<void>>();
  private listening = false;

  constructor(private readonly ipc: Ipc) {}

  isOnline = () => navigator.onLine;
  onForeground = (cb: () => void) => on(window, 'focus', cb);
  onBackground = (cb: () => void) => on(window, 'blur', cb);
  onOnline = (cb: () => void) => on(window, 'online', cb);
  onOffline = (cb: () => void) => on(window, 'offline', cb);

  /** iOS background refresh. A wake that came before the first subscriber is pending in Rust; the other platforms never wake. */
  onWake(cb: () => Promise<void>): () => void {
    this.wakes.add(cb);
    if (!this.listening) {
      this.listening = true;
      // A pending wake runs the subscribers there are by then: with several accounts, one that starts later waits for the next wake.
      void this.ipc
        .listen(WAKE_EVENT, () => this.claim('event received'))
        .then(() => this.claim('listening'))
        .catch((e: unknown) => this.log(`wake: listening failed: ${e instanceof Error ? e.message : String(e)}`));
    }
    return () => void this.wakes.delete(cb);
  }

  /** Runs the wake Rust holds, if any. The event only says to ask, so a wake that comes while listening starts runs once. */
  private claim(why: string): void {
    void this.ipc.invoke<boolean>('wake_pending').then(
      (pending) => {
        this.log(`wake: ${why}, ${pending ? 'one pending' : 'none pending'}`);
        return pending ? this.wake() : undefined;
      },
      (e: unknown) => this.log(`wake: asking failed: ${e instanceof Error ? e.message : String(e)}`),
    );
  }

  private async wake(): Promise<void> {
    this.log(`wake: running ${this.wakes.size} accounts`);
    await Promise.allSettled([...this.wakes].map((cb) => cb()));
    this.log('wake: telling iOS it is done');
    await this.ipc.invoke('wake_done').catch(() => undefined);
  }

  /** To the debug log, which only debug builds write. */
  private log(m: string): void {
    void this.ipc.invoke('debug_log_append', { line: `web: ${m}` }).catch(() => undefined);
  }
}

type Page = Pick<Document, 'visibilityState' | 'addEventListener' | 'removeEventListener'>;

/** Phones: the page is hidden when the app leaves the foreground; the window there never loses focus. */
export class PageLifecycle extends WindowLifecycle {
  constructor(ipc: Ipc, private readonly page: Page = document) {
    super(ipc);
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
    lifecycle: kind === 'desktop' ? new WindowLifecycle(ipc) : new PageLifecycle(ipc),
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
