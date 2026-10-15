import { platformKind } from '@runer/platform-tauri';

/**
 * The window's own chrome. On macOS the title bar is an overlay
 * (tauri.conf.json): the app runs up to the top edge and the traffic
 * lights sit over the sidebar, so the UI keeps that much room free
 * (`--titlebar`, theme.css) and the top edge drags the window.
 */
export function setUpChrome(): void {
  if (/Mac/.test(navigator.userAgent) && platformKind() === 'desktop') {
    document.documentElement.style.setProperty('--titlebar', '36px');
  }
}
