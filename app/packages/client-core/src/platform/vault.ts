/**
 * The vault: the Rust side that holds every key and every verified log
 * (client-spec §8), behind Tauri commands in the app and a napi-rs add-on in
 * Node (`crates/runer-vault-node`).
 *
 * What the vault guarantees, even to a compromised webview:
 * - No key leaves it, except the 12 words on the two screens that must show
 *   them (sign-up and "show account key").
 * - It moves key material only where its own verified state allows: list
 *   keys are sealed only to members of the list's verified log, an invite
 *   package carries the list key only for an invite open in that log, and
 *   the identity is re-sealed only by a change of account key.
 * - It signs only what it builds itself from typed arguments and its state:
 *   server messages, log entries its rules would accept, content headers.
 *
 * So the worst a compromised webview can do is act as the user while the
 * compromise lasts: read what's decrypted, and append entries that every
 * member sees in the log.
 *
 * Handles are string ids (the same over Tauri IPC and napi). Every method is
 * async, because over Tauri every call is an IPC round trip; bulk work takes
 * batches. Failures reject with a `VaultError`.
 *
 * All of it is built: accounts, kept across restarts in the keychain and
 * the vault's own store; lists, whose verified logs are kept and
 * re-verified at startup; sharing (E2EE §6, §8); and content.
 */

declare const brand: unique symbol;
/** A signed-in account on one server: its Account Key, identity and verified logs live in the vault. */
export type AccountHandle = string & { readonly [brand]: 'account' };
/** An invite link the user opened, while they decide to join. */
export type InviteHandle = string & { readonly [brand]: 'invite' };

/** Lowercase hex list id (E2EE §4.1). */
export type ListId = string;

export type ServerMessageKind = 'login' | 'rekey' | 'identity';

export interface PublicIdentity {
  signPub: Uint8Array;
  agreePub: Uint8Array;
}

/** A signed log entry, ready to append. */
export interface SignedEntry {
  raw: Uint8Array;
  sig: Uint8Array;
}

/** An account the vault keeps across restarts. */
export interface StoredAccount {
  account: AccountHandle;
  server: string;
  authPub: Uint8Array;
  identity: PublicIdentity;
  bundleGen: number;
  /** `null` until the Inbox has been verified on this device (`setInbox`). */
  inbox: ListId | null;
}

/** A list's id and its signed genesis entry, not yet sent. */
export interface NewList {
  list: ListId;
  entry: SignedEntry;
}

/** What the UI and the views may know about a list's membership, from the verified log. */
export interface ListState {
  id: ListId;
  owner: PublicIdentity;
  /** Everyone in the list: the owner first, then in the order they were admitted. */
  members: PublicIdentity[];
  epoch: number;
  head: { seq: number; hash: Uint8Array };
  openInvites: { invitePub: Uint8Array; expires: number }[];
  /** Members who joined with a link invite, and its key (the owner may have named the link). */
  linkJoins: { member: Uint8Array; invitePub: Uint8Array }[];
  /** The genesis entry's `ts`, Unix milliseconds. */
  created: number;
  /**
   * Someone outside the list may hold its key, so it should `rotate`: after
   * a member left, any member does (E2EE §8.2); after the owner revoked a
   * link, the owner does (E2EE §6.1). `null` once a new epoch began.
   */
  rotationDue: 'member-left' | 'invite-revoked' | null;
}

/** The log-derived subset of client-spec §3.3. */
export type VaultWarning =
  | { kind: 'log-fork' | 'log-rollback'; list: ListId }
  | { kind: 'withheld-removal'; list: ListId }
  | { kind: 'bundle-rollback' };

/** Something that happened in a log, for notices ("Anna joined"). */
export type LogEvent =
  | { kind: 'joined' | 'added' | 'removed' | 'left'; list: ListId; member: Uint8Array }
  | { kind: 'rotated'; list: ListId; epoch: number };

/** A log entry that breaks a rule of E2EE §4.2, named after the Go server's error for it. */
export type LogErrorCode =
  | 'Malformed'
  | 'InvalidSignature'
  | 'HeadMismatch'
  | 'NotOwner'
  | 'NotMember'
  | 'LeaveNotSelf'
  | 'EpochMustIncrease'
  | 'EpochMustHold'
  | 'MissingEnvelope'
  | 'MissingKeyChain'
  | 'OwnerStays'
  | 'TimestampSkew'
  | 'AlreadyMember'
  | 'InviteReused'
  | 'InviteExpiry'
  | 'InviteNotOpen'
  | 'InviteExpired'
  | 'JoinProof';

export type VaultErrorCode =
  | 'UnknownAccount'
  /** No verified log for the list yet: apply one from its genesis entry. */
  | 'UnknownList'
  /** The log's genesis entry doesn't hash to the list id asked for. */
  | 'WrongList'
  | 'InvalidArgument'
  /** The call doesn't fit the account's state, e.g. a commit with nothing pending, or no identity loaded yet. */
  | 'InvalidState'
  /** The Identity Bundle didn't open with this Account Key at that `bundle_gen`. */
  | 'BadBundle'
  /** The keychain or the vault's own store failed; nothing changed. */
  | 'Storage'
  /** `applyLog`: the batch broke a log rule, and nothing from it was kept. */
  | LogErrorCode;

export class VaultError extends Error {
  constructor(
    readonly code: VaultErrorCode,
    message: string,
  ) {
    super(message);
    this.name = 'VaultError';
  }
}

/** Content as it travels in `update` and `snapshot` frames (E2EE §7). */
export interface SealedContent {
  header: Uint8Array;
  nonce: Uint8Array;
  ct: Uint8Array;
  sig: Uint8Array;
}

/**
 * Why the vault rejected one message: `no-key` means it holds no key for the
 * message's epoch, so the list's log may be behind (catch it up, then retry).
 */
export type ContentRejection = 'malformed' | 'wrong-list' | 'not-member' | 'no-key' | 'bad-content';

export type OpenedContent =
  /**
   * `coversSeq` is set on snapshots, from the signed header. `headKnown`: the log head the author cites is an entry
   * of the verified log; if not, our log is behind theirs, or the server shows us different histories (E2EE §4.4).
   */
  | { body: Uint8Array; epoch: number; author: Uint8Array; coversSeq: number | null; headKnown: boolean }
  | { rejected: ContentRejection };

export interface Vault {
  // --- Accounts (E2EE §3). The vault keeps the Account Key in the OS keychain itself.

  /** Sign-up, step 1: fresh keys, the bundle sealed at the server's first `bundle_gen` (1), and the Inbox's genesis. Pending until `confirmSignUp`. */
  startSignUp(server: string): Promise<{ account: AccountHandle; authPub: Uint8Array; identity: PublicIdentity; bundle: Uint8Array; inbox: NewList }>;
  /** Proof-of-work for sign-up: `pow_nonce` with SHA-256(auth_pub ‖ nonce ‖ pow_nonce) having `bits` leading zero bits. */
  solveSignUpPow(account: AccountHandle, nonce: Uint8Array, bits: number): Promise<Uint8Array>;
  /** Sign-up, step 2, after the server accepted: stores the Account Key and returns the words to show once. */
  confirmSignUp(account: AccountHandle): Promise<{ words: string[] }>;
  /** Sign-in on a new device; `null` for a typo (wrong count, unknown word, bad checksum). */
  signIn(server: string, words: string): Promise<{ account: AccountHandle; authPub: Uint8Array } | null>;
  /** Startup: the accounts kept on this device (saved at a confirmed sign-up, or a sign-in once the identity loaded). */
  storedAccounts(): Promise<StoredAccount[]>;
  /** Forgets the account on this device: keychain, the vault's store, memory. */
  signOut(account: AccountHandle): Promise<void>;
  /** Settings → show account key. The one path that hands key material to the UI. */
  showWords(account: AccountHandle): Promise<string[]>;
  /** The account's settings as the server keeps them (`settings_sealed`): only our identity opens them. Works during sign-up. `json` is ours; the vault doesn't read it. */
  sealAccountSettings(account: AccountHandle, json: string): Promise<Uint8Array>;
  /** Rejects for a blob that isn't ours or was changed. */
  openAccountSettings(account: AccountHandle, sealed: Uint8Array): Promise<string>;
  /**
   * Signs a server message over a 32-byte server nonce (E2EE §3.4). `identity`
   * signs with an identity waiting to be registered (sign-up, or a reset not
   * yet committed) and is refused otherwise; the rest sign with the auth key
   * (the old one while a change of account key is pending).
   */
  signServerMessage(account: AccountHandle, kind: ServerMessageKind, nonce: Uint8Array): Promise<Uint8Array>;
  /** Opens the Identity Bundle the server returned. A lower `bundle_gen` than seen is ignored, with a `bundle-rollback` warning. */
  loadIdentity(account: AccountHandle, bundleGen: number, bundle: Uint8Array): Promise<{ identity: PublicIdentity; warnings: VaultWarning[] }>;
  /** E2EE §9.2: a new Account Key; the same identity sealed under it at the next `bundle_gen`. Commit after the server accepts. */
  changeAccountKey(account: AccountHandle): Promise<{ authPub: Uint8Array; bundle: Uint8Array; commit(): Promise<{ words: string[] }> }>;

  // --- Lists and logs (E2EE §4, §5, §8)

  /** A new list owned by our identity: its genesis entry, with `K_0` sealed to us (E2EE §5). Applied here at once; the server gets it when it can be reached. */
  createList(account: AccountHandle): Promise<NewList>;
  /**
   * Verifies `entries` on top of the list's verified log (parsing, canonical
   * form, signatures, rules 1–12, and that the genesis is `list`), stores
   * them, opens any envelope addressed to us and follows the key chain back.
   * Entries already verified may be sent again; one that differs at a
   * verified seq is a fork, returned as a `log-fork` warning with nothing
   * applied. The batch is all-or-nothing.
   */
  applyLog(account: AccountHandle, list: ListId, entries: SignedEntry[]): Promise<{ state: ListState; events: LogEvent[]; warnings: VaultWarning[] }>;
  /** The head the server reports (`GET /groups`) against the verified log: `log-rollback` if lower, `log-fork` if the hash differs at a seq we have. */
  checkHead(account: AccountHandle, list: ListId, seq: number, hash: Uint8Array): Promise<VaultWarning[]>;
  listState(account: AccountHandle, list: ListId): Promise<ListState>;
  /** Sign-in: accepts `list` as the account's Inbox (as the server names it) only if its verified log shows a list we own alone. */
  setInbox(account: AccountHandle, list: ListId): Promise<void>;
  /** Drops a list we left, were removed from, or that was deleted. */
  forgetList(account: AccountHandle, list: ListId): Promise<void>;
  /** Owner: add someone from a verified log of any of our lists (E2EE §6.3). */
  addMember(account: AccountHandle, list: ListId, signPub: Uint8Array): Promise<SignedEntry>;
  /** Owner: remove, with a new epoch sealed to everyone else (E2EE §8.1). */
  removeMember(account: AccountHandle, list: ListId, signPub: Uint8Array): Promise<SignedEntry>;
  /** Any member: a new epoch sealed to every member (after a leave, an uninvite, an unused expiry). Apply it once the server accepts it. */
  rotate(account: AccountHandle, list: ListId): Promise<SignedEntry>;
  leave(account: AccountHandle, list: ListId): Promise<SignedEntry>;
  /** Everyone we share, or shared, a verified log with: the "Share with…" candidates. */
  knownContacts(account: AccountHandle): Promise<PublicIdentity[]>;
  safetyCode(id: PublicIdentity): Promise<string[]>;

  // --- Link invites (E2EE §6.1). The link secret is in the URL, so the UI has it by nature.

  /**
   * Owner: an `invite` entry and the sealed package (with the current list key) for it.
   * `name` is the list's and `ownerName` the owner's own, for the Join screen; an empty one is left out.
   */
  createInvite(account: AccountHandle, list: ListId, expires: number, name: string, ownerName: string): Promise<{ entry: SignedEntry; secret: Uint8Array; id: Uint8Array; invitePub: Uint8Array; sealedPackage: Uint8Array }>;
  uninvite(account: AccountHandle, list: ListId, invitePub: Uint8Array): Promise<SignedEntry>;
  /** Joiner, step 1: the invite id to fetch the package with. */
  openInviteLink(secret: Uint8Array): Promise<{ invite: InviteHandle; id: Uint8Array }>;
  /** Joiner, step 2: what the package says, for the Join screen. The list key stays in the vault. */
  readInvitePackage(invite: InviteHandle, sealed: Uint8Array): Promise<{ list: ListId; name: string; ownerName: string; owner: PublicIdentity }>;
  /** Joiner, step 3, after `applyLog` verified the list's log up to the package's head: the `join` entry. */
  join(account: AccountHandle, invite: InviteHandle): Promise<SignedEntry>;

  // --- Content (E2EE §7)

  /** Seals under the current epoch; the vault writes the header (list, epoch, author, log head). */
  sealContent(account: AccountHandle, list: ListId, kind: 'update' | { snapshot: { coversSeq: number } }, body: Uint8Array): Promise<SealedContent>;
  /**
   * Batch: per message, checks the header names `list` and its author was a
   * member during its epoch in the verified log, verifies, decrypts. Each
   * message gets its body and header fields, or why it was rejected.
   */
  openContents(account: AccountHandle, list: ListId, messages: SealedContent[]): Promise<OpenedContent[]>;
}
