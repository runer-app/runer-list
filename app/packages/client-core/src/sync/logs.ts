/**
 * Lists and their membership logs over HTTP (E2EE §4): create, fetch and
 * verify from genesis, catch up, fork and rollback checks, delete. The vault
 * verifies and keeps every log; this only moves entries between it and the
 * server.
 */

import type { Account } from '../account/account.js';
import { fromBase64url, toBase64url } from '../base64url.js';
import { VaultError, type ListId, type ListState, type SignedEntry } from '../platform/vault.js';
import type { WireEntry } from './store.js';

/** A list as `GET /v1/groups` reports it. */
interface GroupView {
  id: string;
  head_seq: number;
  head_hash: string;
}

interface LogEntryView {
  seq: number;
  entry: string;
  sig: string;
}

/** The server's page size for `GET …/log`; a full page means there may be more. */
const LOG_PAGE = 500;

const wire = (e: SignedEntry): WireEntry => ({ entry: toBase64url(e.raw), sig: toBase64url(e.sig) });

/**
 * Makes a list owned by this account, its first key sealed to us in the
 * genesis (E2EE §5). Nothing here needs the network: the list is verified
 * and usable on this device at once, and `announceList` tells the server.
 */
export async function newList(account: Account): Promise<{ state: ListState; genesis: WireEntry }> {
  const { list, entry } = await account.platform.vault.createList(account.handle);
  const { state } = await account.platform.vault.applyLog(account.handle, list, [entry]);
  return { state, genesis: wire(entry) };
}

/** Makes a list and tells the server straight away. */
export async function createList(account: Account): Promise<ListState> {
  const { state, genesis } = await newList(account);
  await announceList(account, genesis);
  return state;
}

/** Tells the server about a list made here. `409` means it has it already: an earlier try got through. */
export async function announceList(account: Account, genesis: WireEntry): Promise<void> {
  await account.request('POST', '/v1/groups', genesis).catch((e: unknown) => {
    if ((e as { status?: number }).status !== 409) throw e;
  });
}

/**
 * Brings every list the server lists for us up to date, verified from
 * genesis for lists new to this device. Returns their verified states.
 * Lists the server stops listing are dropped by sync's `group` handling.
 */
export async function refreshLists(account: Account): Promise<ListState[]> {
  const groups = await account.request<GroupView[]>('GET', '/v1/groups');
  const states: ListState[] = [];
  for (const g of groups) states.push(await refreshList(account, g));
  return states;
}

async function refreshList(account: Account, g: GroupView): Promise<ListState> {
  const { vault } = account.platform;
  const ours = await verified(account, g.id);
  if (ours !== null) {
    const warnings = await vault.checkHead(account.handle, g.id, g.head_seq, fromBase64url(g.head_hash));
    account.warn(warnings);
    if (warnings.length > 0 || g.head_seq === ours.head.seq) return ours;
  }
  return catchUp(account, g.id);
}

/**
 * Fetches the log from our head (or from genesis) and applies it in one
 * batch. Starting at our own head, not after it, means a server serving a
 * different entry there shows up as a fork. `path` is where the log is
 * served: the list's own, or an invite's while we aren't a member yet.
 */
export async function catchUp(account: Account, list: ListId, path = `/v1/groups/${list}/log`): Promise<ListState> {
  const { vault } = account.platform;
  const ours = await verified(account, list);
  const entries: SignedEntry[] = [];
  for (let from = ours?.head.seq ?? 0; ; ) {
    const page = await account.request<LogEntryView[]>('GET', `${path}?from=${from}`);
    entries.push(...page.map((e) => ({ raw: fromBase64url(e.entry), sig: fromBase64url(e.sig) })));
    if (page.length < LOG_PAGE) break;
    from = page.at(-1)!.seq + 1;
  }
  const r = await vault.applyLog(account.handle, list, entries);
  account.warn(r.warnings);
  // A log new to this device replays its whole history; only news is worth a notice.
  if (ours !== null) account.notice(r.events);
  return r.state;
}

/** How often `append` rebuilds an entry after the head moved before giving up. */
const APPEND_ATTEMPTS = 4;

/**
 * Appends an entry the vault builds from the verified log, then applies
 * it. On `409` (the head moved: another device or member got there first)
 * it catches up and builds again; `build` may then return `null` when the
 * entry is no longer needed. Returns the list's state, or `null` if skipped.
 */
export async function append(
  account: Account,
  list: ListId,
  build: () => Promise<SignedEntry | null>,
  opts: { path?: string; catchUp?: () => Promise<unknown> } = {},
): Promise<ListState | null> {
  const path = opts.path ?? `/v1/groups/${list}/log`;
  for (let attempt = 1; ; attempt++) {
    const entry = await build();
    if (entry === null) return null;
    try {
      await account.request('POST', path, wire(entry));
    } catch (e) {
      if (attempt >= APPEND_ATTEMPTS || (e as { status?: number }).status !== 409) throw e;
      await (opts.catchUp ?? (() => catchUp(account, list)))();
      continue;
    }
    const r = await account.platform.vault.applyLog(account.handle, list, [entry]);
    account.warn(r.warnings);
    return r.state;
  }
}

/** Owner only: deletes the list for everyone, then forgets it here. The server refuses the Inbox. */
export async function deleteList(account: Account, list: ListId): Promise<void> {
  await account.request('DELETE', `/v1/groups/${list}`);
  await account.platform.vault.forgetList(account.handle, list);
}

/** The list's verified state, or `null` if this device has no log for it yet. */
function verified(account: Account, list: ListId): Promise<ListState | null> {
  return account.platform.vault.listState(account.handle, list).catch((e: unknown) => {
    if (e instanceof VaultError && e.code === 'UnknownList') return null;
    throw e;
  });
}
