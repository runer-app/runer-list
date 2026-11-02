import type { ListDoc } from './doc.js';

/**
 * Moves an item between lists (e.g. Inbox to a list) keeping its id: insert
 * into the target first, then delete from the source. A crash or an offline
 * gap between the two steps leaves the item in both lists, never in neither;
 * views de-duplicate by id, and calling this again finishes the move (the
 * insert is a no-op if the id is already there).
 *
 * Returns false if the item is in neither list.
 */
export function moveItem(from: ListDoc, to: ListDoc, id: string, index?: number): boolean {
  const fields = from.exportItem(id);
  if (fields !== undefined) to.importItem(id, fields, index);
  else if (!to.has(id)) return false;
  from.remove(id);
  return true;
}
