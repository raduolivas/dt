import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// `make mc-web-dev` runs this dev server and proxies the API to mc-backend on 127.0.0.1:8095.
export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8095',
      '/livez': 'http://127.0.0.1:8095',
      '/readyz': 'http://127.0.0.1:8095',
    },
  },
  build: { outDir: 'dist', sourcemap: false },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
});
