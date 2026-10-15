/**
 * Joining a list from a link invite (E2EE §6.1): fetch the package by the
 * invite id, show what it says, verify the list's log from genesis through
 * the invite, and append our own `join`. The vault checks the package
 * against the verified log and keeps the list key it carries.
 */

import type { Account } from '../account/account.js';
import { fromBase64url, toBase64url } from '../base64url.js';
import { cleanName } from '../list/doc.js';
import type { Platform } from '../platform/index.js';
import { VaultError, type InviteHandle, type ListId, type PublicIdentity } from '../platform/vault.js';
import { append, catchUp } from '../sync/logs.js';
import { parseInviteLink } from './link.js';

/**
 * - `bad-link`: not an invite link.
 * - `other-server`: the link is for a server this account isn't on.
 * - `not-usable`: the invite was used, revoked, voided by a new epoch, or expired. If you didn't
 *   use it yourself, someone else may have: ask the owner, who sees who joined.
 * - `bad-package`: the server's package doesn't match the link or the list's log.
 */
export type InviteErrorCode = 'bad-link' | 'other-server' | 'not-usable' | 'bad-package';

export class InviteError extends Error {
  constructor(readonly code: InviteErrorCode) {
    super(`invite: ${code}`);
    this.name = 'InviteError';
  }
}

/** An invite link opened on this device: what its package says, for the Join screen. */
export interface OpenedInvite {
  /** The list's server, from the link. */
  server: string;
  list: ListId;
  /** The list's name when the link was made. */
  name: string;
  /** The owner's name when the link was made, cleaned (`cleanName`); empty when the owner had none. */
  ownerName: string;
  owner: PublicIdentity;
  readonly invite: InviteHandle;
  /** The invite id as the server knows it (base64url). */
  readonly id: string;
}

/**
 * Opens a link and reads its package; needs no account. `platform` must
 * talk to the link's server (`parseInviteLink(link).server`).
 */
export async function openInvite(platform: Platform, link: string): Promise<OpenedInvite> {
  const parsed = parseInviteLink(link);
  if (parsed === null) throw new InviteError('bad-link');
  const { invite, id } = await platform.vault.openInviteLink(parsed.secret);
  const res = await platform.transport.request('GET', `/v1/invites/${toBase64url(id)}`);
  if (res.status !== 200) throw new InviteError(res.status >= 500 || res.status === 429 ? 'bad-package' : 'not-usable');
  const sealed = fromBase64url((res.body as { data: { package: string } }).data.package);
  const preview = await platform.vault.readInvitePackage(invite, sealed).catch(() => {
    throw new InviteError('bad-package');
  });
  return { server: parsed.server, ...preview, ownerName: cleanName(preview.ownerName), invite, id: toBase64url(id) };
}

/**
 * Joins the list as `account`, which must be on the invite's server.
 * Already a member: nothing to do. Returns the list's id.
 */
export async function joinInvite(account: Account, opened: OpenedInvite): Promise<ListId> {
  const { vault } = account.platform;
  const me = toBase64url(account.identity.signPub);
  const known = await vault.listState(account.handle, opened.list).catch(() => null);
  if (known?.members.some((m) => toBase64url(m.signPub) === me)) return opened.list;
  // The invitee isn't a member yet, so the log comes through the invite.
  const fetchLog = () => catchUp(account, opened.list, `/v1/invites/${opened.id}/log`);
  try {
    await fetchLog();
    await append(account, opened.list, () => vault.join(account.handle, opened.invite), {
      path: `/v1/invites/${opened.id}/accept`,
      catchUp: fetchLog,
    });
  } catch (e) {
    if (known === null) await vault.forgetList(account.handle, opened.list).catch(() => undefined);
    throw toInviteError(e);
  }
  return opened.list;
}

function toInviteError(e: unknown): unknown {
  if (e instanceof VaultError) return new InviteError(e.code === 'InvalidState' && /invite/.test(e.message) ? 'not-usable' : 'bad-package');
  const status = (e as { status?: number }).status;
  if (status === 403 || status === 404 || status === 410) return new InviteError('not-usable');
  return e;
}
