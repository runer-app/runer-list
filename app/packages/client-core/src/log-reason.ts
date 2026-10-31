/** Our own errors whose text is fixed and worth reading. */
const KNOWN = new Set(['sync did not settle', 'offline']);

/**
 * What a failure may say in the device's log: its code, or fixed text, or
 * else only its kind. Never `message`, which can hold a server's words, a
 * URL or a piece of content (`JSON.parse` quotes its input).
 */
export function logReason(e: unknown): string {
  const { code, status } = (e ?? {}) as { code?: unknown; status?: unknown };
  if (typeof code === 'string' && /^[\w-]{1,40}$/.test(code)) return typeof status === 'number' ? `${status} ${code}` : code;
  if (e instanceof Error) return KNOWN.has(e.message) ? e.message : e.name;
  return 'unknown';
}
