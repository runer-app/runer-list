import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

// Only for the component tests; the app's bundle is built in apps/tauri.
export default defineConfig({
  plugins: [svelte()],
  resolve: process.env.VITEST ? { conditions: ['browser'] } : {},
  test: { environment: 'jsdom', include: ['test/**/*.test.ts'] },
} as never);
