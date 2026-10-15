/**
 * The `ClientCore` facade (client-spec §3.2): one object per signed-in
 * account, which is all the UI talks to. It owns the account and its sync
 * engine, and turns them into `Readable`s: the lists, Today, My tasks, the
 * connection and the security warnings.
 *
 * The `Platform` is per server: its transport holds that server's base URL
 * and cookie. The shell builds it from the server the user typed (or an
 * invite named) and passes the same URL here, for the login message.
 */

import { Account, type Session } from '../account/account.js';
import { toBase64url } from '../base64url.js';
import type { AddOptions, AddTaskOptions } from '../list/doc.js';
import type { ListSnapshot } from '../list/item.js';
import { moveItem } from '../list/move.js';
import type { Platform } from '../platform/index.js';
import { InviteError, openInvite, type OpenedInvite } from '../invite/join.js';
import { parseInviteLink } from '../invite/link.js';
import type { ListId, ListState, LogEvent, PublicIdentity, VaultWarning } from '../platform/vault.js';
import { Writable, type Readable } from '../readable.js';
import { planReminders } from '../reminders/plan.js';
import { searchLists, type SearchHit } from '../search/search.js';
import { addMember, createInvite, removeMember, revokeInvite, type CreatedInvite } from '../sync/members.js';
import { Sync, type ConnectionState, type SyncOptions } from '../sync/sync.js';
import { addDays, localDateString } from '../views/dates.js';
import { inOrder, myTasksView, openTaskCount, todayView, type MyTasks, type TaskRef } from '../views/views.js';

export interface ClientCoreOptions {
  sync?: SyncOptions;
  /** The clock for Today, My tasks and reminders; a test seam. */
  now?: () => Date;
  /** The UI's words for notifications: the generic title (privacy setting) and the Inbox's name as a body. */
  reminderText?: { generic: string; inbox: string };
}

/** The account's preferences as the UI sees them, wherever each is kept. */
export interface Settings {
  /** Notifications show the task title and list name (default), or only a generic text (client-spec §7.2). This device only. */
  reminderTitles: boolean;
  /** Times shown on a 12- or 24-hour clock; unset follows the device's language. The same on every device (`AccountSettings`). */
  clock?: '12h' | '24h';
}

/** The part of `Settings` kept on this device only. */
type DeviceSettings = Omit<Settings, 'clock'>;

const DEFAULT_SETTINGS: DeviceSettings = { reminderTitles: true };
/** `LocalStore` table, keyed by the account's vault handle; JSON `DeviceSettings`. Not secret: display preferences. */
const SETTINGS_TABLE = 'settings';
/** The Inbox document's view order for Today and My tasks (`ListDoc.viewOrder`). */
const TASK_ORDER = 'tasks';

/** client-spec §3.3. All but `invite-already-used` come from the vault. */
export type SecurityWarning = VaultWarning | { kind: 'invite-already-used' };

export interface ListSummary {
  id: ListId;
  /** Empty for the Inbox, which has no name of its own; the UI names it. */
  name: string;
  inbox: boolean;
  openTasks: number;
  /** Ticked-off tasks, for the list's progress. */
  doneTasks: number;
  role: 'owner' | 'member';
  /** Everyone in the list, the owner first, from the verified log. */
  members: PublicIdentity[];
  /** More than one member. */
  shared: boolean;
  /** What members are called in this list, by member id; a member with no name isn't in it. */
  names: Record<string, string>;
}

/** The members' names: each one's account name, which their own app wrote into the list (`ClientCore.shareName`). */
export function memberNames(people: Record<string, string> = {}, state: ListState | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of state?.members ?? []) {
    const id = toBase64url(m.signPub);
    if (people[id] !== undefined) out[id] = people[id];
  }
  return out;
}

export interface SessionsApi {
  list(): Promise<Session[]>;
  revoke(id: string): Promise<void>;
}

/** Link invites, from the joiner's side (E2EE §6.1). */
export interface InviteApi {
  /** Reads an invite link for this account's server: what its package says, for the Join screen, and `join()`. */
  open(link: string): Promise<JoinableInvite>;
}

export interface JoinableInvite extends OpenedInvite {
  /** Joins and starts syncing the list. Rejects with an `InviteError`. */
  join(): Promise<ListId>;
}

/** One list: its document operations (client-spec §3.1) and, from the verified log, who is in it. */
export interface ListHandle {
  readonly id: ListId;
  readonly inbox: boolean;
  readonly role: 'owner' | 'member';
  readonly members: PublicIdentity[];
  /** `ListSummary.names` when the handle was made. */
  readonly names: Record<string, string>;
  /** The list's name and items, on every change, local or remote. */
  readonly content: Readable<ListSnapshot>;

  setName(name: string): void;
  addTask(title: string, opts?: AddTaskOptions): string;
  addNote(text: string, opts?: AddOptions): string;
  /** The mutators below return false when the item no longer exists (e.g. removed by a sync). */
  setTitle(id: string, title: string): boolean;
  setBody(id: string, body: string | null): boolean;
  toggleDone(id: string, done?: boolean): boolean;
  setDue(id: string, due: string | null): boolean;
  setReminder(id: string, at: number | null): boolean;
  assign(id: string, assignee: string | null): boolean;
  move(id: string, toIndex: number): boolean;
  remove(id: string): boolean;
  /** Moves an item to another list, keeping its id (list/move.ts). */
  moveToList(id: string, target: ListId, index?: number): boolean;
  /** Owner only; the server refuses the Inbox. */
  delete(): Promise<void>;

  // Sharing (client-spec §3.4). The owner-only ones are refused for members, by the vault and the server.
  /** Link invites still open, from the verified log (expired ones too, until the owner's app rotates). */
  readonly openInvites: { invitePub: Uint8Array; expires: number }[];
  /** Owner: adds one of `knownContacts()` (E2EE §6.3). */
  addMember(signPub: Uint8Array): Promise<void>;
  /** Owner: removes a member with a new key; they keep what they have already seen (E2EE §8.1). */
  removeMember(signPub: Uint8Array): Promise<void>;
  /** Owner: a single-use link, by default for 48 hours. Send it over a channel the server doesn't see. */
  createInvite(opts?: { ttlMs?: number }): Promise<CreatedInvite>;
  /** Owner: closes a link invite and rotates the list key it carried. */
  revokeInvite(invitePub: Uint8Array): Promise<void>;
  /** Any member but the owner: leaves and forgets the list here. */
  leave(): Promise<void>;
}

export class ClientCore {
  readonly sessions: SessionsApi;
  readonly invites: InviteApi;
  private readonly summaries = new Writable<ListSummary[]>([]);
  private readonly todayRefs = new Writable<TaskRef[]>([]);
  private readonly mine = new Writable<MyTasks>({ today: [], upcoming: [], planned: [] });
  private readonly states = new Map<ListId, ListState>();
  private readonly now: () => Date;
  private readonly unhook: (() => void)[] = [];
  private readonly prefs = new Writable<Settings>(DEFAULT_SETTINGS);
  private device = DEFAULT_SETTINGS;
  private midnight: ReturnType<typeof setTimeout> | undefined;
  private statesQueued = false;
  private remindersQueued = false;
  private closed = false;
  /** What the OS was last given, so an edit that changes no reminder doesn't reach it. */
  private scheduled = '';

  private constructor(
    private readonly account: Account,
    private readonly sync: Sync,
    private readonly opts: ClientCoreOptions,
  ) {
    this.now = opts.now ?? (() => new Date());
    this.sessions = { list: () => account.sessions(), revoke: (id) => account.revokeSession(id) };
    this.invites = { open: (link) => this.openInvite(link) };
  }

  /** Creates an account on `server`; `name` is optional (empty for none). Show `accountKeyWords` once (Welcome → create account). */
  static async signUp(platform: Platform, server: string, name = '', opts: ClientCoreOptions = {}): Promise<{ core: ClientCore; accountKeyWords: string[] }> {
    const { account, words } = await Account.signUp(platform, server, name);
    return { core: await ClientCore.start(account, opts), accountKeyWords: words };
  }

  /** Signs in on a new device. `null` for a typo in the words, caught before any request. */
  static async signIn(platform: Platform, server: string, words: string[], opts: ClientCoreOptions = {}): Promise<ClientCore | null> {
    const account = await Account.signIn(platform, server, words.join(' '));
    return account === null ? null : ClientCore.start(account, opts);
  }

  /** Startup: the accounts kept on this device, rendered from the cache and syncing. Empty means Welcome. */
  static async resume(platform: Platform, opts: ClientCoreOptions = {}): Promise<ClientCore[]> {
    const cores: ClientCore[] = [];
    for (const account of await Account.resume(platform)) {
      const core = await ClientCore.start(account, opts);
      cores.push(core);
      // Picks up a newer identity, and warns if the server serves an older bundle than this device has seen (E2EE §3.3).
      void account.loadIdentity().catch(() => undefined);
      // Settings changed on another device reach this one here, and a change made here that the server hasn't taken is sent.
      // The name is one of them: a rename elsewhere arrives here, and goes on to the lists (`begin`).
      void account.syncSettings().catch(() => undefined);
    }
    return cores;
  }

  private static async start(account: Account, opts: ClientCoreOptions): Promise<ClientCore> {
    const core = new ClientCore(account, new Sync(account, opts.sync), opts);
    await core.begin();
    return core;
  }

  private async begin(): Promise<void> {
    const stored = await this.account.platform.storage.get(SETTINGS_TABLE, this.account.handle);
    if (stored !== null) {
      const { clock, ...device } = JSON.parse(new TextDecoder().decode(stored)) as Partial<Settings>;
      this.device = { ...DEFAULT_SETTINGS, ...device };
      // The clock was kept here before it followed the account: hand it over once.
      if (clock !== undefined) {
        await this.saveDevice({});
        if (this.account.settings.value.clock === undefined) await this.account.changeSettings({ clock });
      }
    }
    this.unhook.push(this.account.settings.subscribe(() => this.showSettings()));
    // A name that changes while the app runs came from the user or, through the settings, from another device: either way it is the newest.
    this.unhook.push(this.account.name.subscribe(() => this.shareName(true)));
    this.unhook.push(
      this.sync.onChange((list) => {
        if (list === null) this.refreshStates();
        else {
          // A list's content has arrived: a name still missing from it can be filled in now.
          this.shareName(false);
          this.recompute();
        }
      }),
    );
    await this.sync.start();
    await this.sync.track(this.account.inbox);
    await this.loadStates();
    this.scheduleMidnight();
  }

  // --- account ---------------------------------------------------------

  /** The API base URL this account signs in to. */
  get server(): string {
    return this.account.server;
  }

  /** Our `identity_sign_pub`, base64url: what `assignee` holds for us. */
  get me(): string {
    return toBase64url(this.account.identity.signPub);
  }

  /** What the user is called; the others in a shared list see it. Empty for none: a name is optional. */
  get name(): Readable<string> {
    return this.account.name;
  }

  /**
   * Changes the name, here and in every list we share; an empty one clears
   * it. Works offline, like the other account settings: the other devices
   * have it at their next start.
   */
  setName(name: string): Promise<void> {
    return this.account.setName(name);
  }

  /**
   * Writes our name into the lists we share, where the others read it
   * (client-spec §3.7). Only where nothing was written yet, unless
   * `overwrite`: then the name just came from the user or another device
   * and replaces an older one, and an empty name clears it. A device that
   * hasn't heard of a change yet mustn't write the old name back, which is
   * why a cleared name stays in the list as empty (`ListDoc.setPerson`).
   * For the same reason a missing name is only filled in once the list's
   * content has arrived: before that, "nothing written" is not known yet.
   */
  private shareName(overwrite: boolean): void {
    const name = this.account.name.value;
    for (const [id, state] of this.states) {
      const doc = this.sync.doc(id);
      if (doc === undefined || state.members.length < 2 || !state.members.some((m) => toBase64url(m.signPub) === this.me)) continue;
      const have = doc.person(this.me);
      if (overwrite ? (have ?? '') !== name : have === undefined && name !== '' && this.sync.hasContent(id)) doc.setPerson(this.me, name);
    }
  }

  /** Stops syncing and forgets the account and its content on this device. */
  async signOut(): Promise<void> {
    this.close();
    await this.sync.signOut();
    await this.account.platform.storage.delete(SETTINGS_TABLE, this.account.handle);
    await this.account.platform.notifications.replaceAll([]);
  }

  /**
   * Deletes the account for good (E2EE §8.4): it leaves the lists of
   * others, which the server can't do for it, and the server then deletes
   * the account with the lists it owns, for everyone in them; this device is
   * cleared as on sign-out. Needs the server. Stopped halfway, the account
   * is still there and this can be run again.
   */
  async deleteAccount(): Promise<void> {
    for (const [id, state] of [...this.states]) if (!this.isOwner(state)) await this.sync.leaveList(id);
    await this.account.delete();
    await this.signOut();
  }

  // --- settings and notifications ---------------------------------------

  get settings(): Readable<Settings> {
    return this.prefs;
  }

  /** The privacy setting for notifications (client-spec §7.2); reschedules at once. */
  async setReminderTitles(show: boolean): Promise<void> {
    await this.saveDevice({ reminderTitles: show });
    this.queueReminders();
  }

  /** For every device of the account: here at once, on the others at their next start. Works offline; sent when the server is reached. */
  async setClock(clock: '12h' | '24h'): Promise<void> {
    await this.account.changeSettings({ clock });
  }

  private async saveDevice(change: Partial<DeviceSettings>): Promise<void> {
    const next = { ...this.device, ...change };
    await this.account.platform.storage.put(SETTINGS_TABLE, this.account.handle, new TextEncoder().encode(JSON.stringify(next)));
    this.device = next;
    this.showSettings();
  }

  /** `settings` from its two halves. A clock this app doesn't know (a newer app's) counts as unset. */
  private showSettings(): void {
    const { clock } = this.account.settings.value;
    this.prefs.set(clock === '12h' || clock === '24h' ? { ...this.device, clock } : this.device);
  }

  notificationPermission(): Promise<'granted' | 'denied' | 'prompt'> {
    return this.account.platform.notifications.permission();
  }

  requestNotificationPermission(): Promise<'granted' | 'denied' | 'prompt'> {
    return this.account.platform.notifications.requestPermission();
  }

  /** E2EE §9.2. Every other session is signed out. Show the new words once. */
  async changeAccountKey(): Promise<{ accountKeyWords: string[] }> {
    return { accountKeyWords: await this.account.changeAccountKey() };
  }

  showAccountKey(): Promise<string[]> {
    return this.account.showWords();
  }

  /** Closes the connection and stops every timer; the outbox stays for next time. */
  async stop(): Promise<void> {
    this.close();
    await this.sync.stop();
  }

  private close(): void {
    this.closed = true;
    clearTimeout(this.midnight);
    for (const off of this.unhook.splice(0)) off();
  }

  // --- lists -----------------------------------------------------------

  /** Every list on this device, oldest first. */
  get lists(): Readable<ListSummary[]> {
    return this.summaries;
  }

  get inbox(): ListId {
    return this.account.inbox;
  }

  async createList(name: string): Promise<ListId> {
    const id = await this.sync.createList(name);
    await this.loadStates();
    return id;
  }

  /** A list on this device; throws for one it doesn't hold. */
  list(id: ListId): ListHandle {
    const doc = this.sync.doc(id);
    const state = this.states.get(id);
    if (doc === undefined) throw new Error(`no list ${id} on this device`);
    const sync = this.sync;
    return {
      id,
      inbox: id === this.account.inbox,
      role: state !== undefined && this.isOwner(state) ? 'owner' : 'member',
      members: state?.members ?? [],
      names: memberNames(doc.people(), state),
      content: doc.readable(id),
      setName: (name) => doc.setName(name),
      addTask: (title, opts) => doc.addTask(title, opts),
      addNote: (text, opts) => doc.addNote(text, opts),
      setTitle: (item, title) => doc.setTitle(item, title),
      setBody: (item, body) => doc.setBody(item, body),
      toggleDone: (item, done) => doc.toggleDone(item, done),
      setDue: (item, due) => doc.setDue(item, due),
      setReminder: (item, at) => doc.setReminder(item, at),
      assign: (item, assignee) => doc.assign(item, assignee),
      move: (item, toIndex) => doc.move(item, toIndex),
      remove: (item) => doc.remove(item),
      moveToList(item, target, index) {
        const to = sync.doc(target);
        if (to === undefined) throw new Error(`no list ${target} on this device`);
        return moveItem(doc, to, item, index);
      },
      delete: () => sync.deleteList(id),
      openInvites: state?.openInvites ?? [],
      addMember: (signPub) => this.changeMembers(() => addMember(this.account, id, signPub)),
      removeMember: (signPub) => this.changeMembers(() => removeMember(this.account, id, signPub)),
      createInvite: async (opts) => {
        const made = await createInvite(this.account, id, doc.snapshot(id).name, this.account.name.value, opts?.ttlMs);
        await this.loadStates();
        return made;
      },
      revokeInvite: (invitePub) => this.changeMembers(() => revokeInvite(this.account, id, invitePub)),
      leave: () => sync.leaveList(id),
    };
  }

  /** Everyone we share, or shared, a list with: "Share with…" offers these before a link (client-spec §3.4). */
  knownContacts(): Promise<PublicIdentity[]> {
    return this.account.platform.vault.knownContacts(this.account.handle);
  }

  /** An identity's safety code, six groups of five digits, to compare out of band (E2EE §6.4). */
  safetyCode(id: PublicIdentity): Promise<string[]> {
    return this.account.platform.vault.safetyCode(id);
  }

  /** Others joining, being added or removed, leaving, and new epochs, in our lists ("Anna joined"). */
  get notices(): Readable<LogEvent[]> {
    return this.account.notices;
  }

  private async openInvite(link: string): Promise<JoinableInvite> {
    const parsed = parseInviteLink(link);
    if (parsed === null) throw new InviteError('bad-link');
    if (parsed.server !== this.server.replace(/\/+$/, '')) throw new InviteError('other-server');
    const opened = await openInvite(this.account.platform, link);
    return {
      ...opened,
      join: async () => {
        const list = await this.sync.join(opened);
        await this.loadStates();
        return list;
      },
    };
  }

  /** A membership change, then the verified states again. */
  private async changeMembers(change: () => Promise<unknown>): Promise<void> {
    await change();
    await this.loadStates();
  }

  // --- views -----------------------------------------------------------

  /** Open tasks that are mine, due today or earlier, across every list. */
  get today(): Readable<TaskRef[]> {
    return this.todayRefs;
  }

  get myTasks(): Readable<MyTasks> {
    return this.mine;
  }

  /**
   * The order the user dragged Today and My tasks into: task ids, first
   * first. Kept in the private Inbox document, so it syncs to their devices
   * and nobody else sees it. Tasks not in it follow in the usual order.
   */
  setTaskOrder(ids: readonly string[]): void {
    this.sync.doc(this.account.inbox)?.setViewOrder(TASK_ORDER, ids);
  }

  async search(query: string): Promise<SearchHit[]> {
    return searchLists(this.snapshots(), query);
  }

  // --- health ----------------------------------------------------------

  get connection(): Readable<ConnectionState> {
    return this.sync.connection;
  }

  get warnings(): Readable<SecurityWarning[]> {
    return this.account.warnings;
  }

  // --- internals -------------------------------------------------------

  private isOwner(state: ListState): boolean {
    return toBase64url(state.owner.signPub) === this.me;
  }

  private snapshots(): ListSnapshot[] {
    return this.sync.lists().flatMap((id) => this.sync.doc(id)?.snapshot(id) ?? []);
  }

  /** The set of lists or their membership changed: re-read every verified state from the vault, once at a time. */
  private refreshStates(): void {
    if (this.statesQueued) return;
    this.statesQueued = true;
    queueMicrotask(() => {
      this.statesQueued = false;
      void this.loadStates();
    });
  }

  private async loadStates(): Promise<void> {
    const { vault } = this.account.platform;
    const ids = this.sync.lists();
    const states = await Promise.all(ids.map((id) => vault.listState(this.account.handle, id).catch(() => undefined)));
    this.states.clear();
    ids.forEach((id, i) => states[i] !== undefined && this.states.set(id, states[i]));
    this.shareName(false);
    this.recompute();
  }

  private recompute(): void {
    const lists = this.snapshots();
    const ctx = { me: this.me, today: localDateString(this.now()) };
    this.queueReminders();
    const order = this.sync.doc(this.account.inbox)?.viewOrder(TASK_ORDER) ?? [];
    this.todayRefs.set(inOrder(todayView(lists, ctx), order));
    const mine = myTasksView(lists, ctx);
    this.mine.set({ today: inOrder(mine.today, order), upcoming: inOrder(mine.upcoming, order), planned: inOrder(mine.planned, order) });
    this.summaries.set(
      lists
        .map((l) => ({ l, state: this.states.get(l.id) }))
        .sort((a, b) => (a.state?.created ?? Infinity) - (b.state?.created ?? Infinity) || a.l.id.localeCompare(b.l.id))
        .map(({ l, state }) => {
          const members = state?.members ?? [];
          return {
            id: l.id,
            name: l.name,
            inbox: l.id === this.account.inbox,
            openTasks: openTaskCount(l),
            doneTasks: l.items.filter((i) => i.type === 'task' && i.done).length,
            role: state !== undefined && this.isOwner(state) ? 'owner' : 'member',
            members,
            shared: members.length > 1,
            names: memberNames(l.people, state),
          };
        }),
    );
  }

  /**
   * After every change, local or synced: hand the OS the reminders this
   * device should have (client-spec §7.2), once per tick and only when
   * they differ from what it already has.
   */
  private queueReminders(): void {
    if (this.remindersQueued || this.closed) return;
    this.remindersQueued = true;
    queueMicrotask(() => {
      this.remindersQueued = false;
      if (this.closed) return;
      const { notifications } = this.account.platform;
      const text = this.opts.reminderText;
      const lists = this.snapshots().map((l) => (l.id === this.account.inbox && text !== undefined ? { ...l, name: text.inbox } : l));
      const reminders = planReminders({
        lists,
        me: this.me,
        now: this.now().getTime(),
        maxPending: notifications.maxPending,
        showTitle: this.prefs.value.reminderTitles,
        ...(text !== undefined ? { genericTitle: text.generic } : {}),
      });
      const key = JSON.stringify(reminders);
      if (key === this.scheduled) return;
      this.scheduled = key;
      notifications.replaceAll(reminders).catch(() => (this.scheduled = ''));
    });
  }

  /** Today changes at local midnight even when nothing else does. */
  private scheduleMidnight(): void {
    const now = this.now();
    const [y, m, d] = addDays(localDateString(now), 1).split('-').map(Number) as [number, number, number];
    const ms = new Date(y, m - 1, d).getTime() - now.getTime();
    this.midnight = setTimeout(() => {
      this.recompute();
      this.scheduleMidnight();
    }, ms + 1000);
  }
}
