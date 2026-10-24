/**
 * How the app looks on this device: a theme, in light or dark. Kept in the
 * webview's own storage, not in the account: it is the device's, it holds
 * before anyone signs in, and it is read synchronously, so the first paint
 * is already in the right colors. The colors themselves are in theme.css,
 * under `[data-theme]` and `[data-mode]` on the root element.
 */

export const THEMES = ['default', 'moss', 'harbor', 'ember'] as const;
export type Theme = (typeof THEMES)[number];
/** `system` follows the device's light or dark setting. */
export type Mode = 'system' | 'light' | 'dark';

const KEY = 'runer.appearance';

/** The choice, and the mode it comes to: `resolved` is what is drawn now. */
export const appearance = $state<{ theme: Theme; mode: Mode; resolved: 'light' | 'dark' }>({ theme: 'default', mode: 'system', resolved: 'dark' });

let systemLight: MediaQueryList | null = null;

/** The app entry calls this once, before mounting the UI. */
export function startAppearance(): void {
  systemLight = typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: light)') : null;
  systemLight?.addEventListener('change', apply);
  const stored = read();
  appearance.theme = THEMES.includes(stored.theme as Theme) ? (stored.theme as Theme) : 'default';
  appearance.mode = stored.mode === 'light' || stored.mode === 'dark' ? stored.mode : 'system';
  apply();
}

export function setTheme(theme: Theme): void {
  appearance.theme = theme;
  write();
  apply();
}

export function setMode(mode: Mode): void {
  appearance.mode = mode;
  write();
  apply();
}

function apply(): void {
  appearance.resolved = appearance.mode === 'system' ? (systemLight?.matches ? 'light' : 'dark') : appearance.mode;
  document.documentElement.dataset['theme'] = appearance.theme;
  document.documentElement.dataset['mode'] = appearance.resolved;
}

// Storage can be missing or refuse (a private window): the choice then lasts until the app closes.
function read(): { theme?: unknown; mode?: unknown } {
  try {
    return (JSON.parse(localStorage.getItem(KEY) ?? '{}') as { theme?: unknown; mode?: unknown } | null) ?? {};
  } catch {
    return {};
  }
}

function write(): void {
  try {
    localStorage.setItem(KEY, JSON.stringify({ theme: appearance.theme, mode: appearance.mode }));
  } catch {
    // Kept for this run only.
  }
}
