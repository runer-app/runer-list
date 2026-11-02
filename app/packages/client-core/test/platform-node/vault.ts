import { join } from 'node:path';
import {
  VaultError,
  type AccountHandle,
  type InviteHandle,
  type ListId,
  type ListState,
  type LogEvent,
  type NewList,
  type OpenedContent,
  type ContentRejection,
  type SealedContent,
  type PublicIdentity,
  type ServerMessageKind,
  type SignedEntry,
  type Vault,
  type VaultErrorCode,
  type VaultWarning,
} from '../../src/platform/vault.js';

/** The add-on's own shapes (`crates/runer-vault-node/src/lib.rs`). */
interface NativeListState {
  id: string;
  owner: PublicIdentity;
  members: PublicIdentity[];
  epoch: number;
  head: { seq: number; hash: Uint8Array };
  openInvites: { invitePub: Uint8Array; expires: number }[];
  linkJoins: { member: Uint8Array; invitePub: Uint8Array }[];
  created: number;
  rotationDue?: 'member-left' | 'invite-revoked' | null;
}

interface NativeNewList {
  list: string;
  entry: SignedEntry;
}

interface NativeStoredAccount {
  account: string;
  server: string;
  authPub: Uint8Array;
  identity: PublicIdentity;
  bundleGen: number;
  inbox?: string | null;
}

interface NativeVault {
  accounts(): NativeStoredAccount[];
  setInbox(account: string, list: string): void;
  signIn(server: string, words: string): { account: string; authPub: Uint8Array } | null;
  startSignUp(server: string): { account: string; authPub: Uint8Array; identity: PublicIdentity; bundle: Uint8Array; inbox: NativeNewList };
  solveSignUpPow(account: string, nonce: Uint8Array, bits: number): Uint8Array;
  confirmSignUp(account: string): string[];
  signOut(account: string): void;
  showWords(account: string): string[];
  sealAccountSettings(account: string, json: string): Uint8Array;
  openAccountSettings(account: string, sealed: Uint8Array): string;
  signServerMessage(account: string, kind: string, nonce: Uint8Array): Uint8Array;
  loadIdentity(account: string, bundleGen: number, bundle: Uint8Array): { identity: PublicIdentity; warnings: string[] };
  changeAccountKey(account: string): { authPub: Uint8Array; bundle: Uint8Array };
  commitAccountKey(account: string): string[];
  applyLog(
    account: string,
    list: string,
    entries: SignedEntry[],
  ): {
    state: NativeListState;
    events: { kind: string; member?: Uint8Array; epoch?: number }[];
    warnings: string[];
  };
  listState(account: string, list: string): NativeListState;
  checkHead(account: string, list: string, seq: number, hash: Uint8Array): string[];
  forgetList(account: string, list: string): void;
  createList(account: string): NativeNewList;
  rotate(account: string, list: string): SignedEntry;
  addMember(account: string, list: string, signPub: Uint8Array): SignedEntry;
  removeMember(account: string, list: string, signPub: Uint8Array): SignedEntry;
  leave(account: string, list: string): SignedEntry;
  knownContacts(account: string): PublicIdentity[];
  safetyCode(signPub: Uint8Array, agreePub: Uint8Array): string[];
  createInvite(
    account: string,
    list: string,
    expires: number,
    name: string,
    ownerName: string,
  ): { entry: SignedEntry; secret: Uint8Array; id: Uint8Array; invitePub: Uint8Array; sealedPackage: Uint8Array };
  uninvite(account: string, list: string, invitePub: Uint8Array): SignedEntry;
  openInviteLink(secret: Uint8Array): { invite: string; id: Uint8Array };
  readInvitePackage(invite: string, sealed: Uint8Array): { list: string; name: string; ownerName: string; owner: PublicIdentity };
  join(account: string, invite: string): SignedEntry;
  sealContent(account: string, list: string, coversSeq: number | undefined, body: Uint8Array): SealedContent;
  openContents(
    account: string,
    list: string,
    messages: SealedContent[],
  ): { body?: Uint8Array; epoch?: number; author?: Uint8Array; coversSeq?: number | null; headKnown?: boolean | null; rejected?: string | null }[];
}

const LIBRARY: Partial<Record<NodeJS.Platform, string>> = {
  linux: 'libruner_vault_node.so',
  darwin: 'libruner_vault_node.dylib',
  win32: 'runer_vault_node.dll',
};

/** Built by `npm test` (`cargo build -p runer-vault-node`) before vitest runs. */
/** A device's keychain and vault store, in memory (the add-on's `MemoryDevice`). */
export type MemoryDevice = object & { readonly __memoryDevice: unique symbol };

interface Addon {
  Vault: new (device: MemoryDevice) => NativeVault;
  MemoryDevice: new () => MemoryDevice;
}

function loadAddon(): Addon {
  const file = LIBRARY[process.platform];
  if (file === undefined) throw new Error(`no vault add-on for ${process.platform}`);
  const module = { exports: {} as Addon };
  process.dlopen(module, join(import.meta.dirname, '../../../../target/debug', file));
  return module.exports;
}

const addon = loadAddon();

/** Rethrows the add-on's errors as `VaultError`s: napi puts the vault's code in `code`. */
function call<T>(fn: () => T): T {
  try {
    return fn();
  } catch (e) {
    const { code, message } = e as { code?: string; message: string };
    if (code === undefined) throw e;
    throw new VaultError(code as VaultErrorCode, message);
  }
}

function listState(s: NativeListState): ListState {
  return { ...s, id: s.id as ListId, rotationDue: s.rotationDue ?? null };
}

function logEvent(list: ListId, e: { kind: string; member?: Uint8Array; epoch?: number }): LogEvent {
  if (e.kind === 'rotated') return { kind: 'rotated', list, epoch: e.epoch! };
  return { kind: e.kind as 'added' | 'joined' | 'removed' | 'left', list, member: e.member! };
}

const newList = (l: NativeNewList): NewList => ({ list: l.list as ListId, entry: l.entry });

const logWarning = (list: ListId, kind: string): VaultWarning => ({ kind: kind as 'log-fork' | 'log-rollback', list });

/** A fresh device's storage, for `new NodeVault(device)`. */
export const newMemoryDevice = (): MemoryDevice => new addon.MemoryDevice();

/**
 * The real vault through the Node add-on, on a device's in-memory keychain
 * and store. A second `NodeVault` on the same device is that device after a restart.
 */
export class NodeVault implements Vault {
  private readonly native: NativeVault;

  constructor(readonly device: MemoryDevice = newMemoryDevice()) {
    this.native = call(() => new addon.Vault(device));
  }

  async storedAccounts() {
    return call(() => this.native.accounts()).map((a) => ({
      ...a,
      account: a.account as AccountHandle,
      inbox: (a.inbox ?? null) as ListId | null,
    }));
  }

  async setInbox(account: AccountHandle, list: ListId) {
    call(() => this.native.setInbox(account, list));
  }

  async signIn(server: string, words: string) {
    const r = call(() => this.native.signIn(server, words));
    return r === null ? null : { account: r.account as AccountHandle, authPub: r.authPub };
  }

  async startSignUp(server: string) {
    const r = call(() => this.native.startSignUp(server));
    return { ...r, account: r.account as AccountHandle, inbox: newList(r.inbox) };
  }

  async solveSignUpPow(account: AccountHandle, nonce: Uint8Array, bits: number) {
    return call(() => this.native.solveSignUpPow(account, nonce, bits));
  }

  async confirmSignUp(account: AccountHandle) {
    return { words: call(() => this.native.confirmSignUp(account)) };
  }

  async signOut(account: AccountHandle) {
    call(() => this.native.signOut(account));
  }

  async showWords(account: AccountHandle) {
    return call(() => this.native.showWords(account));
  }

  async sealAccountSettings(account: AccountHandle, json: string) {
    return call(() => this.native.sealAccountSettings(account, json));
  }

  async openAccountSettings(account: AccountHandle, sealed: Uint8Array) {
    return call(() => this.native.openAccountSettings(account, sealed));
  }

  async signServerMessage(account: AccountHandle, kind: ServerMessageKind, nonce: Uint8Array) {
    return call(() => this.native.signServerMessage(account, kind, nonce));
  }

  async loadIdentity(account: AccountHandle, bundleGen: number, bundle: Uint8Array) {
    const r = call(() => this.native.loadIdentity(account, bundleGen, bundle));
    return { identity: r.identity, warnings: r.warnings.map((): VaultWarning => ({ kind: 'bundle-rollback' })) };
  }

  async changeAccountKey(account: AccountHandle) {
    const r = call(() => this.native.changeAccountKey(account));
    return { ...r, commit: async () => ({ words: call(() => this.native.commitAccountKey(account)) }) };
  }

  async applyLog(account: AccountHandle, list: ListId, entries: SignedEntry[]) {
    const r = call(() => this.native.applyLog(account, list, entries));
    return {
      state: listState(r.state),
      events: r.events.map((e) => logEvent(list, e)),
      warnings: r.warnings.map((kind) => logWarning(list, kind)),
    };
  }

  async checkHead(account: AccountHandle, list: ListId, seq: number, hash: Uint8Array) {
    return call(() => this.native.checkHead(account, list, seq, hash)).map((kind) => logWarning(list, kind));
  }

  async listState(account: AccountHandle, list: ListId) {
    return listState(call(() => this.native.listState(account, list)));
  }

  async forgetList(account: AccountHandle, list: ListId) {
    call(() => this.native.forgetList(account, list));
  }

  async createList(account: AccountHandle) {
    return newList(call(() => this.native.createList(account)));
  }

  async rotate(account: AccountHandle, list: ListId) {
    return call(() => this.native.rotate(account, list));
  }

  async sealContent(account: AccountHandle, list: ListId, kind: 'update' | { snapshot: { coversSeq: number } }, body: Uint8Array) {
    const coversSeq = kind === 'update' ? undefined : kind.snapshot.coversSeq;
    return call(() => this.native.sealContent(account, list, coversSeq, body));
  }

  async openContents(account: AccountHandle, list: ListId, messages: SealedContent[]): Promise<OpenedContent[]> {
    return call(() => this.native.openContents(account, list, messages)).map((o) =>
      o.rejected != null
        ? { rejected: o.rejected as ContentRejection }
        : { body: o.body!, epoch: o.epoch!, author: o.author!, coversSeq: o.coversSeq ?? null, headKnown: o.headKnown! },
    );
  }

  async addMember(account: AccountHandle, list: ListId, signPub: Uint8Array) {
    return call(() => this.native.addMember(account, list, signPub));
  }

  async removeMember(account: AccountHandle, list: ListId, signPub: Uint8Array) {
    return call(() => this.native.removeMember(account, list, signPub));
  }

  async leave(account: AccountHandle, list: ListId) {
    return call(() => this.native.leave(account, list));
  }

  async knownContacts(account: AccountHandle) {
    return call(() => this.native.knownContacts(account));
  }

  async safetyCode(id: PublicIdentity) {
    return call(() => this.native.safetyCode(id.signPub, id.agreePub));
  }

  async createInvite(account: AccountHandle, list: ListId, expires: number, name: string, ownerName: string) {
    return call(() => this.native.createInvite(account, list, expires, name, ownerName));
  }

  async uninvite(account: AccountHandle, list: ListId, invitePub: Uint8Array) {
    return call(() => this.native.uninvite(account, list, invitePub));
  }

  async openInviteLink(secret: Uint8Array) {
    const r = call(() => this.native.openInviteLink(secret));
    return { invite: r.invite as InviteHandle, id: r.id };
  }

  async readInvitePackage(invite: InviteHandle, sealed: Uint8Array) {
    const r = call(() => this.native.readInvitePackage(invite, sealed));
    return { ...r, list: r.list as ListId };
  }

  async join(account: AccountHandle, invite: InviteHandle) {
    return call(() => this.native.join(account, invite));
  }
}
