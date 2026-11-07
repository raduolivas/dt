import react from '@vitejs/plugin-react';
import type { ProxyOptions } from 'vite';
import { defineConfig } from 'vitest/config';

const backend = 'http://127.0.0.1:8095';
const devOrigins = new Set(['http://127.0.0.1:5173', 'http://localhost:5173']);

// mc-backend answers only Host 127.0.0.1/localhost/[::1] with its own port (DNS-rebinding guard), so the
// proxy rewrites Host (changeOrigin). The sim routes refuse an Origin that differs from Host, so this dev
// server's own Origin is mapped to the backend's; any other Origin is passed through unchanged and refused.
const proxy: ProxyOptions = {
  target: backend,
  changeOrigin: true,
  configure: (p) => {
    p.on('proxyReq', (req) => {
      const o = req.getHeader('origin');
      if (typeof o === 'string' && devOrigins.has(o)) req.setHeader('origin', backend);
    });
  },
};

// `make mc-web-dev` runs this dev server and proxies the API to mc-backend on 127.0.0.1:8095.
export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: { '/api': proxy, '/livez': proxy, '/readyz': proxy },
  },
  build: { outDir: 'dist', sourcemap: false },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
});
