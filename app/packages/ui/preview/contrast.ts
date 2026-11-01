/**
 * Text too faint against what's behind it, by the WCAG ratio. Runs in the page (`page.evaluate`), so it stands alone.
 * Plain backgrounds only: it doesn't see images or gradients. The logo is left out, as WCAG does.
 */
export function lowContrast(min: number): string[] {
  type Rgba = [number, number, number, number];
  const ctx = document.createElement('canvas').getContext('2d', { willReadFrequently: true })!;
  const rgba = (css: string): Rgba => {
    ctx.clearRect(0, 0, 1, 1);
    ctx.fillStyle = css;
    ctx.fillRect(0, 0, 1, 1);
    const [r, g, b, a] = ctx.getImageData(0, 0, 1, 1).data;
    return [r!, g!, b!, a! / 255];
  };
  const over = (top: Rgba, under: Rgba): Rgba => {
    const mix = (i: 0 | 1 | 2) => top[i] * top[3] + under[i] * (1 - top[3]);
    return [mix(0), mix(1), mix(2), 1];
  };
  const behind = (el: Element | null): Rgba => {
    if (el === null) return [255, 255, 255, 1];
    const bg = rgba(getComputedStyle(el).backgroundColor);
    return bg[3] === 1 ? bg : over(bg, behind(el.parentElement));
  };
  const light = ([r, g, b]: Rgba) => {
    const c = (v: number) => (v / 255 <= 0.03928 ? v / 255 / 12.92 : ((v / 255 + 0.055) / 1.055) ** 2.4);
    return 0.2126 * c(r) + 0.7152 * c(g) + 0.0722 * c(b);
  };

  const faint: string[] = [];
  for (const el of document.querySelectorAll('body *')) {
    const text = [...el.childNodes].filter((n) => n.nodeType === Node.TEXT_NODE).map((n) => n.textContent).join('').trim();
    if (text === '' || ['SCRIPT', 'STYLE', 'TITLE'].includes(el.tagName) || el.closest(':disabled, .logo') !== null) continue;
    const box = el.getBoundingClientRect();
    if (box.width === 0 || getComputedStyle(el).visibility === 'hidden') continue;
    const bg = behind(el);
    const a = light(over(rgba(getComputedStyle(el).color), bg));
    const b = light(bg);
    const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
    if (ratio < min) faint.push(`${ratio.toFixed(2)} ${el.tagName.toLowerCase()}.${String(el.className).split(' ')[0]}: ${text.slice(0, 40)}`);
  }
  return [...new Set(faint)];
}
