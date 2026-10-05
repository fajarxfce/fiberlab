import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/site/dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: { proxy: { '/api': { target: 'http://127.0.0.1:8787', ws: true } } },
});
