import { resolve } from 'node:path';
import { defineConfig } from 'vitest/config';

// `vite` serves the dev simulator page (src/dev). `vite build` builds the library (src/index.ts).
export default defineConfig({
  root: resolve(import.meta.dirname, 'src/dev'),
  server: { host: true },
  test: { root: import.meta.dirname },
  build: {
    outDir: resolve(import.meta.dirname, 'dist'),
    emptyOutDir: true,
    lib: {
      entry: resolve(import.meta.dirname, 'src/index.ts'),
      formats: ['es'],
      fileName: 'index',
    },
    rollupOptions: { external: [/^three(\/.*)?$/] },
  },
});
