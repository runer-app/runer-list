import type { ListHandle } from '@runer/client-core';
import { dueLabel } from './dates.js';
import { t } from './messages.js';

/** The short message after ticking a repeating task ("Next: Monday"); `n` counts, so the same text shows again. */
export const tickNote = $state({ text: '', n: 0 });

/**
 * Ticks a task from its checkbox. A repeating task isn't done afterwards but
 * on its next date (`ListDoc.toggleDone`), so the box is unticked again and
 * the new date is said. There is no Undo.
 */
export function tickTask(list: ListHandle, id: string, box?: EventTarget | null): void {
  const r = list.toggleDone(id);
  if (r === false || r.next === undefined) return;
  if (box instanceof HTMLInputElement) box.checked = false;
  tickNote.text = t('task.next', { when: dueLabel(r.next) });
  tickNote.n++;
}
