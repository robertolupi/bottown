import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    // dist is committed and embedded in the Go binary: fixed names make a
    // rebuild replace the same files in git instead of adding new hashed
    // ones. The server sends them with Cache-Control: no-cache instead.
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
