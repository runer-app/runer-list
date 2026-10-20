/**
 * Invite links (E2EE §6): `https://<server>/join#<s>`, or
 * the app's own `runer://join?host=<server>#<s>` that the server's `/join`
 * page hands over. The fragment is the link secret `s`: it is a key, so
 * nothing here logs it, puts it in an error, or sends it anywhere.
 */

import { fromBase64url, toBase64url } from '../base64url.js';

export interface InviteLink {
  /** The API base URL of the list's server; the joiner signs in there. */
  server: string;
  /** The link secret `s`, 32 bytes. */
  secret: Uint8Array;
}

/** The app's own link scheme, as registered in `tauri.conf.json` and used by the server's `/join` page. */
const APP_SCHEME = 'runer';

const SECRET_LEN = 32;

/** The link to send: `<server>/join#<base64url(s)>`, on the list's own server. */
export function formatInviteLink(server: string, secret: Uint8Array): string {
  return `${server.replace(/\/+$/, '')}/join#${toBase64url(secret)}`;
}

/** The server and secret in an invite link, or `null` if it isn't one. Says nothing about why. */
export function parseInviteLink(link: string): InviteLink | null {
  let url: URL;
  try {
    url = new URL(link.trim());
  } catch {
    return null;
  }
  const secret = decodeSecret(url.hash.slice(1));
  if (secret === null) return null;
  if (url.protocol === 'https:' || url.protocol === 'http:') {
    if (!url.pathname.endsWith('/join')) return null;
    return { server: url.origin + url.pathname.slice(0, -'/join'.length), secret };
  }
  if (url.protocol === `${APP_SCHEME}:` && (url.host === 'join' || url.pathname.replace(/^\/+/, '') === 'join')) {
    const server = serverOf(url.searchParams.get('host'));
    return server === null ? null : { server, secret };
  }
  return null;
}

/** `host` is a base URL, or a bare host for an `https://` one. */
function serverOf(host: string | null): string | null {
  if (host === null || host === '') return null;
  try {
    const u = new URL(/^https?:\/\//.test(host) ? host : `https://${host}`);
    if (u.search !== '' || u.hash !== '' || u.username !== '' || u.password !== '') return null;
    return (u.origin + u.pathname).replace(/\/+$/, '');
  } catch {
    return null;
  }
}

function decodeSecret(fragment: string): Uint8Array | null {
  if (!/^[A-Za-z0-9_-]{43}$/.test(fragment)) return null;
  const secret = fromBase64url(fragment);
  return secret.length === SECRET_LEN ? secret : null;
}
