/**
 * The app's entry: mounts the UI with the Tauri platform.
 * The only place that knows both.
 */

import { ClientCore, type ClientCoreOptions } from '@runer/client-core';
import { createTauriPlatform, storedServers } from '@runer/platform-tauri';
import { App, startAppearance, t, type Shell } from '@runer/ui';
import '@runer/ui/theme.css';
import { mount } from 'svelte';
import { setUpChrome } from './chrome.js';
import tauriConf from '../src-tauri/tauri.conf.json';

/** Must equal the server's PUBLIC_URL. Set VITE_RUNER_SERVER at build time to change it. */
const DEFAULT_SERVER: string = import.meta.env['VITE_RUNER_SERVER'] ?? 'http://localhost:8080';

const opts: ClientCoreOptions = {
  reminderText: { generic: t('reminder.generic'), inbox: t('nav.inbox') },
  ...(import.meta.env.DEV ? { sync: { log: (m: string) => console.warn(m) } } : {}),
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
};

setUpChrome();
startAppearance();
mount(App, { target: document.getElementById('app')!, props: { shell } });
