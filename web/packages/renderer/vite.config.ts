import { resolve } from 'node:path';
import { defineConfig } from 'vitest/config';

// `vite` serves the dev simulator page (src/dev). `vite build` builds the library (src/index.ts).
export default defineConfig({
  root: resolve(import.meta.dirname, 'src/dev'),
  server: { host: true },
  // The dev page serves the Amsterdam tile fixture at /amsterdam-z14.pmtiles; the library build does not copy it.
  publicDir: resolve(import.meta.dirname, 'fixtures'),
  test: { root: import.meta.dirname },
  build: {
    outDir: resolve(import.meta.dirname, 'dist'),
    emptyOutDir: true,
    copyPublicDir: false,
    lib: {
      entry: resolve(import.meta.dirname, 'src/index.ts'),
      formats: ['es'],
      fileName: 'index',
    },
    rollupOptions: { external: [/^three(\/.*)?$/] },
  },
});
