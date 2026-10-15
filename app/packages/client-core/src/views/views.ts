import type { Item, ListSnapshot } from '../list/item.js';
import { addDays } from './dates.js';

/** A task together with where it lives. */
export interface TaskRef {
  listId: string;
  listName: string;
  /** Position within its list, for stable ordering. */
  index: number;
  item: Item;
  /** Due before today. */
  overdue: boolean;
}

export interface ViewContext {
  /** The signed-in user's `identity_sign_pub` (base64url). */
  me: string;
  /** Today's local date, `YYYY-MM-DD` (`localDateString(new Date())`). */
  today: string;
  /** How many days after today count as "upcoming" rather than "planned". Default 7. */
  upcomingDays?: number;
}

export interface MyTasks {
  /** Due today or earlier (overdue first). */
  today: TaskRef[];
  /** Due in the next `upcomingDays` days. */
  upcoming: TaskRef[];
  /** Due later than that. */
  planned: TaskRef[];
}

/** A task is the user's when it is assigned to them or to nobody (client-spec §7.2, REWORK §3.1). */
export function isMine(item: Item, me: string): boolean {
  return item.assignee === undefined || item.assignee === me;
}

/**
 * Keeps one copy of an item id. A move between lists that was interrupted
 * leaves the item in both (list/move.ts); the copy in the list with the
 * smallest id wins, so every device picks the same one.
 */
export function dedupeById<T extends { listId: string }>(refs: T[], idOf: (r: T) => string): T[] {
  const best = new Map<string, T>();
  for (const r of refs) {
    const cur = best.get(idOf(r));
    if (cur === undefined || r.listId < cur.listId) best.set(idOf(r), r);
  }
  return refs.filter((r) => best.get(idOf(r)) === r);
}

/** Open tasks that are the user's, across all lists. */
export function openTasks(lists: readonly ListSnapshot[], ctx: ViewContext): TaskRef[] {
  const refs: TaskRef[] = [];
  for (const list of lists) {
    list.items.forEach((item, index) => {
      if (item.type !== 'task' || item.done || !isMine(item, ctx.me)) return;
      refs.push({ listId: list.id, listName: list.name, index, item, overdue: item.due !== undefined && item.due < ctx.today });
    });
  }
  return dedupeById(refs, (r) => r.item.id);
}

const byDueThenPlace = (a: TaskRef, b: TaskRef): number =>
  (a.item.due ?? '').localeCompare(b.item.due ?? '') ||
  a.listName.localeCompare(b.listName) ||
  a.listId.localeCompare(b.listId) ||
  a.index - b.index;

/** The Today view: open tasks that are the user's and due today or earlier. */
export function todayView(lists: readonly ListSnapshot[], ctx: ViewContext): TaskRef[] {
  return openTasks(lists, ctx)
    .filter((t) => t.item.due !== undefined && t.item.due <= ctx.today)
    .sort(byDueThenPlace);
}

/** "My tasks": today, upcoming and planned, across all lists. Undated tasks are not listed. */
export function myTasksView(lists: readonly ListSnapshot[], ctx: ViewContext): MyTasks {
  const horizon = addDays(ctx.today, ctx.upcomingDays ?? 7);
  const out: MyTasks = { today: [], upcoming: [], planned: [] };
  for (const t of openTasks(lists, ctx).sort(byDueThenPlace)) {
    const due = t.item.due;
    if (due === undefined) continue;
    if (due <= ctx.today) out.today.push(t);
    else if (due <= horizon) out.upcoming.push(t);
    else out.planned.push(t);
  }
  return out;
}

/** Open tasks in one list, for the count next to it in the lists screen. */
export function openTaskCount(list: ListSnapshot): number {
  return list.items.filter((i) => i.type === 'task' && !i.done).length;
}

/**
 * A view in the order the user dragged it into (`order`: item ids), kept
 * in their private Inbox document so every device of theirs agrees. Tasks
 * not in `order` follow, in the view's own order.
 */
export function inOrder(refs: readonly TaskRef[], order: readonly string[]): TaskRef[] {
  const rank = new Map(order.map((id, i) => [id, i]));
  return refs
    .map((r, i) => ({ r, i, k: rank.get(r.item.id) ?? Infinity }))
    .sort((a, b) => a.k - b.k || a.i - b.i)
    .map((x) => x.r);
}
