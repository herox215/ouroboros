import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';

// The window loads what go:embed carries in internal/plugins/gui/frontend:
// index.html, app.js and app.css, with paths relative to the page, so that the
// Wails asset server can serve them from its root. The names are fixed rather
// than hashed, so that a build tells the same files each time.
export default defineConfig({
  base: './',
  plugins: [svelte(), svelteTesting()],
  build: {
    outDir: '../frontend',
    emptyOutDir: true,
    target: 'es2022',
    cssCodeSplit: false,
    assetsInlineLimit: 0,
    rollupOptions: {
      output: {
        entryFileNames: 'app.js',
        chunkFileNames: 'chunks/[name].js',
        assetFileNames: (asset) =>
          asset.names?.some((name) => name.endsWith('.css')) ? 'app.css' : 'assets/[name]-[hash][extname]',
      },
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      include: ['src/lib/**/*.ts'],
      exclude: ['src/lib/demo.ts'],
    },
  },
});
