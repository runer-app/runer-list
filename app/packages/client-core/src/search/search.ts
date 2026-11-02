import type { Item, ListSnapshot } from '../list/item.js';
import { dedupeById } from '../views/views.js';

export interface SearchHit {
  listId: string;
  listName: string;
  /** Absent when the list's own name matched. */
  item?: Item;
  field: 'name' | 'title' | 'body';
  score: number;
}

/** Lower-case and strip accents, so "cafe" finds "Café". */
export function normalize(s: string): string {
  return s.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase();
}

/**
 * Local full-text search over decrypted lists: every word of the query must
 * appear, in any order. Title matches rank above body matches; open tasks
 * above done ones.
 */
export function searchLists(lists: readonly ListSnapshot[], query: string, limit = 50): SearchHit[] {
  const terms = normalize(query).split(/\s+/).filter((t) => t !== '');
  if (terms.length === 0) return [];
  const all = (text: string) => terms.every((t) => text.includes(t));

  const hits: { listId: string; key: string; hit: SearchHit }[] = [];
  for (const list of lists) {
    if (all(normalize(list.name))) hits.push({ listId: list.id, key: `list:${list.id}`, hit: { listId: list.id, listName: list.name, field: 'name', score: 3 } });
    for (const item of list.items) {
      const title = normalize(item.title);
      const body = normalize(item.body ?? '');
      const inTitle = all(title);
      if (!inTitle && !all(`${title} ${body}`)) continue;
      const open = item.type === 'task' && !item.done ? 0.5 : 0;
      hits.push({
        listId: list.id,
        key: item.id,
        hit: { listId: list.id, listName: list.name, item, field: inTitle ? 'title' : 'body', score: (inTitle ? 2 : 1) + open },
      });
    }
  }
  return dedupeById(hits, (h) => h.key)
    .map((h) => h.hit)
    .sort((a, b) => b.score - a.score || a.listName.localeCompare(b.listName) || a.listId.localeCompare(b.listId))
    .slice(0, limit);
}
