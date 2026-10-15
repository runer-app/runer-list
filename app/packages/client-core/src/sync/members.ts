/**
 * Changing who is in a list (E2EE §6, §8): direct adds of known contacts,
 * link invites from the owner's side, removal, leaving, and the rotations
 * that follow a leave, a revoked link or an unused one that expired. The
 * vault builds and signs every entry from its verified log; this sends them.
 */

import type { Account } from '../account/account.js';
import { toBase64url } from '../base64url.js';
import { formatInviteLink } from '../invite/link.js';
import type { ListId, ListState } from '../platform/vault.js';
import { append } from './logs.js';

/** A link invite lasts 48 hours unless asked otherwise (client-spec §3.4), at most 7 days (E2EE §4.2 rule 9). */
export const DEFAULT_INVITE_TTL_MS = 48 * 60 * 60 * 1000;

export interface CreatedInvite {
  /** `https://<server>/join#<s>`: send it over a channel the server doesn't see. */
  link: string;
  /** Identifies the invite in `ListState.openInvites`, to revoke it. */
  invitePub: Uint8Array;
  /** Unix milliseconds. */
  expires: number;
}

/** Owner: adds someone from a verified log of one of our lists (E2EE §6.3). */
export async function addMember(account: Account, list: ListId, signPub: Uint8Array): Promise<ListState> {
  return (await append(account, list, () => account.platform.vault.addMember(account.handle, list, signPub)))!;
}

/** Owner: removes a member, with a new epoch they never get (E2EE §8.1). */
export async function removeMember(account: Account, list: ListId, signPub: Uint8Array): Promise<ListState> {
  return (await append(account, list, () => account.platform.vault.removeMember(account.handle, list, signPub)))!;
}

/** Any member but the owner. The server stops serving the list to us; the caller forgets it here. */
export async function leaveList(account: Account, list: ListId): Promise<void> {
  await append(account, list, () => account.platform.vault.leave(account.handle, list));
}

/**
 * Owner: a link invite (E2EE §6.1). Appends the `invite` entry, then
 * stores the package for it; the link carries the secret in its fragment.
 * The list's `name` and the owner's own go into the package, for the Join screen.
 */
export async function createInvite(account: Account, list: ListId, name: string, ownerName: string, ttlMs = DEFAULT_INVITE_TTL_MS): Promise<CreatedInvite> {
  const { vault } = account.platform;
  let made: Awaited<ReturnType<typeof vault.createInvite>> | undefined;
  let expires = 0;
  await append(account, list, async () => {
    expires = Date.now() + ttlMs;
    made = await vault.createInvite(account.handle, list, expires, name, ownerName);
    return made.entry;
  });
  const { id, invitePub, sealedPackage, secret } = made!;
  await account.request('POST', `/v1/groups/${list}/invites`, {
    invite_id: toBase64url(id),
    package: toBase64url(sealedPackage),
    expires_at: new Date(expires).toISOString(),
    invite_pub: toBase64url(invitePub),
  });
  return { link: formatInviteLink(account.server, secret), invitePub, expires };
}

/**
 * Owner: closes a link invite with `uninvite`, which also stops the server
 * serving its package. The unused link carried the list key, so the list
 * rotates straight away (E2EE §6.1).
 */
export async function revokeInvite(account: Account, list: ListId, invitePub: Uint8Array): Promise<ListState> {
  await append(account, list, () => account.platform.vault.uninvite(account.handle, list, invitePub));
  return (await rotateIfDue(account, list)) ?? (await account.platform.vault.listState(account.handle, list));
}

/**
 * A new epoch, if one is due: after a `leave` (any member), or, for the
 * owner, after an `uninvite` or when an unused link invite has expired
 * (E2EE §6.1, §8.2). Re-checked after every `409`, so two members racing
 * rotate once. Returns the new state, or `null` when nothing was due.
 */
export async function rotateIfDue(account: Account, list: ListId, now = Date.now()): Promise<ListState | null> {
  const { vault } = account.platform;
  return append(account, list, async () => {
    const state = await vault.listState(account.handle, list);
    return rotationDue(state, toBase64url(account.identity.signPub), now) ? vault.rotate(account.handle, list) : null;
  });
}

/** Whether `me` should rotate `state` now. */
export function rotationDue(state: ListState, me: string, now: number): boolean {
  if (!state.members.some((m) => toBase64url(m.signPub) === me)) return false;
  const owner = toBase64url(state.owner.signPub) === me;
  if (state.rotationDue === 'member-left') return true;
  return owner && (state.rotationDue === 'invite-revoked' || state.openInvites.some((i) => i.expires < now));
}
