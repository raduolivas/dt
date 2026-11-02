import { resolve } from 'node:path';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';

// The hosted courier page (courier.<domain>). Files are hashed under assets/ (scripts/size.mjs checks them by pattern).
// The API base is baked in from VITE_DT_API (src/main.ts); the build puts the same origin in the CSP's connect-src.
const api = process.env.VITE_DT_API || 'http://localhost:8080/v1';

/** Production builds carry a CSP meta; `vite` (dev) needs inline HMR scripts and gets none. */
function csp(): Plugin {
  return {
    name: 'dt-courier-csp',
    transformIndexHtml: {
      order: 'post',
      handler(html, ctx) {
        const meta = ctx.server
          ? ''
          : `<meta http-equiv="Content-Security-Policy" content="default-src 'self'; connect-src ${new URL(api).origin}" />`;
        return html.replace('<!--csp-->', meta);
      },
    },
  };
}

export default defineConfig({
  // absolute: the shell is served at /s/<code> and / (nginx falls back to index.html), so relative asset URLs would break
  base: '/',
  plugins: [csp()],
  build: {
    outDir: resolve(import.meta.dirname, 'dist'),
    emptyOutDir: true,
    cssCodeSplit: false,
    rolldownOptions: {
      output: {
        entryFileNames: 'assets/courier-[hash].js',
        chunkFileNames: 'assets/chunk-[hash].js',
        assetFileNames: (a) =>
          a.names.some((n) => n.endsWith('.css')) ? 'assets/courier-[hash].css' : 'assets/[name]-[hash][extname]',
        minify: true,
      },
    },
  },
  test: { environment: 'happy-dom' },
});
