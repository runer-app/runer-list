/**
 * The app's entry: mounts the UI with the Tauri platform.
 * The only place that knows both.
 */

import { ClientCore, type ClientCoreOptions } from '@runer/client-core';
import { createTauriPlatform, storedServers, tauriIpc } from '@runer/platform-tauri';
import { App, startAppearance, t, type Shell } from '@runer/ui';
import '@runer/ui/theme.css';
import { mount } from 'svelte';
import { setUpChrome } from './chrome.js';
import tauriConf from '../src-tauri/tauri.conf.json';

/** Must equal the server's PUBLIC_URL. Set VITE_RUNER_SERVER at build time to change it. */
const DEFAULT_SERVER: string = import.meta.env['VITE_RUNER_SERVER'] ?? 'http://localhost:8080';

// `tauri dev`, and debug builds (`make ios-phone`): the sync log goes to the console and the debug log (Settings).
const debug = import.meta.env.DEV || import.meta.env['TAURI_ENV_DEBUG'] === 'true';

function log(m: string): void {
  console.warn(m);
  void tauriIpc.invoke('debug_log_append', { line: `web: ${m}` }).catch(() => undefined);
}

const opts: ClientCoreOptions = {
  reminderText: { generic: t('reminder.generic'), inbox: t('nav.inbox') },
  ...(debug ? { sync: { log } } : {}),
};

// Links don't depend on the server: any platform instance reads them.
const links = createTauriPlatform(DEFAULT_SERVER).links;

const shell: Shell = {
  defaultServer: DEFAULT_SERVER,
  version: tauriConf.version,
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
  ...(debug
    ? {
        debugLog: {
          read: () => tauriIpc.invoke<string>('debug_log_read'),
          clear: () => tauriIpc.invoke<void>('debug_log_clear'),
        },
      }
    : {}),
};

if (debug) log(`page loaded (${document.visibilityState})`);

setUpChrome();
startAppearance();
mount(App, { target: document.getElementById('app')!, props: { shell } });
