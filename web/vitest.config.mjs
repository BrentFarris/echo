import { resolve } from 'node:path';

if (process.platform === 'win32' && process.env.ECHO_WORKSPACE) {
  // Vitest's install probe uses its own realpath walker, which inspects denied
  // ancestors. Verify the dependency with Node's scoped resolver instead; actual
  // imports still fail normally if an installed dependency is incomplete.
  import.meta.resolve('jsdom');
  process.env.VITEST_SKIP_INSTALL_CHECKS = '1';
}

// Load directly with Node: bundling this config invokes Vite's Windows drive
// detection before preserveSymlinks takes effect. Older libuv versions hang
// when that detection spawns `net use` with pipes inside an AppContainer.
export default {
  resolve: {
    preserveSymlinks: true,
    alias: [{ find: /^.*\/dompurify\/dompurify\.js$/, replacement: resolve(import.meta.dirname, 'src/code/monacoSanitizer.ts') }],
  },
  test: {
    // Worker threads keep test isolation without child-process IPC pipes.
    pool: 'threads',
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
  },
};
