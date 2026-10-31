import { resolve } from 'node:path';
import { defineConfig } from 'vitest/config';

// `vite build` makes the SDK as one minified ES module. Partners bundle it into their courier app; the courier
// link page (apps/courier-page) imports the source directly.
export default defineConfig({
  test: { environment: 'happy-dom' },
  build: {
    outDir: resolve(import.meta.dirname, 'dist'),
    emptyOutDir: true,
    rolldownOptions: { output: { minify: true } },
    lib: {
      entry: resolve(import.meta.dirname, 'src/index.ts'),
      formats: ['es'],
      fileName: () => 'courier.js',
    },
  },
});
