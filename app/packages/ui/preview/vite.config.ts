import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

// The design preview (`npm run design -w @runer/ui`): the real screens in a browser, on sample data. Not shipped.
export default defineConfig({
  root: import.meta.dirname,
  plugins: [svelte()],
  server: { port: 5199, strictPort: true },
});
