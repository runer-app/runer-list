import type { ListSnapshot } from '../list/item.js';
import type { Reminder } from '../platform/index.js';
import { dedupeById, isMine } from '../views/views.js';

/** Notification title when the user hides task titles. The UI may pass its own translation. */
export const GENERIC_REMINDER_TITLE = 'Reminder';

export interface ReminderPlanOptions {
  lists: readonly ListSnapshot[];
  /** The signed-in user's `identity_sign_pub` (base64url). */
  me: string;
  /** Now, Unix milliseconds. */
  now: number;
  /** `Notifier.maxPending`. */
  maxPending: number;
  /** Show the task title in the notification (default), or a generic text (privacy setting). */
  showTitle?: boolean;
  genericTitle?: string;
}

/**
 * Which notifications this device should have scheduled.
 * Run after every sync and pass the result to `Notifier.replaceAll`.
 *
 * A task qualifies when it has a future `remind_at`, is not done, and is
 * assigned to the user or to nobody. The nearest `maxPending` win; the rest
 * are topped up on a later sync. Reminders already in the past are dropped,
 * not fired late.
 */
export function planReminders(opts: ReminderPlanOptions): Reminder[] {
  const candidates: { listId: string; listName: string; id: string; at: number; title: string }[] = [];
  for (const list of opts.lists) {
    for (const item of list.items) {
      if (item.type !== 'task' || item.done || item.remindAt === undefined) continue;
      if (item.remindAt <= opts.now || !isMine(item, opts.me)) continue;
      candidates.push({ listId: list.id, listName: list.name, id: item.id, at: item.remindAt, title: item.title });
    }
  }
  const showTitle = opts.showTitle ?? true;
  return dedupeById(candidates, (c) => c.id)
    .sort((a, b) => a.at - b.at || a.id.localeCompare(b.id))
    .slice(0, Math.max(0, opts.maxPending))
    .map((c) => ({
      id: `${c.listId}:${c.id}`,
      at: new Date(c.at),
      title: showTitle ? c.title : (opts.genericTitle ?? GENERIC_REMINDER_TITLE),
      ...(showTitle ? { body: c.listName } : {}),
    }));
}
