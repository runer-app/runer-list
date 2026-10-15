/**
 * Closes a popover (a menu, a dropdown) on a pointer press outside it, or
 * on Escape. Use on the element that holds both the popover and the button
 * that opens it, so pressing that button again toggles instead.
 */
export function dismiss(node: HTMLElement, close: () => void) {
  let current = close;
  const onPointer = (e: PointerEvent) => {
    if (!node.contains(e.target as Node)) current();
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'Escape') current();
  };
  document.addEventListener('pointerdown', onPointer, true);
  document.addEventListener('keydown', onKey);
  return {
    update(next: () => void) {
      current = next;
    },
    destroy() {
      document.removeEventListener('pointerdown', onPointer, true);
      document.removeEventListener('keydown', onKey);
    },
  };
}
