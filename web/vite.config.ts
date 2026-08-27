import { defineConfig } from "vitest/config";
import { resolve } from "path";

const monacoDir = resolve(__dirname, "node_modules/monaco-editor/esm/vs");

export default defineConfig({
  resolve: {
    alias: [
      // monaco-vim's "browser" export points to a UMD bundle that Vite can't handle.
      // Redirect to the ESM entry directly via an absolute path to bypass exports checks,
      // and map its internal monaco-editor imports to their real .js files.
      { find: /^monaco-vim$/, replacement: resolve(__dirname, "node_modules/monaco-vim/dist/index.mjs") },
      { find: "monaco-editor/esm/vs/editor/editor.api", replacement: resolve(monacoDir, "editor/editor.api.js") },
      { find: "monaco-editor/esm/vs/editor/common/commands/shiftCommand", replacement: resolve(monacoDir, "editor/common/commands/shiftCommand.js") },
    ],
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    manifest: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:3740",
      "/ws": { target: "ws://127.0.0.1:3740", ws: true },
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts"],
  },
});
