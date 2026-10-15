/**
 * Dragging a row by its handle, with pointer events so mouse, pen and
 * touch work alike (HTML drag and drop doesn't on touch). The rows are
 * asked for at each move, so the list can change underneath. A gap is a
 * position between rows: 0 is before the first, `rows.length` after the last.
 */
export interface DragOptions {
  /** The rows in order, as they are on screen. */
  rows: () => (HTMLElement | undefined)[];
  /** The dragged row's index. */
  from: number;
  /** The gap the row would drop into, or `null` where dropping changes nothing. */
  hover: (gap: number | null) => void;
  drop: (gap: number) => void;
}

/** The gap nearest `y`: before the first row whose middle is below it. */
export function gapAt(rows: (HTMLElement | undefined)[], y: number): number {
  for (const [i, el] of rows.entries()) {
    const r = el?.getBoundingClientRect();
    if (r !== undefined && y < r.top + r.height / 2) return i;
  }
  return rows.length;
}

/** Starts a drag from a pointerdown on a handle. */
export function startDrag(e: PointerEvent, opts: DragOptions): void {
  if (e.button !== 0) return;
  e.preventDefault();
  let gap: number | null = null;
  const move = (ev: PointerEvent) => {
    const g = gapAt(opts.rows(), ev.clientY);
    gap = g === opts.from || g === opts.from + 1 ? null : g;
    opts.hover(gap);
  };
  const stop = (drop: boolean) => () => {
    window.removeEventListener('pointermove', move);
    window.removeEventListener('pointerup', up);
    window.removeEventListener('pointercancel', cancel);
    opts.hover(null);
    if (drop && gap !== null) opts.drop(gap);
  };
  const up = stop(true);
  const cancel = stop(false);
  window.addEventListener('pointermove', move);
  window.addEventListener('pointerup', up);
  window.addEventListener('pointercancel', cancel);
}

/** The gap one step up or down from row `from`, for ↑/↓ on a handle; `null` at an end. */
export function stepGap(from: number, count: number, key: string): number | null {
  if (key !== 'ArrowUp' && key !== 'ArrowDown') return null;
  const gap = key === 'ArrowUp' ? from - 1 : from + 2;
  return gap < 0 || gap > count ? null : gap;
}
