import { defineConfig } from 'vitest/config';
import { resolve } from 'node:path';
import type { ClientRequest, IncomingMessage } from 'node:http';
export default defineConfig({
  build: { rollupOptions: { input: { main: resolve(import.meta.dirname, 'index.html'), renderer: resolve(import.meta.dirname, 'renderer.html') } } },
  server: { port: 5174, cors: { origin: /^(http:\/\/(localhost|127\.0\.0\.1):5174|null)$/ }, proxy: { '/api': { target: 'http://127.0.0.1:3740', ws: true, changeOrigin: true, configure(proxy) {
    const rewrite = (request: ClientRequest, incoming: IncomingMessage) => { if (incoming.headers.origin === `http://${incoming.headers.host}`) request.setHeader('Origin', 'http://127.0.0.1:3740'); };
    proxy.on('proxyReq', rewrite); proxy.on('proxyReqWs', rewrite);
  } } } },
  test: { environment: 'jsdom', include: ['src/**/*.test.ts'] }
});
