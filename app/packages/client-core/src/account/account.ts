/**
 * One signed-in account on one server (E2EE §3, §9): sign-up, sign-in,
 * silent re-auth, change of account key. The
 * vault holds every key and signs; this only talks to the server.
 */

import { fromBase64url, toBase64url } from '../base64url.js';
import { cleanName } from '../list/doc.js';
import type { Platform } from '../platform/index.js';
import { Writable } from '../readable.js';
import { catchUp } from '../sync/logs.js';
import type { AccountHandle, ListId, LogEvent, PublicIdentity, SignedEntry, VaultWarning } from '../platform/vault.js';

/** A non-2xx answer: the status and the API's error `code` (API reference: Conventions). */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

interface LoginResult {
  bundle_gen: number;
  inbox_id: ListId;
}

interface Bundle {
  identity_bundle: string;
  bundle_gen: number;
}

/**
 * Settings that are the same on every device of the account (E2EE §3.3).
 * The server keeps them sealed (`settings_sealed`) and can't read them. What
 * belongs to one device (its notifications) stays in that device's store.
 */
export interface AccountSettings {
  /** The format's version. */
  v: number;
  /** Times shown on a 12- or 24-hour clock; unset follows the device's language. */
  clock?: '12h' | '24h';
  /**
   * What the user is called, shown to the people they share lists with.
   * Optional; unset or empty means no name. The server holds no personal
   * information about an account, so the name lives here, sealed.
   */
  name?: string;
  /** What a newer app keeps here: carried along and sent back as it came. */
  [field: string]: unknown;
}

export type AccountSettingsChange = Partial<Pick<AccountSettings, 'clock' | 'name'>>;

const SETTINGS_VERSION = 1;
/** `LocalStore` tables, by vault handle, both sealed as for the server: the settings as this device has them, and a change the server hasn't taken yet. */
const SETTINGS_TABLE = 'account-settings';
const UNSENT_SETTINGS_TABLE = 'account-settings-unsent';
/** How often a write that lost the race to another device is tried again. */
const SETTINGS_RETRIES = 3;
const SETTINGS_RETRY_MS = 30_000;

/** What `notices` holds: news from a log, or a list this account can no longer open, named as this device last saw it. */
export type Notice = LogEvent | { kind: 'lost-access'; list: ListId; name: string };

export class Account {
  /** Security warnings seen so far. */
  readonly warnings = new Writable<VaultWarning[]>([]);
  /** Membership changes seen in our lists ("Anna joined"), and lists we lost access to, oldest first. */
  readonly notices = new Writable<Notice[]>([]);
  /**
   * What the user is called: `settings.name`, cleaned (`cleanName`). Empty
   * for an account without a name. The others in a shared list read it
   * from the list, where `ClientCore` writes it.
   */
  readonly name = new Writable<string>('');
  /** The account's settings as this device knows them; `{v}` alone until one is set. */
  readonly settings = new Writable<AccountSettings>({ v: SETTINGS_VERSION });
  /** What `changeSettings` changed and the server hasn't taken yet. */
  private unsentSettings: AccountSettingsChange | null = null;
  private settingsRetry: ReturnType<typeof setTimeout> | undefined;
  /** One exchange with the server at a time, and one write to the local store at a time. */
  private settingsExchange: Promise<void> = Promise.resolve();
  private settingsSaved: Promise<void> = Promise.resolve();
  private signedOut = false;
  private bundleGen = 0;
  private relogin: Promise<void> | null = null;

  private constructor(
    readonly platform: Platform,
    /** The base URL the user typed or an invite named; the login message covers it. */
    readonly server: string,
    readonly handle: AccountHandle,
    private authPub: Uint8Array,
    private identityKeys: PublicIdentity,
    private inboxId: ListId | null,
  ) {}

  get identity(): PublicIdentity {
    return this.identityKeys;
  }

  /**
   * The Inbox: a private list made with the account (or with its current
   * identity), which the server won't delete. The server names it; the
   * list's verified log must show it is ours (`refreshLists`).
   */
  get inbox(): ListId {
    if (this.inboxId === null) throw new Error('the Inbox is not verified yet');
    return this.inboxId;
  }

  /**
   * Creates the account: keys and bundle in the vault, proof-of-work, and a
   * first login carrying the identity and the Inbox's genesis, which the
   * server creates together. Returns the 12 words to show once.
   *
   * The server is given keys only. A `name` goes into the sealed settings;
   * if the server can't be reached for that, the next `syncSettings` sends it.
   */
  static async signUp(platform: Platform, server: string, name = ''): Promise<{ account: Account; words: string[] }> {
    const { vault, transport } = platform;
    const s = await vault.startSignUp(server);
    let account: Account;
    let words: string[];
    try {
      const { nonce, pow_bits } = await data<{ nonce: string; pow_bits: number }>(
        transport.request('POST', '/v1/auth/challenge', { auth_pub: toBase64url(s.authPub) }),
      );
      const n = fromBase64url(nonce);
      const login = await data<LoginResult>(
        transport.request('POST', '/v1/auth/login', {
          auth_pub: toBase64url(s.authPub),
          nonce,
          sig: toBase64url(await vault.signServerMessage(s.account, 'login', n)),
          identity_sign_pub: toBase64url(s.identity.signPub),
          identity_agree_pub: toBase64url(s.identity.agreePub),
          identity_bundle: toBase64url(s.bundle),
          identity_sig: toBase64url(await vault.signServerMessage(s.account, 'identity', n)),
          pow_nonce: toBase64url(await vault.solveSignUpPow(s.account, n, pow_bits)),
          ...entryFields('inbox', s.inbox.entry),
        }),
      );
      ({ words } = await vault.confirmSignUp(s.account));
      await vault.applyLog(s.account, s.inbox.list, [s.inbox.entry]);
      account = new Account(platform, server, s.account, s.authPub, s.identity, s.inbox.list);
      account.bundleGen = login.bundle_gen;
    } catch (e) {
      await vault.signOut(s.account);
      throw e;
    }
    // The account exists from here on, and the words must reach the user whatever happens to the name: one that
    // doesn't reach the server now is kept and sent later, and one that can't even be kept is set again in Settings.
    const named = cleanName(name);
    if (named !== '') {
      await account
        .keepSettings({ ...account.settings.value, name: named }, { name: named })
        .then(() => account.syncSettings())
        .catch(() => undefined);
    }
    return { account, words };
  }

  /** Sign-in on a new device. `null` for a typo in the words, caught before any request. */
  static async signIn(platform: Platform, server: string, words: string): Promise<Account | null> {
    const r = await platform.vault.signIn(server, words);
    if (r === null) return null;
    try {
      const login = await loginWith(platform, r.account, r.authPub);
      const b = await data<Bundle>(platform.transport.request('GET', '/v1/account/bundle'));
      const { identity } = await platform.vault.loadIdentity(r.account, b.bundle_gen, fromBase64url(b.identity_bundle));
      const account = new Account(platform, server, r.account, r.authPub, identity, null);
      account.bundleGen = b.bundle_gen;
      await account.adoptInbox(login.inbox_id);
      // The settings carry the name, so a new device has both from the start.
      await account.syncSettings().catch(() => undefined);
      return account;
    } catch (e) {
      await platform.vault.signOut(r.account);
      throw e;
    }
  }

  /**
   * Startup: the accounts kept on this device, ready without the network.
   * One whose sign-in stopped before its Inbox was verified finishes that
   * first, which needs the network.
   */
  static async resume(platform: Platform): Promise<Account[]> {
    const accounts: Account[] = [];
    for (const s of await platform.vault.storedAccounts()) {
      const account = new Account(platform, s.server, s.account, s.authPub, s.identity, s.inbox);
      account.bundleGen = s.bundleGen;
      await account.loadSettings();
      if (s.inbox === null) await account.adoptInbox((await account.request<{ inbox_id: ListId }>('GET', '/v1/account')).inbox_id);
      accounts.push(account);
    }
    return accounts;
  }

  /** Verifies the list the server names as our Inbox (the vault checks we own it alone) and keeps it. */
  private async adoptInbox(list: ListId): Promise<void> {
    await catchUp(this, list);
    await this.platform.vault.setInbox(this.handle, list);
    this.inboxId = list;
  }

  /**
   * A request with this account's session. On a 401 for a lost or expired
   * session it signs in again with the key in the vault and retries once.
   * A refused login fails the request (`loginRefused`): the key changed or
   * the account is gone, and the user signs in again with the words.
   */
  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const send = () => this.platform.transport.request(method, path, body);
    let res = await send();
    if (res.status === 401) {
      await this.reauth();
      res = await send();
    }
    return data<T>(res);
  }

  /** Fetches and opens the Identity Bundle; a lower `bundle_gen` than seen becomes a warning. */
  async loadIdentity(): Promise<void> {
    const b = await this.request<Bundle>('GET', '/v1/account/bundle');
    const r = await this.platform.vault.loadIdentity(this.handle, b.bundle_gen, fromBase64url(b.identity_bundle));
    this.identityKeys = r.identity;
    this.warn(r.warnings);
    if (r.warnings.length === 0) this.bundleGen = b.bundle_gen;
  }

  /** Changes the name, or clears it with an empty one. A setting like the others: it works offline. */
  setName(name: string): Promise<void> {
    return this.changeSettings({ name: cleanName(name) });
  }

  /** Sets `settings`, and `name` from them. A name this app can't read (not a string) counts as none. */
  private show(settings: AccountSettings): void {
    this.settings.set(settings);
    this.name.set(typeof settings.name === 'string' ? cleanName(settings.name) : '');
  }

  /**
   * Changes settings on this device at once, so it works offline, and sends
   * the change in the background. One the server didn't take is sent by the
   * next `syncSettings`: a retry here, or the next connection or foreground.
   */
  async changeSettings(change: AccountSettingsChange): Promise<void> {
    await this.keepSettings({ ...this.settings.value, ...change }, { ...this.unsentSettings, ...change });
    this.sendSettings();
  }

  /** A connection can outlast a network blip, so this can't wait for the next one. */
  private sendSettings(): void {
    clearTimeout(this.settingsRetry);
    this.syncSettings().catch(() => {
      if (this.unsentSettings === null || this.signedOut) return;
      this.settingsRetry = setTimeout(() => this.sendSettings(), SETTINGS_RETRY_MS);
    });
  }

  /**
   * Brings this device and the server in line: takes the settings as the
   * server has them (another device may have changed them; there is no push,
   * so this is when it arrives), and sends a change made here on top of
   * them. Needs the server.
   */
  syncSettings(): Promise<void> {
    const run = this.settingsExchange.then(() => this.exchangeSettings());
    this.settingsExchange = run.catch(() => undefined);
    return run;
  }

  private async exchangeSettings(): Promise<void> {
    for (let lost = 0; ; lost++) {
      const r = await this.request<{ settings_sealed?: string | null; settings_gen?: number }>('GET', '/v1/account');
      // A blob that doesn't open is ignored, and replaced by the next change.
      const theirs = r.settings_sealed ? await this.openSettings(fromBase64url(r.settings_sealed)) : null;
      const unsent = this.unsentSettings;
      if (unsent === null) {
        if (theirs !== null) await this.keepSettings(theirs, null);
        return;
      }
      // The server can't merge what it can't read: our change goes on top of what it has, fields we don't know included.
      const next = { ...(theirs ?? { v: SETTINGS_VERSION }), ...unsent };
      const sealed = await this.platform.vault.sealAccountSettings(this.handle, JSON.stringify(next));
      try {
        await this.request('PUT', '/v1/account/settings', { settings_sealed: toBase64url(sealed), settings_gen: r.settings_gen ?? 0 });
      } catch (e) {
        // Another device wrote first (a stale `settings_gen`): fetch its settings and reapply ours.
        if (e instanceof ApiError && e.status === 409 && lost < SETTINGS_RETRIES) continue;
        throw e;
      }
      // A change made while this one was on its way stays unsent, for the exchange it queued.
      if (this.unsentSettings === unsent) await this.keepSettings(next, null);
      return;
    }
  }

  /** Startup: the settings and the unsent change as this device kept them. */
  private async loadSettings(): Promise<void> {
    const { storage } = this.platform;
    const settings = await storage.get(SETTINGS_TABLE, this.handle);
    const kept = settings === null ? null : await this.openSettings(settings);
    if (kept !== null) this.show(kept);
    const unsent = await storage.get(UNSENT_SETTINGS_TABLE, this.handle);
    if (unsent !== null) this.unsentSettings = await this.openSettings(unsent, {});
  }

  /** `null` for a blob that isn't ours, was changed, or isn't a JSON object. */
  private async openSettings(sealed: Uint8Array): Promise<AccountSettings | null>;
  private async openSettings(sealed: Uint8Array, base: AccountSettingsChange): Promise<AccountSettingsChange | null>;
  private async openSettings(sealed: Uint8Array, base: object = { v: SETTINGS_VERSION }): Promise<object | null> {
    try {
      const json: unknown = JSON.parse(await this.platform.vault.openAccountSettings(this.handle, sealed));
      return typeof json === 'object' && json !== null && !Array.isArray(json) ? { ...base, ...json } : null;
    } catch {
      return null;
    }
  }

  /** Takes effect at once; the local store follows, its writes in order so the last state is the one kept. */
  private keepSettings(settings: AccountSettings, unsent: AccountSettingsChange | null): Promise<void> {
    this.show(settings);
    this.unsentSettings = unsent;
    this.settingsSaved = this.settingsSaved.catch(() => undefined).then(() => this.saveSettings());
    return this.settingsSaved;
  }

  private async saveSettings(): Promise<void> {
    if (this.signedOut) return;
    const { vault, storage } = this.platform;
    const settings = await vault.sealAccountSettings(this.handle, JSON.stringify(this.settings.value));
    const unsent = this.unsentSettings === null ? null : await vault.sealAccountSettings(this.handle, JSON.stringify(this.unsentSettings));
    await storage.tx(async (tx) => {
      await tx.put(SETTINGS_TABLE, this.handle, settings);
      if (unsent === null) await tx.delete(UNSENT_SETTINGS_TABLE, this.handle);
      else await tx.put(UNSENT_SETTINGS_TABLE, this.handle, unsent);
    });
  }

  /** Adds to `warnings`. */
  warn(warnings: VaultWarning[]): void {
    if (warnings.length > 0) this.warnings.set([...this.warnings.value, ...warnings]);
  }

  /** Adds to `notices`: what others did in our lists' logs since this device last looked. */
  notice(events: LogEvent[]): void {
    const me = toBase64url(this.identity.signPub);
    const news = events.filter((e) => e.kind === 'rotated' || toBase64url(e.member) !== me);
    if (news.length > 0) this.notices.set([...this.notices.value, ...news]);
  }

  /** Adds to `notices`: a list someone else owns that the server no longer lists for us, deleted or we were removed. */
  lostAccess(list: ListId, name: string): void {
    this.notices.set([...this.notices.value, { kind: 'lost-access', list, name }]);
  }

  showWords(): Promise<string[]> {
    return this.platform.vault.showWords(this.handle);
  }

  /** E2EE §9.2. The server revokes every other session. Returns the new words to show once. */
  async changeAccountKey(): Promise<string[]> {
    const { vault } = this.platform;
    const r = await vault.changeAccountKey(this.handle);
    const nonce = await this.challenge();
    const out = await this.request<{ bundle_gen: number }>('POST', '/v1/account/rekey', {
      nonce: toBase64url(nonce),
      sig: toBase64url(await vault.signServerMessage(this.handle, 'rekey', nonce)),
      new_auth_pub: toBase64url(r.authPub),
      new_bundle: toBase64url(r.bundle),
      bundle_gen: this.bundleGen,
    });
    const { words } = await r.commit();
    this.authPub = r.authPub;
    this.bundleGen = out.bundle_gen;
    return words;
  }

  /** Deletes the account on the server with the lists it owns, and ends its every session (E2EE §8.4). Refused (`account_has_lists`) while it is a member of someone else's list. */
  async delete(): Promise<void> {
    await this.request('DELETE', '/v1/account');
  }

  /** Ends the session on the server (best effort) and forgets the account in the vault. */
  async signOut(): Promise<void> {
    await this.platform.transport.request('POST', '/v1/auth/logout').catch(() => undefined);
    this.signedOut = true;
    await this.settingsSaved.catch(() => undefined);
    await this.platform.storage.delete(SETTINGS_TABLE, this.handle);
    await this.platform.storage.delete(UNSENT_SETTINGS_TABLE, this.handle);
    await this.platform.vault.signOut(this.handle);
  }

  private async challenge(): Promise<Uint8Array> {
    const { nonce } = await this.request<{ nonce: string }>('POST', '/v1/account/rekey/challenge');
    return fromBase64url(nonce);
  }

  /**
   * One login at a time, however many requests hit a 401 together. A refusal
   * is tried once more: a stale challenge is refused the same way, and a
   * server restart loses them, but only a refused key is refused twice.
   */
  private reauth(): Promise<void> {
    const login = () => loginWith(this.platform, this.handle, this.authPub);
    this.relogin ??= login()
      .catch((e: unknown) => {
        if (loginRefused(e)) return login();
        throw e;
      })
      .then(() => undefined)
      .finally(() => (this.relogin = null));
    return this.relogin;
  }
}

/** A signed log entry as request fields: `<prefix>_entry` and `<prefix>_sig`. */
function entryFields(prefix: string, e: SignedEntry): Record<string, string> {
  return { [`${prefix}_entry`]: toBase64url(e.raw), [`${prefix}_sig`]: toBase64url(e.sig) };
}

/** The server refused the login: the only failure that means this device is signed out. */
export function loginRefused(e: unknown): boolean {
  return e instanceof ApiError && e.code === 'invalid_credentials';
}

async function loginWith(platform: Platform, account: AccountHandle, authPub: Uint8Array): Promise<LoginResult> {
  const auth_pub = toBase64url(authPub);
  const { nonce } = await data<{ nonce: string }>(platform.transport.request('POST', '/v1/auth/challenge', { auth_pub }));
  const sig = await platform.vault.signServerMessage(account, 'login', fromBase64url(nonce));
  return data<LoginResult>(platform.transport.request('POST', '/v1/auth/login', { auth_pub, nonce, sig: toBase64url(sig) }));
}

async function data<T>(res: Promise<{ status: number; body: unknown }> | { status: number; body: unknown }): Promise<T> {
  const { status, body } = await res;
  if (status >= 200 && status < 300) return (body as { data: T }).data;
  const err = (body as { error?: { code?: string; message?: string } } | undefined)?.error;
  throw new ApiError(status, err?.code ?? 'unknown', err?.message ?? `HTTP ${status}`);
}
