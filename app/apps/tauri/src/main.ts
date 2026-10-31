/**
 * The app's entry: mounts the UI with the Tauri platform.
 * The only place that knows both.
 */

import { ClientCore, logReason, type ClientCoreOptions } from '@runer/client-core';
import { createTauriPlatform, storedServers, tauriIpc } from '@runer/platform-tauri';
import { App, startAppearance, t, type Shell } from '@runer/ui';
import '@runer/ui/theme.css';
import { mount } from 'svelte';
import { setUpChrome } from './chrome.js';
import { getVersion } from '@tauri-apps/api/app';

/** Must equal the server's PUBLIC_URL. Set VITE_RUNER_SERVER at build time to change it. */
const DEFAULT_SERVER: string = import.meta.env['VITE_RUNER_SERVER'] ?? 'http://localhost:8080';

// `tauri dev`, and debug builds (`make ios-phone`): the log also goes to the console.
const debug = import.meta.env.DEV || import.meta.env['TAURI_ENV_DEBUG'] === 'true';

/** To the device's log (Settings → Diagnostics). */
function log(m: string): void {
  if (debug) console.warn(m);
  void tauriIpc.invoke('debug_log_append', { line: `web: ${m}` }).catch(() => undefined);
}

const clearLog = () => tauriIpc.invoke<void>('debug_log_clear');

// Before anything that can fail, the `await`s below included. Where and what kind, not the message (`logReason`).
window.addEventListener('error', (e) => log(`js: uncaught ${logReason(e.error)} at ${e.filename.split('/').pop()}:${e.lineno}:${e.colno}`));
window.addEventListener('unhandledrejection', (e) => log(`js: unhandled rejection ${logReason(e.reason)}`));

const opts: ClientCoreOptions = {
  reminderText: { generic: t('reminder.generic'), inbox: t('nav.inbox') },
  sync: { log },
  clearLog,
};

// Links don't depend on the server: any platform instance reads them.
const links = createTauriPlatform(DEFAULT_SERVER).links;

// The version in `src-tauri/tauri.conf.json`, asked of the running app.
const version = await getVersion();

const shell: Shell = {
  defaultServer: DEFAULT_SERVER,
  version,
  // One account per device; several servers at once need a platform per account.
  async resume() {
    const [server] = await storedServers();
    if (server === undefined) return null;
    const [core] = await ClientCore.resume(createTauriPlatform(server), opts);
    return core ?? null;
  },
  signUp: (server, name) => ClientCore.signUp(createTauriPlatform(server), server, name, opts),
  signIn: (server, words) => ClientCore.signIn(createTauriPlatform(server), server, words, opts),
  links: { initial: () => links.initial(), onLink: (cb) => links.onLink(cb) },
  // The event `src-tauri/src/update.rs` emits once an update is downloaded (and, off Windows, installed).
  // ponytail: an update ready before the page listens is missed until the next start; the check and download take far longer than the page.
  update: {
    onReady: (cb) => void tauriIpc.listen('runer://update-ready', cb),
    restart: () => tauriIpc.invoke<void>('update_restart'),
  },
  debugLog: { read: () => tauriIpc.invoke<string>('debug_log_read'), clear: clearLog },
};

log(`page loaded (${document.visibilityState})`);

setUpChrome();
startAppearance();
mount(App, { target: document.getElementById('app')!, props: { shell } });
