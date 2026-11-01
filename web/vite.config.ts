import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte()],
  build: {
    // Not tracked: the Go binary embeds internal/webui/dist, whose tracked
    // README keeps the directory valid when ui/ was not built.
    outDir: '../internal/webui/dist/ui',
    emptyOutDir: true,
    // Fixed names: a rebuild replaces the same files. The server sends them
    // with Cache-Control: no-cache, since a new binary serves new content
    // at the same URLs.
    rollupOptions: {
      output: {
        entryFileNames: 'assets/[name].js',
        chunkFileNames: 'assets/[name].js',
        assetFileNames: 'assets/[name].[ext]'
      }
    }
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:3031'
    }
  }
});
