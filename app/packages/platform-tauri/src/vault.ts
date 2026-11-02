/**
 * The vault behind the `vault_*` commands (`src-tauri/src/vault.rs`), the
 * same mapping as the Node add-on's adapter in client-core's tests. Bytes
 * cross as base64url; the vault's errors come back as `VaultError`s.
 */

import {
  fromBase64url as b,
  toBase64url as s,
  VaultError,
  type AccountHandle,
  type InviteHandle,
  type ContentRejection,
  type ListId,
  type ListState,
  type LogEvent,
  type NewList,
  type OpenedContent,
  type PublicIdentity,
  type SealedContent,
  type ServerMessageKind,
  type SignedEntry,
  type StoredAccount,
  type Vault,
  type VaultErrorCode,
  type VaultWarning,
} from '@runer/client-core';
import { isIpcError, type Ipc } from './ipc.js';

type B64 = string;
interface WireIdentity {
  signPub: B64;
  agreePub: B64;
}
interface WireEntry {
  raw: B64;
  sig: B64;
}
interface WireNewList {
  list: string;
  entry: WireEntry;
}
interface WireListState {
  id: string;
  owner: WireIdentity;
  members: WireIdentity[];
  epoch: number;
  head: { seq: number; hash: B64 };
  openInvites: { invitePub: B64; expires: number }[];
  linkJoins: { member: B64; invitePub: B64 }[];
  created: number;
  rotationDue: 'member-left' | 'invite-revoked' | null;
}
interface WireSealed {
  header: B64;
  nonce: B64;
  ct: B64;
  sig: B64;
}

const identity = (i: WireIdentity): PublicIdentity => ({ signPub: b(i.signPub), agreePub: b(i.agreePub) });
const entry = (e: WireEntry): SignedEntry => ({ raw: b(e.raw), sig: b(e.sig) });
const wireEntry = (e: SignedEntry): WireEntry => ({ raw: s(e.raw), sig: s(e.sig) });
const newList = (l: WireNewList): NewList => ({ list: l.list as ListId, entry: entry(l.entry) });
const sealed = (m: WireSealed): SealedContent => ({ header: b(m.header), nonce: b(m.nonce), ct: b(m.ct), sig: b(m.sig) });
const wireSealed = (m: SealedContent): WireSealed => ({ header: s(m.header), nonce: s(m.nonce), ct: s(m.ct), sig: s(m.sig) });
const logWarning = (list: ListId, kind: string): VaultWarning => ({ kind: kind as 'log-fork' | 'log-rollback', list });

function listState(w: WireListState): ListState {
  return {
    id: w.id as ListId,
    owner: identity(w.owner),
    members: w.members.map(identity),
    epoch: w.epoch,
    head: { seq: w.head.seq, hash: b(w.head.hash) },
    openInvites: w.openInvites.map((i) => ({ invitePub: b(i.invitePub), expires: i.expires })),
    linkJoins: w.linkJoins.map((j) => ({ member: b(j.member), invitePub: b(j.invitePub) })),
    created: w.created,
    rotationDue: w.rotationDue,
  };
}

function logEvent(list: ListId, e: { kind: string; member?: B64; epoch?: number }): LogEvent {
  if (e.kind === 'rotated') return { kind: 'rotated', list, epoch: e.epoch! };
  return { kind: e.kind as 'added' | 'joined' | 'removed' | 'left', list, member: b(e.member!) };
}

export class TauriVault implements Vault {
  constructor(private readonly ipc: Ipc) {}

  /** Rethrows the vault's `{ code, message }` as a `VaultError`. */
  private async call<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
    try {
      return await this.ipc.invoke<T>(`vault_${cmd}`, args);
    } catch (e) {
      if (isIpcError(e)) throw new VaultError(e.code as VaultErrorCode, e.message);
      throw e;
    }
  }

  async storedAccounts(): Promise<StoredAccount[]> {
    const r = await this.call<{ account: string; server: string; authPub: B64; identity: WireIdentity; bundleGen: number; inbox: string | null }[]>('accounts');
    return r.map((a) => ({
      account: a.account as AccountHandle,
      server: a.server,
      authPub: b(a.authPub),
      identity: identity(a.identity),
      bundleGen: a.bundleGen,
      inbox: a.inbox as ListId | null,
    }));
  }

  async setInbox(account: AccountHandle, list: ListId) {
    await this.call('set_inbox', { account, list });
  }

  async signIn(server: string, words: string) {
    const r = await this.call<{ account: string; authPub: B64 } | null>('sign_in', { server, words });
    return r === null ? null : { account: r.account as AccountHandle, authPub: b(r.authPub) };
  }

  async startSignUp(server: string) {
    const r = await this.call<{ account: string; authPub: B64; identity: WireIdentity; bundle: B64; inbox: WireNewList }>('start_sign_up', { server });
    return { account: r.account as AccountHandle, authPub: b(r.authPub), identity: identity(r.identity), bundle: b(r.bundle), inbox: newList(r.inbox) };
  }

  async solveSignUpPow(account: AccountHandle, nonce: Uint8Array, bits: number) {
    return b(await this.call<B64>('solve_sign_up_pow', { account, nonce: s(nonce), bits }));
  }

  async confirmSignUp(account: AccountHandle) {
    return { words: await this.call<string[]>('confirm_sign_up', { account }) };
  }

  async signOut(account: AccountHandle) {
    await this.call('sign_out', { account });
  }

  showWords(account: AccountHandle) {
    return this.call<string[]>('show_words', { account });
  }

  async sealAccountSettings(account: AccountHandle, json: string) {
    return b(await this.call<B64>('seal_account_settings', { account, json }));
  }

  openAccountSettings(account: AccountHandle, sealed: Uint8Array) {
    return this.call<string>('open_account_settings', { account, sealed: s(sealed) });
  }

  async signServerMessage(account: AccountHandle, kind: ServerMessageKind, nonce: Uint8Array) {
    return b(await this.call<B64>('sign_server_message', { account, kind, nonce: s(nonce) }));
  }

  async loadIdentity(account: AccountHandle, bundleGen: number, bundle: Uint8Array) {
    const r = await this.call<{ identity: WireIdentity; warnings: string[] }>('load_identity', { account, bundleGen, bundle: s(bundle) });
    return { identity: identity(r.identity), warnings: r.warnings.map((): VaultWarning => ({ kind: 'bundle-rollback' })) };
  }

  async changeAccountKey(account: AccountHandle) {
    const r = await this.call<{ authPub: B64; bundle: B64 }>('change_account_key', { account });
    return { authPub: b(r.authPub), bundle: b(r.bundle), commit: async () => ({ words: await this.call<string[]>('commit_account_key', { account }) }) };
  }

  async applyLog(account: AccountHandle, list: ListId, entries: SignedEntry[]) {
    const r = await this.call<{ state: WireListState; events: { kind: string; member?: B64; epoch?: number }[]; warnings: string[] }>('apply_log', {
      account,
      list,
      entries: entries.map(wireEntry),
    });
    return { state: listState(r.state), events: r.events.map((e) => logEvent(list, e)), warnings: r.warnings.map((k) => logWarning(list, k)) };
  }

  async checkHead(account: AccountHandle, list: ListId, seq: number, hash: Uint8Array) {
    return (await this.call<string[]>('check_head', { account, list, seq, hash: s(hash) })).map((k) => logWarning(list, k));
  }

  async listState(account: AccountHandle, list: ListId) {
    return listState(await this.call<WireListState>('list_state', { account, list }));
  }

  async forgetList(account: AccountHandle, list: ListId) {
    await this.call('forget_list', { account, list });
  }

  async createList(account: AccountHandle) {
    return newList(await this.call<WireNewList>('create_list', { account }));
  }

  async rotate(account: AccountHandle, list: ListId) {
    return entry(await this.call<WireEntry>('rotate', { account, list }));
  }

  async sealContent(account: AccountHandle, list: ListId, kind: 'update' | { snapshot: { coversSeq: number } }, body: Uint8Array) {
    const coversSeq = kind === 'update' ? null : kind.snapshot.coversSeq;
    return sealed(await this.call<WireSealed>('seal_content', { account, list, coversSeq, body: s(body) }));
  }

  async openContents(account: AccountHandle, list: ListId, messages: SealedContent[]): Promise<OpenedContent[]> {
    const r = await this.call<({ body: B64; epoch: number; author: B64; coversSeq: number | null; headKnown: boolean } | { rejected: string })[]>('open_contents', {
      account,
      list,
      messages: messages.map(wireSealed),
    });
    return r.map((o) =>
      'rejected' in o ? { rejected: o.rejected as ContentRejection } : { body: b(o.body), epoch: o.epoch, author: b(o.author), coversSeq: o.coversSeq, headKnown: o.headKnown },
    );
  }

  async addMember(account: AccountHandle, list: ListId, signPub: Uint8Array) {
    return entry(await this.call<WireEntry>('add_member', { account, list, signPub: s(signPub) }));
  }

  async removeMember(account: AccountHandle, list: ListId, signPub: Uint8Array) {
    return entry(await this.call<WireEntry>('remove_member', { account, list, signPub: s(signPub) }));
  }

  async leave(account: AccountHandle, list: ListId) {
    return entry(await this.call<WireEntry>('leave', { account, list }));
  }

  async knownContacts(account: AccountHandle) {
    return (await this.call<WireIdentity[]>('known_contacts', { account })).map(identity);
  }

  safetyCode(id: PublicIdentity) {
    return this.call<string[]>('safety_code', { signPub: s(id.signPub), agreePub: s(id.agreePub) });
  }

  async createInvite(account: AccountHandle, list: ListId, expires: number, name: string, ownerName: string) {
    const r = await this.call<{ entry: WireEntry; secret: B64; id: B64; invitePub: B64; sealedPackage: B64 }>('create_invite', { account, list, expires, name, ownerName });
    return { entry: entry(r.entry), secret: b(r.secret), id: b(r.id), invitePub: b(r.invitePub), sealedPackage: b(r.sealedPackage) };
  }

  async uninvite(account: AccountHandle, list: ListId, invitePub: Uint8Array) {
    return entry(await this.call<WireEntry>('uninvite', { account, list, invitePub: s(invitePub) }));
  }

  async openInviteLink(secret: Uint8Array) {
    const r = await this.call<{ invite: string; id: B64 }>('open_invite_link', { secret: s(secret) });
    return { invite: r.invite as InviteHandle, id: b(r.id) };
  }

  async readInvitePackage(invite: InviteHandle, sealed: Uint8Array) {
    const r = await this.call<{ list: string; name: string; ownerName: string; owner: WireIdentity }>('read_invite_package', { invite, sealed: s(sealed) });
    return { list: r.list as ListId, name: r.name, ownerName: r.ownerName, owner: identity(r.owner) };
  }

  async join(account: AccountHandle, invite: InviteHandle) {
    return entry(await this.call<WireEntry>('join', { account, invite }));
  }
}
