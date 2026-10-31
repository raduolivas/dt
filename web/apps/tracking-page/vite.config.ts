import { resolve } from 'node:path';
import { defineConfig } from 'vite';

// The hosted tracking page. The gateway serves the HTML shell (GET /t/{token}) and links `<base>entry.css` and
// `<base>entry.js` from the CDN, so those two names are fixed; every other chunk is hashed. Files are immutable on the
// CDN (page/<git-sha>/…), and `base: './'` lets entry.js find its chunks next to itself whatever the prefix is.
// `vite` (dev) serves index.html, a copy of the shell with a local config.
export default defineConfig({
  base: './',
  build: {
    outDir: resolve(import.meta.dirname, 'dist'),
    emptyOutDir: true,
    // the shell names entry.css itself; a single stylesheet keeps that name meaningful
    cssCodeSplit: false,
    // one bundle with Three.js on purpose (the SDK's own build checks the size budget); no warning for it
    chunkSizeWarningLimit: 1024,
    rolldownOptions: {
      input: resolve(import.meta.dirname, 'src/main.ts'),
      output: {
        entryFileNames: 'entry.js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: (a) =>
          a.names.some((n) => n.endsWith('.css')) ? 'entry.css' : 'assets/[name]-[hash][extname]',
        minify: true,
      },
    },
  },
});
