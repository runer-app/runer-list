import { toBase64url, type ListSummary, type PublicIdentity } from '@runer/client-core';
import { t } from './messages.js';

/**
 * Members go by their account names, which each one's app writes into the
 * list (`ListSummary.names`). Until a name has synced, a member is shown by
 * a short tag from their key, "Member 3FA2", the same on every device.
 */

/** What people are called, by member id. */
export type Names = Readonly<Record<string, string>>;

/** `identity_sign_pub` as base64url: what `assignee` and `ClientCore.me` hold. */
export const idOf = (p: PublicIdentity): string => toBase64url(p.signPub);

/** Four hex digits of the signing key. */
export function tagOf(signPub: Uint8Array): string {
  return [...signPub.slice(0, 2)].map((b) => b.toString(16).padStart(2, '0')).join('').toUpperCase();
}

const tagOfId = (id: string, members: readonly PublicIdentity[]): string => {
  const m = members.find((p) => idOf(p) === id);
  return m === undefined ? id.slice(0, 4).toUpperCase() : tagOf(m.signPub);
};

/** Every name from every list, for someone seen outside the list at hand (a known contact, a notice). */
export function allNames(lists: readonly ListSummary[]): Names {
  return Object.assign({}, ...lists.map((l) => l.names)) as Names;
}

/** "You", "Anna", or "Member 3FA2". */
export function personName(id: string, me: string, members: readonly PublicIdentity[] = [], names: Names = {}): string {
  if (id === me) return t('people.you');
  return names[id] ?? t('people.member', { tag: tagOfId(id, members) });
}

/** "Anna", or "Member 3FA2", for me too: how others see someone. */
export function nameOf(id: string, members: readonly PublicIdentity[] = [], names: Names = {}): string {
  return names[id] ?? t('people.member', { tag: tagOfId(id, members) });
}

/** One letter or digit for an avatar. */
export function initialOf(id: string, me: string, members: readonly PublicIdentity[], names: Names = {}): string {
  const name = names[id];
  if (name !== undefined) return [...name][0]!.toUpperCase();
  if (id === me) return t('people.you').slice(0, 1);
  const m = members.find((p) => idOf(p) === id);
  return m === undefined ? '?' : tagOf(m.signPub).slice(0, 1);
}

/** A muted colour per person, the same on every device. */
const COLORS = ['#6e5ba8', '#8a5775', '#4f7a8c', '#7a6a3f', '#5b7f5f', '#8c5a4a', '#5a5f9c'];
export function colorOf(id: string): string {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) | 0;
  return COLORS[Math.abs(h) % COLORS.length]!;
}

/** "Only you", "You and Anna", "You and 3 others". */
export function membersSummary(members: readonly PublicIdentity[], me: string, names: Names = {}): string {
  const others = members.filter((m) => idOf(m) !== me);
  if (others.length === 0) return t('people.onlyYou');
  if (others.length === 1) return t('people.youAnd', { name: personName(idOf(others[0]!), me, members, names) });
  return t('people.youAndOthers', { n: others.length });
}
