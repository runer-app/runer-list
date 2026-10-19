# Client specification

**Scope:** The client application: the Rust vault, headless `client-core`, the shared UI, and the Tauri shell for desktop and mobile.
**Normative references (in `protocol/docs/`, shared with the server):**
[E2EE Protocol](../protocol/docs/e2ee-protocol.md) (**E2EE §n**) · [Sync Protocol](../protocol/docs/sync-protocol.md) (**Sync §n**) · [API Reference](../protocol/docs/api-reference.md). How the parts fit together: [architecture.md](architecture.md).

> This document does not redefine the protocol. Where it repeats protocol detail, the protocol docs win. It defines how the client is structured, which platforms it targets, and what each platform layer must provide.

---

## 1. Goals and non-goals

### Goals

1. **Fast capture beats everything.** Creating and completing a task must not take more taps than the app the user already has. The input never waits on the network or on crypto.
2. **One codebase** for macOS, Windows, Linux, iOS and Android via Tauri 2.
3. **Two layers under the UI.** The **vault** (Rust) holds every key and every verified membership log, and moves key material only where that verified state allows (§8). **`client-core`** (TypeScript) does everything else that isn't UI: sync, the list document, views, search, reminders. Both run unchanged in Node for tests.
4. **Plaintext never touches disk.** Only ciphertext and keys wrapped under the device cache key are persisted (E2EE §3.5).
5. **Offline-first.** Reading and editing work offline; changes queue in the outbox (Sync §4).
6. **One security core to review**, the vault, the same on every platform.

### Non-goals

Rich media, file attachments, comments, recurring tasks, calendar integration, team workspaces, granular access, AI, automation, sub-tasks, nested blocks, per-device keys, ownership transfer (E2EE §8.4), and server push (§7.2).

Accepted trade-offs: rigid ownership (the owner can't leave, and deleting a list removes it for everyone; there is no identity reset, so an owner stays owner for the life of their account, E2EE §9.3), and lists that grow forever (no archive).

### Platforms

macOS, Windows and Linux, with signed installers and an updater; iOS and Android from the same Tauri project. Every platform-specific capability sits behind an adapter from day one (§4).

---

## 2. Architecture

```text
┌────────────────────────────────────────────────────────────────┐
│ packages/ui          Svelte 5. A list of rows, not a document  │
│                      editor. Knows nothing about Tauri.        │
├────────────────────────────────────────────────────────────────┤
│ packages/client-core Headless TS. Sync, outbox, Yjs provider,  │
│                      list doc, views, search, reminder         │
│                      planning, invite links. Holds no keys.    │
│                      No DOM, no Tauri imports.                 │
├────────────────────────────────────────────────────────────────┤
│ Platform interface   (defined in client-core, §4), incl. Vault │
├───────────────────────────────────────┬────────────────────────┤
│ platform-tauri                        │ platform-node          │
│ desktop + mobile                      │ (tests)                │
│ vault commands, SQLite, Rust HTTP/WS, │ vault via napi-rs,     │
│ deep links, notifications             │ in-memory              │
├───────────────────────────────────────┴────────────────────────┤
│ crates/runer-vault  Rust. Keys (keychain), verified logs and  │
│                      their rules, every signature and seal.    │
│ crates/runer-crypto Rust. The byte-level crypto (E2EE §3–§7). │
└────────────────────────────────────────────────────────────────┘
```

**Dependency rule:** `ui` → `client-core` → `Platform` interface. Only the shells import a platform implementation. Lint rules enforce this: no `@tauri-apps/*` import outside `platform-tauri` and `apps/tauri`. Nothing in TypeScript sees a key, a raw signature operation or an unverified log.

### 2.1 Layout (`app/`)

```text
app/
├── packages/
│   ├── client-core/        headless client
│   │   └── src/{invite,sync,list,views,reminders,search,store,platform}/
│   ├── ui/                 Svelte components, routes, stores
│   └── platform-tauri/     TS side of the Tauri adapter
├── crates/
│   ├── runer-crypto/      byte-level crypto (§8)
│   ├── runer-vault/       keys, verified logs and their rules (§8)
│   └── runer-vault-node/  the vault as a Node add-on (napi-rs), for tests
├── apps/
│   └── tauri/              one Tauri 2 project for desktop AND mobile
│       ├── src/            entry: mounts ui with platform-tauri
│       └── src-tauri/      Rust: plugins, capabilities, gen/{android,apple}
└── e2e/                    client-core vs the real server
```

`platform-node` lives in `client-core/test/` and is not published. The shared log fixtures and crypto vectors live in `protocol/`, beside the protocol docs; client and server tests read the same files. A protocol change updates the docs, the data and both sides in one change (see the README).

---

## 3. `client-core`

Modules:

| Module | Responsibility | Reference |
|---|---|---|
| `account/` | Sign-up, sign-in, silent re-auth, change of account key, sessions; the vault signs | E2EE §3, §9 |
| `invite/` | Parsing invite links (the fragment never leaves the device, §6.3) and the join flow; the vault does the crypto | E2EE §6 |
| `sync/` | WebSocket protocol, outbox, catch-up, Yjs provider; hands logs and content to the vault in batches | Sync |
| `list/` | The list document: typed operations over the Yjs doc | §3.1, E2EE §5.1 |
| `views/` | Today, My tasks and search over decrypted lists | §3.6 |
| `reminders/` | Which notifications this device should have scheduled | §7.2 |

The membership logs are not here: parsing, verifying and applying them is the vault's job (§8), so no TypeScript code decides who is a member. `client-core` asks the vault for a list's `ListState` (owner, members, epoch, open invites) and shows it.

### 3.1 The list document

One Yjs document per list, used as a data store:

```text
name:   Y.Text
order:  Y.Array<item_id>
items:  Y.Map<item_id, Y.Map>   // {type: task|note, title, body?, done, due?, remind_at?, assignee?}; title and body are Y.Text
views:  Y.Map<view, item_id[]>  // Inbox only: the user's own order of Today and My tasks
people: Y.Map<member id, name>   // §3.7
```

- **Tasks and notes**. A note's `title` is its text, in a small subset of Markdown: `#`–`###` headings, `-`/`1.` lists, `>` quotes, `---` dividers, **bold**, *italic*, `code`, ~~strike~~; one block per line, and a line ending in `\` continues the block (Shift+Enter). The lines between two tasks are one note: only tasks separate notes. There is no heading type; older `heading` items are an unknown type.
- **Flat.** A heading is a line of a note, not a container. No sub-tasks.
- **Text merges.** `title` and `body` are `Y.Text`, written as a diff of the old and new text, so two people editing one note at once both keep their edits. `setTitle`/`setBody` take the whole new string and write only what changed between the common prefix and suffix, never splitting a surrogate pair. The list's `name` is written the same way, so concurrent renames merge instead of concatenating. Reads also accept a plain string; the first edit replaces it with a `Y.Text`. Other fields are last-writer-wins.
- **Field formats.** `due` is a calendar date string `YYYY-MM-DD`: a date only, with no time and no time zone, compared with the device's local date. `remind_at` is Unix milliseconds, a date and a time. `assignee` is the member's `identity_sign_pub` in base64url.
- **One date per task, with an optional time.** The UI shows no separate reminder: a task has a date (`due`), or a date and a time, stored as `due` plus `remind_at` at that time on that day. Removing the time or the date removes the reminder. A `remind_at` without `due` (written before this) is shown as that date and time.
- **Tasks are first-class:** stable ids, per-field merge, so Today, assignment and completion work across lists.
- **Unknown `type`** values from a newer client are shown as plain text and preserved, never dropped.
- **Intro text** (the line under a list's title) is ordinary text at the top. There is no special field.
- `assignee` is an identity reference taken from the verified membership log, never a free-text name.
- **Moving an item between lists** (e.g. Inbox → a list) keeps its id: insert into the target first, then delete from the source, and de-duplicate by id, so a crash or offline gap can never leave two copies. When a crash left both, the copy in the list with the smallest list id wins, so all devices agree.
- **Moving within a list** is delete + insert in `order`, so concurrent moves can duplicate an id there. Reads skip repeats and ids without an item, and append items missing from `order`.
- **Mutators return `false` for an item that no longer exists** instead of throwing, because a sync can remove it under the UI.
- **Why Yjs:** stable ids, per-field merge and offline edits without writing merge code. The rejected alternative was hand-written item ops with last-write-wins per field, which means writing merging, tombstones, ordering and compaction ourselves.

The UI never touches Yjs directly. `list/` exposes typed operations (`addTask`, `addNote`, `setTitle`, `toggleDone`, `setDue`, `setReminder`, `assign`, `move`, `remove`, `moveToList`), so the document shape can change without touching the UI.

### 3.2 Public API shape

One `ClientCore` per signed-in account (`client-core/src/core/`), with observable state for the UI. Getting one is static, since there is no account before sign-up:

```ts
class ClientCore {
  // getting one; `platform` is per server (its transport holds the base URL and cookie)
  static signUp(platform: Platform, server: ServerUrl, name?: string): Promise<{ core: ClientCore; accountKeyWords: string[] }>;
  static signIn(platform: Platform, server: ServerUrl, words: string[]): Promise<ClientCore | null>; // null: typo
  static resume(platform: Platform): Promise<ClientCore[]>;   // startup, from the cache; empty means Welcome

  // account (E2EE §3, §9)
  server: ServerUrl;
  me: string;                                     // our identity_sign_pub, what `assignee` holds
  signOut(): Promise<void>;
  changeAccountKey(): Promise<{ accountKeyWords: string[] }>;
  showAccountKey(): Promise<string[]>;
  sessions: SessionsApi;                          // list, revoke

  // lists (E2EE §4–6, §8)
  lists: Readable<ListSummary[]>;                 // name, open-task count, inbox, role, members, shared; oldest first
  inbox: ListId;                                  // private list created at sign-up; its name is empty, the UI names it
  createList(name: string): Promise<ListId>;
  list(id: ListId): ListHandle;                   // content, document ops (§3.1), moveToList, delete, role, members
  invites: InviteApi;                             // join from a link

  // views, computed locally
  today: Readable<TaskRef[]>;                     // Today; recomputed at local midnight too
  myTasks: Readable<MyTasks>;                     // today, upcoming, planned
  search(query: string): Promise<SearchHit[]>;

  // health
  connection: Readable<ConnectionState>;
  warnings: Readable<SecurityWarning[]>;
  stop(): Promise<void>;
}
```

`Readable` is a minimal subscribe interface (Svelte store contract), so `client-core` does not depend on Svelte.

Owner-only actions (invite, approve, remove, delete list) are refused by the vault and by the server for other members; the `ListHandle` exposes `role: 'owner' | 'member'` from the vault's verified log so the UI can hide them. Any member can leave. The owner cannot leave; they delete the list, so the List screen offers "Delete list" to the owner and "Leave list" to members.

**A list can be made offline.** `createList` makes and verifies the list on the device and it is usable at once. Its genesis is kept (`created` in the local store) until the server takes it on the next connect; until then the list isn't subscribed and nothing of it is sent. Sharing or deleting it still needs the server.

**No account id.** Nothing needs the server's `account_id`; the client identifies accounts by vault handle, server and `auth_pub`.

### 3.3 Security warnings are first-class

Each is a typed `SecurityWarning` that the UI must show and cannot dismiss silently. The log-derived ones come from the vault, which holds the verified logs.

| Warning | Source |
|---|---|
| `log-fork` / `log-rollback` | E2EE §4.4 |
| `bundle-rollback` | Server served a lower `bundle_gen` than seen (E2EE §3.3) |
| `withheld-removal` | Live update with an older epoch (E2EE §4.4, §7) |
| `invite-already-used` | E2EE §6 |

They show as a banner on every screen until the vault clears them, with a detail screen; there is no dismiss.

**`invite-already-used` is never raised for link invites.** A spent, revoked or expired link gives `InviteError('not-usable')`, whose text tells the joiner to ask the owner; the owner is told of every join. It can't be told apart from this side: the server answers all three with the same `410 invite_not_usable` and stops serving the invite's log, so the joiner never sees the entry that closed it. A warning would need a server change.

### 3.4 Sharing

Two paths (protocol in E2EE §6):

1. **Known contact** (E2EE §6.3: anyone who is, or was, in a verified log of one of your lists): the owner appends `add` + envelope directly, with no link and no `invite_proof`. It is instant and bound to that exact identity. The subject's keys come **only from a verified log**, never from a server lookup; the vault enforces this, since it builds the `add` only for someone in a log it has verified. The UI offers these people under "Share with…" before the link option.
2. **New person, link invite** (E2EE §6.1): the owner signs an `invite` entry into the list's log when creating the link; the invitee joins on their own with a `join` entry, can read at once, and the owner gets a notice ("Anna joined"). Single-use, revocable (`uninvite`), expires after 48 hours by default (at most 7 days). A new epoch voids unused invites, and the app offers to send a new link. After an `uninvite`, or when an unused invite expires, the owner's app appends `rotate` the next time it is online, because the unused link carried the list key. The safety code (E2EE §6.4) is an optional check afterwards.

The approve-first invite (E2EE §6.2) is in the protocol; the app has no UI for it.

There is no extra PIN for sensitive invites.

- **Known contacts include former members** of any of our lists, with the keys the log admitted them with.
- **"Rotation due" is the vault's call, with its reason.** `ListState.rotationDue` is `member-left` after a `leave` (any member's app rotates, E2EE §8.2) or `invite-revoked` after an `uninvite` (only the owner's app rotates, E2EE §6.1); a leave outranks a revoked link. The next `remove` or `rotate` clears it. Only the owner's app rotates away an unused invite that expired. Two members racing rotate once: the loser's `409` refetches the log, and the rotation is no longer due.
- **Notices only for news.** Membership events become `ClientCore.notices` when a log this device already held grows; a log fetched from genesis (a new device, a list just joined) replays its history silently, and our own entries never make a notice.
- **"You no longer have access to Groceries."** When the server stops listing a list someone else owns, it is forgotten here and makes a `lost-access` notice, named as this device last saw it. It doesn't say whether the list was deleted or we were removed. The owner's own lists, and one left on this device, just go. A list whose log can't be fetched or verified keeps its last verified state and doesn't hold up the others.

### 3.5 Multi-server

The server URL is part of the account. An invite link names its host (E2EE §6), so a user can belong to more than one server. `client-core` instances are keyed by `(server URL, auth_pub)`.

### 3.6 Views

- **Whose tasks.** A task is "mine" when assigned to me or to nobody; the same rule as reminders (§7.2).
- **My tasks split.** My tasks lives under Today, with no screen of its own: overdue and today's tasks first, then "Next 7 days", then "Later". Undated tasks are not listed there.
- **The order of Today and My tasks** is personal and kept in the Inbox document (`views`, last writer wins), so it syncs to the user's devices and to nobody else. Tasks not in it follow in the usual order. A task is dragged within its section only, since moving it between sections would mean changing its date.

### 3.7 Account name and settings

- **The server holds no personal information about an account.** An account is a set of keys: sign-up sends keys only, and there is no email address, anywhere. The server can't tell who an account belongs to, and has nothing to leak or to hand over.
- **A name is optional, and lives in the sealed account settings.** Sign-up offers a name field and works with it empty; Settings changes the name or clears it. The name is the `name` field of the account settings below, which the server stores sealed and can't read, so it works like any other setting: a change takes effect at once, works offline, and reaches the user's other devices at their next start. The app trims a name, turns line breaks and control characters into spaces, drops text-direction overrides and cuts it at 60 characters (`cleanName` in `src/list/doc.ts`).
- **Other members read the name from the list**, never from the server. Each list document has a `people` map (§3.1), and a member's own app writes its name there under its member id (`ClientCore.shareName`): where nothing was written yet whenever membership changes (joining, being added, someone joining our list), and over an older one whenever the name changes while the app runs, whether the user changed it in Settings or the settings sync brought a change from another device. A device that hasn't heard of a change only fills in a name where none was ever written, so it can't write the old one back. It does that only once the list's content has reached it, since before that it can't know whether one was written. For the same reason clearing a name writes an empty entry instead of removing the entry: an empty entry reads as "no name" and is left alone by a device that still has the old name. Nobody is renamed in the Share dialog and Join asks for no name.
- **A member without a name is shown by a tag from their key**: "Member 3FA2", four hex digits of their signing key, the same on every device. That covers a member who has no name, one whose name hasn't synced yet, and one whose entry is empty after cleaning, in the member list, the list's summary line, the assignee picker and the notices ("Member 3FA2 joined Groceries"). No screen shows a blank where a name would be.
- **Names are untrusted text.** The server never sees a name and so validates nothing, and the `people` map and an invite's `owner_name` are written by other people's apps. Every name is therefore cleaned again where it is read (`ListDoc.people`, `openInvite`), with the same rules as for the user's own name, and the screens that show names cut or wrap a long one.
- **The invite says who it is from.** Before joining, the invitee can't read the list document, so the link's package carries the owner's name as it was when the link was made (`owner_name`, E2EE §6.1; sealed with the link's secret, so the server doesn't see it). Join shows "Anna invited you to a list". The field is left out when the owner has no name, and Join then says "You've been invited to a list" rather than showing a key tag. After joining, the name comes from the list document like any other.
- **A link has no name**: whoever joins goes by their own name, so making a link asks for nothing.
- **Not enforced:** the `people` map is ordinary shared content, so a changed client could write someone else's entry (that member's app puts it right the next time their name changes); signing the entries would close that. A name is only a label either way: the safety code stays the real check.
- **The name and the time format follow the account.** The server keeps the account's settings as `settings_sealed` with a `settings_gen` that a write must name (E2EE §3.3). The plaintext is JSON, `{"v": 1, "clock": "12h" | "24h", "name": "Anna"}`, each field optional (`AccountSettings` in `src/account/`); fields this app doesn't know are carried along and written back. A change takes effect on the device at once and works offline: the device keeps its settings in `LocalStore` table `account-settings` and a change the server hasn't taken in `account-settings-unsent` (both sealed), and sends it in the background and at every start (`Account.syncSettings`). On a `409` it fetches the other device's settings, puts its own change on top and tries again, so two devices changing different settings both win and the last one wins on the same setting. There is no push: another device has the change at its next start.
- **The reminder privacy setting stays on the device**, since it is about what one device shows on its lock screen (§7.2).
---

## 4. The `Platform` interface

The only thing each shell implements.

```ts
interface Platform {
  vault: Vault;                 // keys, verified logs, every signature and seal (§8)
  storage: LocalStore;          // content ciphertext, outbox
  transport: Transport;         // HTTP + WebSocket to the server
  links: LinkSource;            // incoming invite links
  lifecycle: Lifecycle;         // foreground/background, online/offline
  notifications: Notifier;      // local reminders
  info: { kind: 'desktop' | 'ios' | 'android' | 'node' };
}

interface LocalStore {              // key-value + ordered ranges; everything stored is already ciphertext
  get(table: string, key: string): Promise<Uint8Array | null>;
  put(table: string, key: string, value: Uint8Array): Promise<void>;
  delete(table: string, key: string): Promise<void>;
  range(table: string, prefix: string, opts?: { after?: string; limit?: number }): AsyncIterable<[string, Uint8Array]>;
  tx<T>(fn: (tx: LocalStore) => Promise<T>): Promise<T>;
}

interface Transport {
  request(method: string, path: string, body?: unknown): Promise<HttpResponse>;
  openSocket(path: string): Promise<Socket>;       // JSON text frames (Sync §1)
}

interface Notifier {
  readonly maxPending: number;                     // 64 on iOS
  replaceAll(reminders: { id: string; at: Date; title: string; body?: string }[]): Promise<void>;
  permission(): Promise<'granted' | 'denied' | 'prompt'>;
  requestPermission(): Promise<'granted' | 'denied' | 'prompt'>;   // Settings → notifications
}
```

`Socket`, `HttpResponse` and `Lifecycle` have the shapes `account/` and `sync/` use (`client-core/src/platform/`); every adapter must match them. `LinkSource` gives the raw link; `invite/link.ts` parses it.

**`Vault`** is defined in `client-core/src/platform/vault.ts`: accounts, lists, logs, the vault's own storage, content and sharing. It is the same Rust crate everywhere: behind Tauri commands in the app, through napi-rs in Node. It hands out handles, never key bytes, and takes batches for bulk work. There is no `secrets` field: only the vault touches the keychain.

### 4.1 Implementations

| Capability | Desktop (Tauri) | iOS / Android (Tauri) | Node (tests) |
|---|---|---|---|
| `vault` | `runer-vault` behind Tauri commands; keychain via the `keyring` crate | Same; iOS Keychain / Android Keystore via a mobile plugin (§6.2) | `runer-vault` via napi-rs; in-memory keychain |
| `storage` | SQLite written from Rust, behind `store` commands | Same | In-memory |
| `transport` | Rust-side HTTP + WS, holds the cookie (§5) | Same | Unit tests: in-process, to an in-memory server (`MemoryServer.transport()` in `client-core/test/platform-node/memory-server.ts`). e2e: HTTP and WebSocket to the real server (`app/e2e/http-transport.ts`) |
| `links` | `tauri-plugin-deep-link` (`runer://join`) | Custom scheme + universal/app links (§6.3) | Direct call |
| `lifecycle` | Window focus, `online` events | App foreground/background | Manual |
| `notifications` | `tauri-plugin-notification`, scheduled | Same; iOS limit 64 pending | Recorder |

---

## 5. Networking and authentication

The protocol settles this (E2EE §3.4, Sync §2):

- **All HTTP and WebSocket traffic goes through Tauri's Rust side**, which holds the session cookie. There is no CORS, no webview cookie policy and no dependence on the webview origin (which differs per OS).
- **Origin.** The server accepts exactly one `ORIGIN` (e.g. `tauri://localhost`) on every mutation and the WebSocket upgrade. The Rust transport sends that value explicitly on every platform. Either `tauri-plugin-http` / `tauri-plugin-websocket` set it (the http plugin's `unsafe-headers` feature), or a small custom `net` module does.
- **Login message.** Sign `"runer/v1/login" || 0x00 || uint16be(len(server_url)) || server_url || nonce`, where `server_url` is the API base URL the client connected to (the user typed it or it came from an invite link), never one the server supplies. The server checks it against `PUBLIC_URL`. Rekey uses the same construction with `"runer/v1/rekey"`.
- **Sessions** expire after 30 days idle / 180 days absolute. The client silently re-authenticates: the vault signs the login message with the Account Key it keeps. A session someone ended (`401 session_revoked`: revoked from another device, or the account key changed) is not replaced: sync stops as `signed-out` and the user signs in again with the 12 words.
- **Bearer tokens** are not needed for the Tauri app and are not built.

### 5.1 Reconnect and lifecycle

Follow Sync §2: backoff 1 s → 30 s with jitter; re-`sub` with the stored `since` per list; resend the outbox. In addition:

- Subscribe to **every** list on connect; Today needs all of them (Sync §8).
- On mobile, **close the socket when the app goes to the background** and reconnect on foreground. Do not rely on background execution.
- On foreground, on every reconnect and at least hourly, re-fetch every list's log head (E2EE §4.4).
- A `group` frame means a list was created, its log grew, or it was deleted: fetch and verify the log, open new envelopes, add a new list, and on removal or deletion drop the list locally.
- `bye` with `session-revoked`, or `401 session_revoked` when connecting: don't retry, sign in again. `remove` / `leave`: reconnect and refetch lists.

### 5.2 Sync details

Choices within what Sync allows:

- **`since` moves on our own `ack`s too**, not only on delivered updates, once `synced` for the list has arrived on the connection (Sync §4, "Ordering per connection"). Without it, a device that writes alone would never move `since` and never snapshot.
- **Edits in one tick become one update**: local Yjs updates are merged (`Y.mergeUpdates`) and sealed once per microtask.
- **Snapshots** cover the delivered `since`, though the state may hold more (our unacknowledged edits), which merging tolerates. Only our own acknowledged snapshot replaces stored content (it holds everything we had); snapshots from others are stored next to the updates and never replace them, since a bad snapshot must not erase content (E2EE §7).
- **Re-sealing after `stale-epoch`** opens our own outbox items with the old epoch's key in the vault and seals the plaintext again, so no plaintext is ever stored for the outbox.
- **Write errors:** `forbidden`, `too-large` and `bad-request` drop the outbox item (the edit stays in the document and goes out with the next snapshot); `rate-limited` resends after 1 s; errors on snapshots drop the snapshot.
- **One queue.** Frame handling, list refreshes and sealing run one at a time on one promise queue, so a list refresh delays sending (never editing).

---

## 6. Tauri shell (`apps/tauri`)

### 6.1 Rust side

It provides the capabilities in §4. Protocol logic in Rust is limited to the vault (§8): keys, the membership logs and their rules, and every signature and seal. Sync, the list document and the views stay in `client-core`.

| Plugin / module | Purpose |
|---|---|
| `store` commands (custom, `rusqlite`) | `LocalStore`: only `store_get`, `store_range` and `store_write` (a batch applied all or nothing) on its own SQLite file; no SQL reaches the webview. `tx` collects its writes on the TS side, where its reads see them, and sends one `store_write`. The vault's store is a separate file these commands can't open. |
| `net` commands (custom: `reqwest`, `tokio-tungstenite`, rustls) | `Transport` (§5): `net_request`, and `net_socket_open`/`send`/`close` with frames over a Tauri channel. Sends `Origin: tauri://localhost` on everything. Keeps its own cookie jar keyed by server origin, because the server's session cookie is `Secure` and a browser-style jar would never send it to an `http://` server such as a local one. In memory: after a restart the client signs in again with the vault's key. Chosen over the http/websocket plugins so the webview never holds a cookie or a general-purpose HTTP client. |
| `vault` commands | The `runer-vault` crate (§8) on the `keyring` keychain and its own SQLite file; holds keys and verified logs, returns handles. Bytes cross IPC as base64url. If the vault can't open (no keychain), every vault command fails with `Storage` and the UI says why. |
| `notify` commands + `tauri-plugin-notification` | Desktop shows a plugin notification at once (§6.2a), so `notify_replace_all` keeps the schedule in Rust and shows each reminder when due, while the app runs |
| `tauri-plugin-deep-link` | `runer://join` and app links; forwarded to the webview as the `runer://link` event, and `links_initial` |
| `tauri-plugin-updater` | Desktop only; signed updates. Registered only in a build whose config has `plugins.updater`, which the release workflow adds (the public key and the feed, `.github/workflows/release.yml`), so a build from a laptop never updates itself. Checked once at start, from Rust; the webview gets no updater command |
| `tauri-plugin-single-instance` | Desktop: a second launch (e.g. from a link) hands over to the running app |

**Capabilities:** `build.rs` declares the app's commands, so each needs a permission; the one capability (`capabilities/default.json`) grants exactly those plus listening to events. No plugin command is granted to the webview, there is no filesystem or shell access, and the webview has no HTTP of its own. Strict CSP (`tauri.conf.json`): `'self'` only, no remote sources, `connect-src` limited to IPC. The server never delivers executable code (E2EE §1).

**macOS window:** an overlay title bar (`titleBarStyle: Overlay`, title hidden, traffic lights at 20,22). The app runs up to the top edge, as most Mac apps do; the UI keeps `--titlebar` (36px, set by `apps/tauri/src/chrome.ts`) free at the top and drags the window there (`data-tauri-drag-region`, permission `core:window:allow-start-dragging`). Elsewhere `--titlebar` is the safe-area inset (0 on desktop; the iOS status bar later).

### 6.2 Secret storage per OS

Only the vault reads or writes these.

| OS | Backend | Notes |
|---|---|---|
| macOS | Keychain | `keyring` crate |
| Windows | Credential Manager | `keyring` crate |
| Linux | Secret Service (libsecret) | Needs a running keyring daemon. If none is present, fail loudly with instructions; never fall back to plaintext. |
| iOS | Keychain, `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly` | Through the Security framework directly (`OsKeychain` in `src-tauri/src/device.rs`), since `keyring` can't choose the class: every item is written as `AfterFirstUnlockThisDeviceOnly`. A write replaces the item (delete, then add), because an update keeps the class the item was made with. Compiles for the simulator; not run on a phone. |
| Android | Keystore-wrapped key + EncryptedSharedPreferences (or equivalent) | Community plugin or a small Kotlin plugin |

Avoid `tauri-plugin-stronghold`; upstream has deprecated it. An app lock (E2EE §12) would map to keychain access control (biometric or passcode) on every OS except Linux.

### 6.2a Platform check results

Run on a real iPhone (iOS 27.0.1, dev build) and on macOS. Both pass unless a row says otherwise.

| Check | Result |
|---|---|
| Explicit `Origin` from the Rust side | Works on iPhone and macOS: `tauri-plugin-http` with `unsafe-headers` sends the `Origin` we set; the server sees it. |
| Secret in the keychain | Works on iPhone and macOS with `keyring` (see §6.2). |
| Local notification | iPhone: a scheduled notification fires at the set time (the test used the plugin's `Schedule.at` with the time shifted by the timezone offset, since the plugin's iOS code reads the UTC time as local; whether the unshifted call also works wasn't recorded). macOS: the plugin ignores the schedule and shows the notification immediately (its documented desktop behaviour), so desktop reminders need our own scheduling while the app runs. |
| `runer://` link | iPhone: opens the app. macOS: opens the app from a built app in `/Applications`; it does not register in `cargo tauri dev`. |
| iOS release build | Links with Xcode 27.0, checked for the simulator: the app starts there. Without the fix below it fails, because Xcode 27 makes a Swift package's `@_cdecl` exports local in Release. `swift-rs` 1.0.8 makes each package's own exports global again with `llvm-objcopy` (so `rustup component add llvm-tools` is needed), but not those of its `SwiftRs` helper module; `scripts/ios-swift-helpers.sh`, run by the Xcode project after "Build Rust Code", makes those global and weak in the built library. Not built for a real iPhone, which needs a signing team. |
| Setup on Xcode 27 | Xcode's script sandbox must be off (`ENABLE_USER_SCRIPT_SANDBOXING = NO`), Xcode must start from a shell that has `cargo` on its PATH, and the crate must not build a `cdylib` for iOS. |

### 6.3 Invite links

- An invite is an `https://<server>/join#s` link (E2EE §6). The server's `/join` page hands the fragment to `runer://join`. Without the browser: the link can be pasted where a list is added (the sidebar's "+" and the Lists page), which opens Join.
- **One mechanism on every target:** the app registers the `runer://` scheme on desktop and mobile alike (the deep-link plugin's `desktop.schemes` and `mobile` config), and handles the same `runer://join?host=…#s` link everywhere. The flow is identical: `https://host/join#s` → `/join` page → `runer://join?host=…#s`. Universal links (iOS) and App Links (Android) are an optional extra on mobile: they need the server domain to serve `apple-app-site-association` / `assetlinks.json`, and the scheme stays as the fallback. On desktop the scheme registers only in a built, installed app, not in `cargo tauri dev`.
- The link parser keeps the fragment and never sends it anywhere, including logs, crash reports and analytics. A unit test asserts this.
- **Invite links are sent by copying them.** There is no e-mail button and no share sheet on desktop. The link is shown once, when made; the app keeps no copy of its secret, so "re-send" means a new link (the old one can be revoked). The server must never send the link: it would then see the fragment secret, which is what stops it from impersonating the invitee (E2EE §6).
- **Invite links need an account on the link's server.** Welcome has no invite entry and the server never follows a link. A link the app is opened with while signed out is kept, and Join opens after sign-up or sign-in. A link that arrives while signed in opens Join at once. Join refuses a link for another server (`other-server`) before contacting it. Underneath, `openInvite(platform, link)` can read a package without an account, but joining goes through `ClientCore.invites`, which is where the other server is refused.
- **A link that isn't an invite is never a list's name.** Where a list is added, a whole invite link opens Join; any other text that starts like a link (`https://…`, half an invite) does nothing and stays in the field.

### 6.4 Webview baseline

The crypto doesn't depend on the webview (§8), so the minimums are Tauri 2's own defaults (from `tauri-utils` 2.10). Raise one only when the UI needs a newer engine feature.

| Target | Engine | Minimum |
|---|---|---|
| macOS | WKWebView | macOS 10.13 |
| iOS | WKWebView | iOS 15.0 |
| Windows | WebView2 (Evergreen) | Windows 10, current Evergreen |
| Android | System WebView (Chromium) | API 24 (Android 7.0) |
| Linux | WebKitGTK 4.1 | Distros that ship `webkit2gtk-4.1` (checked on 2.52.6) |

---

## 7. UI (`packages/ui`)

- **Stack:** Svelte 5. **No Tiptap/ProseMirror**: the editor is our own, over rows. Markdown is parsed into elements by `ui/markdown.ts`, never into HTML (a note may come from another member, and the webview reaches the vault).
- **Editing a list:** the list is a page edited in place. Every task and every line of text is a block you type into where it stands; nothing opens to be edited. A new task row waits at the end. Enter after a task starts the next task; Enter on an empty task row starts a line of text; in text, Enter starts the next block and Shift+Enter a new line in the same block; Enter on an empty last line of text goes back to a task. `/` opens a menu (task, text, heading 1–3, bullet and numbered list, quote, divider); "# ", "- ", "1. ", "> ", "---" at the start of a line do the same. A line's type badge in the gutter turns it into another type, a task too. Blocks are dragged by a handle in the gutter (shown on hover or focus), or moved with Alt+↑/↓; tasks in Today and My tasks can be dragged too, into an order kept in the Inbox document. Today and Lists keep a capture field.
- **Notes are stored as Markdown.** The editor's blocks are tasks and lines; the lines between two tasks are one note. Rearranging regroups lines into notes (`ui/blocks.ts`): a regrouped note keeps the id of a note it holds lines from, so concurrent edits stay on the same item where they can. Two notes that meet become one. A newline inside a block is stored as a Markdown hard break (`\` at the end of the line). Ordered lists are renumbered from 1 on each save.
- **When an edit is saved.** A block is saved when it is left or split, not on every key, so one edit is one sealed update. Detail fields save on leaving them and when they go away (the screen closing, the date picker closing on a press outside), since WebKit sends no event for a focused field that is removed. The time is a plain text field (`14:30`, `1430`, `2pm`; `dates.ts`, `parseTime`), not `type="time"`, whose events WebKit sends on its own schedule.
- **Typed dates.** A date or a time typed in a new task ("Call mom tomorrow 9:30") is set on the task, the time as its reminder, and the words leave the title ("Call mom"). It is read in Today's capture field and in the new task row of a list, never when an existing title is edited or a line of text is turned into a task. A chip beside the field shows what was understood before Enter; Escape, or a press on the chip, keeps the words as text for that task and sets no date. A date with no title left adds nothing. The parser is `client-core/src/nl/`: an engine that knows no language (`parse.ts`), and one data file per language with its words and the sheet of examples its tests run (`en.ts`). What keeps it from guessing is listed on `parseTask` and shown in Help. It has no clock: the UI passes the device's date and time (`ui/dates.ts`, `readTyped`). The date picker's time field and its "Next week" and "Next weekend" picks use the same code. Repeats ("every monday") are not read: tasks don't repeat (§2).
- **No links in notes.** Following one would navigate the webview away; a URL shows as text.
- **Notes have no detail screen**, and a note can't be moved to another list (tasks can, from their detail).
- **Layout:** responsive from the start, one component tree with breakpoints. Desktop is a sidebar (Today, Inbox, My lists) plus the list. Mobile is a bottom tab bar with stacked navigation.
- **Touch:** targets of at least 44 px, no hover-only affordances.
- **Security is quiet:** a small "Encrypted" marker and the note caption, not a dominant lock.
- **State:** the UI reads `client-core` `Readable`s and holds no protocol state of its own.
- **Language: English** All strings live in one message catalogue from the start, so other languages can follow. The product name, runer, is one constant.

### 7.1 Screens

| Screen | Key requirements |
|---|---|
| Welcome | Create account / Sign in; hosted server by default, a self-hosted URL behind a small link |
| Create account | Show the 12 words once, confirm 3 of them, offer a recovery-sheet download (E2EE §3.1). Creates the Inbox list. |
| Sign in | 12-word field with BIP-39 autocomplete; checksum validated before any network call |
| Today | Start screen. Date, capture field (to Inbox), My tasks across lists with the source list shown per task |
| Inbox | The same page of tasks and text as a list; a task can be moved to a list from its detail |
| Lists | "New list"; sections "Shared" and "Only me"; per list: open-task count and sharing summary |
| List | Title, members ("You and Anna"), then the page of tasks (title, sub-line with due/assignee, "Today" chip, detail) and text (headings, lists, quotes, dividers). Unknown item types shown as text. Offline indicator; never blocks on the network |
| Task detail | Done, title, description, and a row of properties: the date (a date, or a date and a time; the time is the reminder), assignee (members of this list only), list. Reordering is the list's drag handle |
| Share list | Members; "Share with…" known contacts first, then invite by link (copied, §6.3); pending invites (revoke); optional **safety code** check (one "Show safety codes" line at the bottom of the dialog, for everyone in the list at once; nothing on the rows and no prompt to do it, 2026-10-07); remove with the "keeps what they've already seen" wording (E2EE §8.1). Owner only (§3.4) |
| Join | Parse link → show the list name and the inviter's name from the decrypted package → sign in or up → join (§3.4) |
| Settings | Sessions list and revoke; show and change account key; notification permission; the optional name (§3.7); sign out; delete account (`ClientCore.deleteAccount`: leaves the lists of others, then `DELETE /v1/account`, with which the server deletes the account and the lists it owns for everyone in them, then clears the device as sign-out does; the server refuses while the account is a member of someone else's list, E2EE §8.4) |
| Help | What can be typed: the language's examples, read against today's date by the parser itself, and its rules. In the sidebar on desktop; from Settings on a phone |
| Security warnings | Banner plus detail view for every `SecurityWarning` (§3.3) |

### 7.2 Reminders

Everything about due dates and reminders is computed on the device:

- After every change (local edits as well as syncs), at most once per tick, `reminders/` computes the set of upcoming reminders for tasks with `remind_at` that are assigned to the user, or unassigned in their lists, and calls `notifications.replaceAll` with the nearest `maxPending`. The OS is only called when the planned set differs from the last one. Reminders already in the past are dropped, not fired late.
- **Notification text:** the task title, with the list name as body. A privacy setting switches to a generic title (`GENERIC_REMINDER_TITLE`, overridable) with no body, because scheduled notifications sit in plaintext outside the app. The Inbox has no name, so its reminders carry the UI's name for it (`ClientCoreOptions.reminderText`), as does the generic title.
- **The privacy setting is per account and per device**, kept in `LocalStore` table `settings` as plain JSON. It is a display preference and nothing secret, so it is the one non-ciphertext value in `LocalStore`. Sign-out deletes it and unschedules every reminder.
- **iOS cap:** at most 64 pending notifications; schedule the nearest and top up on each sync.

The UI states the limits honestly: a device only knows about a reminder after it has synced.

| Situation | Result |
|---|---|
| Reminder set on this device | Works, also offline |
| Set on another device, this one not synced since | No alert |
| Partner assigns a task with a reminder | Alert only after this device has synced |
| Reminder moved or deleted elsewhere | The old one may still fire on an unsynced device |
| Several devices | Each fires; dismissing on one doesn't clear the others |

**No push.** The server sends no push notifications, so a device learns of changes only when it syncs.

**Rejected:** server-fired reminders. They would need reminder times on the server in plaintext.

---

## 8. The vault and the crypto

**Keys, crypto and the membership logs run in Rust.** The reason is key isolation. JavaScript in the webview is the part most exposed to compromise, for example through a malicious npm dependency. It must not be able to copy keys out, or get key material sent anywhere the verified logs don't allow. Measured: the vectors pass unchanged, and 5000 decrypts take 11 ms in one batched call from the webview on Linux, 5 ms on an iPhone (iOS 27), and ~200 ms on macOS in a dev build. The same decrypts cost 770–1450 ms as one call per item, and 94–180 ms on WebCrypto. Per-item calls are the thing to avoid.

Two crates:

- **`runer-crypto`**: the byte-level crypto of E2EE §3–§7 (account and words, identity bundle, list keys and key chain, envelopes, content, invites, safety code). Stateless; works on bytes. Checked against the shared vectors.
- **`runer-vault`**: everything that holds state or decides. It keeps the Account Key (in the OS keychain), the identity, the list keys per epoch and the verified log of every list. It parses and verifies logs (canonical form, signatures, rules 1–12, fork and rollback checks), and it builds, signs and seals everything the client sends. It must agree with the Go validator on every file in `protocol/log-fixtures`. The interface is `Vault` (§4).

What the vault guarantees, even to a compromised webview:

- **No key leaves it.** JavaScript holds handles (string ids), never key bytes. The one exception is the 12 words, which the user must see: once at sign-up, and under Settings → show account key. Showing them again is not gated behind the OS passcode or biometrics: an accepted gap (E2EE §12).
- **Key material moves only where verified state allows.** A list key is sealed only to members of that list's verified log. An `add` is built only for an identity from a log the vault has verified (E2EE §6.3). An invite package carries the list key only for an invite that is open in the log. The identity is re-sealed only by a change of account key.
- **It signs only what it builds.** Server messages, log entries its own rules accept, and content headers (list, epoch, author, log head) come from typed arguments and its state, never from caller-supplied bytes. No call signs caller-supplied bytes, not even in the Node add-on; the Rust tests build their entries with their own test keys.

So the worst a compromised webview can do is act as the user while the compromise lasts: read what is decrypted, and append entries that every member sees in the log.

**Keep the webview small.** Everything decrypted (the Yjs documents, search, the UI) is plaintext in JavaScript, so any code in the webview can read it. The vault stops keys leaving, not reading. Therefore:

- **Runtime dependencies of `client-core` and `ui` need the owner's approval**, like a crate added to the vault. The approved list is `svelte` and `yjs`. Development tools (test runners, bundler, linters) don't ship and aren't on the list.
- **Lockfile changes are reviewed** like code.
- **No code is loaded at run time**: everything is bundled, and the CSP allows no remote scripts (§6.1).

- **Batch across the boundary.** Logs are applied in one call per list; content is opened in batches. Single calls (one edit, one entry) are fine.
- **One implementation.** The same crates run behind Tauri commands in the app and as a napi-rs add-on in Node. There is no crypto and no log validation in TypeScript.
- Randomness only from the OS (`getrandom`). Secrets are wiped when dropped (`zeroize`).
- Ed25519 verification gives Go's verdicts, including the edge cases (E2EE §10; `protocol/vectors/ed25519-edge.json`).
- **Vectors are generated by this repo** and copied to the server, which checks them in Go. `crates/runer-crypto/tests/generate.rs` builds all six files from a short list of inputs and the crate's own functions and labels, and `VECTORS_WRITE=1 cargo test -p runer-crypto --test generate the_committed` writes them. `crates/runer-crypto/tests/vectors.rs` rebuilds the committed ones from their inputs and requires the same bytes; set `VECTORS_DIR` to check another directory.
- **The shared test data is in `protocol/`**, one copy that both sides test against. `protocol/log-fixtures` is generated by the server's Go code; `protocol/vectors` by this crate (`VECTORS_WRITE=1 cargo test -p runer-crypto --test generate the_committed`). A new or changed vector counts once the Go check passes too (`go test ./internal/cryptovectors/ ./internal/sync/` in `server/`).
- **Sealed vectors.** The Rust generator reproduces the vectors byte for byte, and its test compares every field of the copies here, so the crate is shown to still make the server's files. The Rust `hpke` crate won't take a raw ephemeral key, so the generator seals envelopes with a fixed-output RNG: `hpke` turns the RNG output (SHA-256 of the envelope's name) into the ephemeral key with `DeriveKeyPair`, and the vector records that derived private key, as Go's test expects.
- Known-answer vectors (`protocol/vectors/`) and the log fixtures (`protocol/log-fixtures/`) run in `cargo test`; the log fixtures also run through the Node add-on in vitest. `npm test` runs both.
- Dependencies of `runer-crypto`: `ed25519-dalek`, `x25519-dalek`, `hkdf`, `hmac`, `sha2`, `aes-gcm`, `hpke`, `bip39`, `zeroize`, `getrandom`, `serde_json`. Adding one to either crate is a security decision (E2EE §10). `yjs` is not crypto and is added for sync.

### 8.1 Logs in the vault

- **Same checks, same order as Go.** `validate_entry` runs the server's checks in the server's order, so verdicts and reasons match on every fixture; `tests/log_fixtures.rs` pins the reason for each. Where a field is missing (an `add` or `remove` without `subject`), it gives Go's code (`MissingEnvelope`, `NotMember`), not an early `Malformed`.
- **Decoding follows Go too.** Go's base64url decoder ignores `\r`/`\n` and tolerates non-zero trailing bits, and Go escapes `\b` and `\f` as short forms (Go ≥ 1.22); ours does the same. It can't change a verdict today, since an entry must re-encode to its own bytes and holds no free text, but it keeps the two validators alike.
- **A fork changes nothing.** When `applyLog` meets an entry that differs from the verified one at the same seq, it keeps nothing from the batch and returns a `log-fork` warning with the list's verified state. Entries it already has may be sent again and are skipped.
- **Rollback and fork against the server's head.** `checkHead` compares each list's head from `GET /groups` with the verified log: lower is `log-rollback`, a different hash at a seq we hold is `log-fork`. Catch-up fetches from our own head (not after it), so a different entry there is also caught by `applyLog` as a fork.
- **`applyLog` checks the genesis against the list id** it was asked for (`WrongList`), so a server can't answer with another list's log.
- **The vault stores logs, not list keys.** Each verified entry is kept (sealed under the device cache key) and the logs are re-verified from genesis at startup, which opens our envelopes and key chains again. That keeps one source of truth and re-checks the stored bytes on every start; a list whose stored log no longer verifies is dropped and refetched. The one key that arrives outside the log, a link invite's `K_e` from its package (E2EE §6.1), is stored on its own (`key/<account>/<list>/<epoch>`, sealed the same way) when the vault builds the `join`, loaded after the logs at startup, and deleted with the list. A store without its cache key in the keychain is unreadable and is cleared.

---

## 9. Local data

| Data | Stored as | Location |
|---|---|---|
| Account Key, device cache key | raw bytes | OS keychain, read only by the vault |
| Identity keys, list keys per epoch | AES-GCM under the device cache key | the vault's own store |
| Verified log per list + highest `(seq, hash)` | signed entry bytes (public) | the vault's own store |
| Highest seen `bundle_gen` | integer | the vault's own store |
| List updates and snapshots | server ciphertext + `seq`, unchanged | `storage` |
| Outbox | encrypted frame + `cid` | `storage` |
| Plaintext Yjs state, search index, Today view | — | **memory only** |
| Scheduled reminders | task title and time, **plaintext in the OS notification store** | OS (§7.2) |

- The vault's own store is an SQLite file written from Rust in the app, and memory in Node. TypeScript can't read or change it, so it can't roll back a verified head or swap a key.
- Sign-out deletes the account from the vault (keychain entries and its store), all of its `storage` tables and its scheduled notifications.
- Lists are small and all are subscribed at startup, so the in-memory rebuild is expected to be cheap. Measure it on a phone; persist an encrypted index only if startup suffers (E2EE §3.5).

---

## 10. No browser version

The product is the Tauri app on desktop and mobile only. A browser version would load its code from a server on every visit, so whoever controls that server could serve code that takes the keys; the vault's isolation wouldn't hold in a page. It would also have no keychain and no reliable reminders.

The server's `/join` page is not a client: it only hands an invite link's fragment to the installed app (§6.3).

The design preview (`npm run design -w @runer/ui`, `packages/ui/preview/`) is not one either: it runs the real UI in a browser on sample data, with a stand-in core over the real list document. It is a tool for look-and-feel work and is never shipped.

---

## 11. Testing

| Layer | What | Where |
|---|---|---|
| `client-core` unit | Invite links, outbox, list operations, views, reminder planning | Node (vitest) |
| Vault | Log fixture suite (`protocol/log-fixtures`, identical verdicts to the Go validator), the rules, what it refuses to seal or sign | `cargo test` |
| e2e | `client-core` vs the real server and PostgreSQL: sign up, create list, share with two and remove one, invite with safety code, concurrent edits to different tasks, stale-epoch outbox resend, adversarial proxy, plaintext canary | Node |
| Crypto crate | Vectors, Ed25519 edge cases against Go's verdicts | `cargo test` |
| Platform adapters | One contract suite per `Platform` implementation | Node + Tauri |
| UI | Component tests for capture, list rows, Today | vitest + Svelte testing library |
| Smoke | Sign up → capture a task → restart → still there | By hand in the built app; real phone for reminders |

There is no WebDriver test of the built app. Tauri's driver runs on Linux only, and a suite that can't run on the machines the app is developed on isn't run and goes stale; the screens are tested as components against the in-memory server instead, and what only the built app can show is checked by hand.

The e2e tests drive `client-core` with the real vault through the Node add-on, against the real server and PostgreSQL: `e2e/against-server.sh` starts both and runs the suite, in CI and in `make test`. The unit tests use an in-memory server (`packages/client-core/test/platform-node/memory-server.ts`), which checks little of what the server checks; the e2e suite is what holds the app to the real one.

---

## 12. Build and release

`.github/workflows/app-release.yml` builds the installers from an `app-v*` tag as a draft GitHub release ([Releasing](releasing.md)):

| Target | Output | Code signing |
|---|---|---|
| macOS (universal) | `.dmg`, `.app` | A self-signed certificate, so the signature stays the same between releases (the keychain needs that); Gatekeeper doesn't trust it. Ad hoc without one |
| Windows | `.msi`, NSIS `.exe` | None |
| Linux | AppImage, `.deb`, `.rpm` | None |

Every build carries the updater's signature, which installed copies check before installing an update. The workflow doesn't build iOS or Android.

The updater key is part of the trusted base (E2EE §1). The server ships no code to the app, so the release pipeline is where a supply-chain attacker would go:

- **Builds run in CI from tagged commits.** No build from a laptop is released.
- **The updater key is in CI secrets and in an offline backup.** Losing it strands every installed app.
- **The app contacts only its server and the update feed.** No analytics and no crash reporting.
- **A leaked updater key**: publish a new updater key through another channel and tell users to reinstall by hand. A compromised server is the lesser case: content stays ciphertext.
