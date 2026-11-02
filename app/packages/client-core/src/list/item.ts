/** One row of a list. */
export interface Item {
  /** Stable across edits, moves and lists. */
  id: string;
  /**
   * May be a type this client does not know (written by a newer client). Show
   * such items as plain text and keep them as they are, never drop them.
   */
  type: string;
  title: string;
  body?: string;
  /** Tasks only. */
  done: boolean;
  /** Tasks only: calendar date `YYYY-MM-DD`, no time zone. */
  due?: string;
  /** Tasks only: absolute instant, Unix milliseconds. */
  remindAt?: number;
  /** Tasks only: `identity_sign_pub` (base64url) of a member from the verified log. */
  assignee?: string;
}

/** Everything the derived views need to know about one list. */
export interface ListSnapshot {
  id: string;
  name: string;
  items: Item[];
  /** `ListDoc.people()`: names by member id. */
  people?: Record<string, string>;
}

export class ListError extends Error {
  constructor(
    readonly code: 'invalid-date' | 'invalid-reminder',
    message: string,
  ) {
    super(message);
    this.name = 'ListError';
  }
}

const DATE = /^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$/;

/** A real calendar date in `YYYY-MM-DD` form. */
export function isDateString(s: string): boolean {
  return DATE.test(s) && new Date(`${s}T00:00:00Z`).toISOString().slice(0, 10) === s;
}
