import { resolve } from 'node:path';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';

/**
 * The renderer imports its CSS for pages that use it directly; the SDK puts that CSS inside its shadow root instead
 * (src/styles.ts), so the extracted stylesheet would be an unused second file. Drop it: each bundle is one file.
 */
const noCssFile = (): Plugin => ({
  name: 'dt-no-css-file',
  enforce: 'post',
  generateBundle(_, bundle) {
    for (const name of Object.keys(bundle)) if (name.endsWith('.css')) delete bundle[name];
  },
});

// `vite build` makes the SDK: an ES module and a single-file IIFE that registers <dt-tracking>. Both bundle Three.js,
// the renderer (with its inline tile worker) and the viewer client, so a partner page needs one script tag.
// Defaults per environment are baked in from VITE_DT_API and VITE_DT_TILES (see src/mount.ts).
export default defineConfig({
  plugins: [noCssFile()],
  test: {
    environment: 'happy-dom',
    // ETA windows are local clock times: fix the zone so the expected "12:40–12:50" holds on every machine.
    env: { TZ: 'Europe/Amsterdam' },
  },
  build: {
    outDir: resolve(import.meta.dirname, 'dist'),
    emptyOutDir: true,
    // the ES module is loaded from the CDN as is (npm publishing is M8), so minify it fully like the IIFE
    rolldownOptions: { output: { minify: true } },
    lib: {
      entry: resolve(import.meta.dirname, 'src/index.ts'),
      name: 'DtTracking',
      formats: ['es', 'iife'],
      fileName: (format) => (format === 'es' ? 'tracking.js' : 'tracking.iife.js'),
    },
  },
});
