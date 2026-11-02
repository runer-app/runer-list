/** base64url without padding, the API's encoding for binary values. No Node or DOM needed. */

const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
const INDEX = new Map([...ALPHABET].map((c, i) => [c, i]));

export function toBase64url(bytes: Uint8Array): string {
  let out = '';
  for (let i = 0; i < bytes.length; i += 3) {
    const n = (bytes[i]! << 16) | ((bytes[i + 1] ?? 0) << 8) | (bytes[i + 2] ?? 0);
    const chars = Math.min(4, Math.ceil(((bytes.length - i) * 8) / 6));
    for (let j = 0; j < chars; j++) out += ALPHABET[(n >> (18 - 6 * j)) & 63];
  }
  return out;
}

export function fromBase64url(s: string): Uint8Array {
  if (s.length % 4 === 1) throw new Error('invalid base64url');
  const out = new Uint8Array(Math.floor((s.length * 6) / 8));
  let bits = 0;
  let acc = 0;
  let o = 0;
  for (const c of s) {
    const v = INDEX.get(c);
    if (v === undefined) throw new Error('invalid base64url');
    acc = (acc << 6) | v;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      out[o++] = (acc >> bits) & 0xff;
    }
  }
  return out;
}
