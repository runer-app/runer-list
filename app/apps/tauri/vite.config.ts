import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

// Tauri serves the built files from `dist` (tauri.conf.json); `tauri dev` uses this dev server.
export default defineConfig(({ command }) => {
  // A build never falls back to the local API: `make build-dev`, `make build-prod` and release.yml set it.
  if (command === 'build' && !process.env['VITE_RUNER_SERVER']) {
    throw new Error('VITE_RUNER_SERVER is not set: use `make build-dev` or `make build-prod`');
  }
  return {
    plugins: [svelte()],
    clearScreen: false,
    // TAURI_ENV_DEBUG: the Tauri CLI sets it for a debug build (main.ts turns the sync log on).
    envPrefix: ['VITE_', 'TAURI_ENV_'],
    server: { port: 5173, strictPort: true },
    build: { target: ['es2022', 'safari15'], outDir: 'dist', emptyOutDir: true },
  };
});
